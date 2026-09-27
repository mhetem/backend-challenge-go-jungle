#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
app1="${SMOKE_APP1:-http://localhost:8081}"
app2="${SMOKE_APP2:-http://localhost:8082}"
app3="${SMOKE_APP3:-http://localhost:8083}"
run="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
player="$(cat /proc/sys/kernel/random/uuid 2>/dev/null || uuidgen | tr 'A-Z' 'a-z')"
body="$(mktemp)"
trap 'rm -f "$body"' EXIT

fail() {
    echo "smoke: $*" >&2
    if [ -s "$body" ]; then
        echo "smoke: last response: $(cat "$body")" >&2
    fi
    exit 1
}

step() {
    echo "smoke: $*"
}

call() {
    method="$1" url="$2" token="$3" data="${4:-}" key="${5:-}"
    set -- -sS -o "$body" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $token"
    if [ -n "$data" ]; then
        set -- "$@" -H 'Content-Type: application/json' --data "$data"
    fi
    if [ -n "$key" ]; then
        set -- "$@" -H "Idempotency-Key: $key"
    fi
    status="$(curl "$@" "$url")" || fail "$method $url: curl failed"
}

expect() {
    [ "$status" = "$1" ] || fail "got HTTP $status; want $1"
}

text() {
    sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" "$body"
}

amount() {
    sed -n "s/.*\"$1\":{\"amount\":\"\([^\"]*\)\".*/\1/p" "$body"
}

literal() {
    sed -n "s/.*\"$1\":\([^,}]*\).*/\1/p" "$body"
}

check() {
    [ "$2" = "$3" ] || fail "$1 = '$2'; want '$3'"
}

wager() {
    extra=""
    if [ -n "${4:-}" ]; then
        extra=",\"referenceExternalTransactionId\":\"$4\""
    fi
    printf '{"providerId":"provider-a","externalTransactionId":"%s","playerId":"%s","walletId":"%s","roundId":"round-%s","gameId":"smoke","kind":"%s","money":{"amount":"%s","currency":"BRL"}%s}' \
        "$1" "$player" "$wallet" "$run" "$2" "$3" "$extra"
}

submit() {
    call POST "$1/wagering/transactions" "$provider" "$(wager "$2" "$3" "$4" "${5:-}")" "provider-a:$2"
}

settle() {
    tries=0
    while :; do
        call GET "$app1/providers/provider-a/wagering/transactions/$1" "$provider"
        if [ "$status" = 200 ] && [ "$(text status)" = "$2" ]; then
            return
        fi
        tries=$((tries + 1))
        [ "$tries" -lt 60 ] || fail "$1 did not reach $2 in 30s"
        sleep 0.5
    done
}

for base in "$app1" "$app2" "$app3"; do
    ready="$(curl -s -o /dev/null -w '%{http_code}' "$base/health/ready" || true)"
    [ "$ready" = 200 ] || fail "$base/health/ready = $ready; run 'docker compose up --build -d --wait' first"
done
provider="$(./scripts/get-token.sh provider-a)"
operator="$(./scripts/get-token.sh wallet-backoffice)"
[ -n "$provider" ] && [ -n "$operator" ] || fail "could not get tokens from Keycloak"

step "open a wallet with 100.00 on app-1"
call POST "$app1/wallets" "$operator" "{\"playerId\":\"$player\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}"
expect 201
wallet="$(text id)"
check "opening balance" "$(amount balance)" "100.00"
check "opening version" "$(literal version)" "1"

step "BET 25.00 on app-2"
submit "$app2" "bet-$run" BET 25.00
expect 200
bet="$(text transactionId)"
check "bet status" "$(text status)" "PROCESSED"
check "bet balance" "$(amount balance)" "75.00"
check "bet replay flag" "$(literal idempotentReplay)" "false"

step "replay the BET on app-3"
submit "$app3" "bet-$run" BET 25.00
expect 200
check "replayed transaction" "$(text transactionId)" "$bet"
check "replay balance" "$(amount balance)" "75.00"
check "replay flag" "$(literal idempotentReplay)" "true"

step "WIN 10.00 on app-1"
submit "$app1" "win-$run" WIN 10.00
expect 200
check "win balance" "$(amount balance)" "85.00"

step "ROLLBACK 5.00 before its BET arrives, on app-2"
submit "$app2" "rollback-$run" ROLLBACK 5.00 "late-bet-$run"
expect 202
check "rollback status" "$(text status)" "PENDING_REFERENCE"

step "the referenced BET 5.00 on app-3, then the rollback settles"
submit "$app3" "late-bet-$run" BET 5.00
expect 200
check "late bet balance" "$(amount balance)" "80.00"
settle "rollback-$run" PROCESSED
check "rollback balance" "$(amount balance)" "85.00"

step "BET 15.00 through SQS"
./scripts/send-wager.sh "$wallet" "$player" BET 15.00 "sqs-$run" >/dev/null 2>&1 || fail "sending the SQS message failed"
settle "sqs-$run" PROCESSED
check "SQS bet balance" "$(amount balance)" "70.00"

step "read the ledger"
call GET "$app2/wallets/$wallet/ledger?limit=50" "$operator"
expect 200
check "ledger entries" "$(grep -o '"walletVersion"' "$body" | wc -l | tr -d ' ')" "6"

step "reconcile"
call POST "$app3/wallets/$wallet/reconciliation" "$operator"
expect 200
check "consistent" "$(literal consistent)" "true"
check "continuous versions" "$(literal continuousVersions)" "true"
check "stored balance" "$(amount storedBalance)" "70.00"
check "posted balance" "$(amount postedBalance)" "70.00"
check "difference" "$(amount difference)" "0.00"
check "checked entries" "$(literal checkedEntries)" "6"

step "trial balance of the double-entry ledger"
call GET "$app1/ledger/trial-balance" "$operator"
expect 200
check "unbalanced or inconsistent currencies" "$(grep -o -e '"balanced":false' -e '"consistent":false' "$body" | wc -l | tr -d ' ')" "0"
check "BRL books" "$(grep -c '"currency":"BRL","accounts"' "$body")" "1"

step "read an event for the wallet from wallet-events.fifo"
: >"$body"
event="$(docker compose run --rm -T --no-deps --entrypoint sh -e WALLET="$wallet" \
    -e QUEUE="${SQS_EVENTS_QUEUE:-wallet-events.fifo}" aws-init -c '
    url=$(aws sqs get-queue-url --queue-name "$QUEUE" --query QueueUrl --output text) || exit 1
    for attempt in $(seq 1 20); do
        found=$(aws sqs receive-message --queue-url "$url" --max-number-of-messages 10 --wait-time-seconds 2 \
            --message-attribute-names All \
            --query "Messages[?contains(Body, '"'"'$WALLET'"'"')] | [0].[ReceiptHandle, MessageAttributes.eventType.StringValue]" \
            --output text) || exit 1
        if [ -n "$found" ] && [ "$found" != "None" ]; then
            aws sqs delete-message --queue-url "$url" --receipt-handle "$(printf "%s" "$found" | cut -f1)" || exit 1
            printf "%s" "$found" | cut -f2
            exit 0
        fi
    done
    exit 1' 2>/dev/null)" || fail "no event for wallet $wallet arrived on wallet-events.fifo"
case "$event" in
WagerTransactionProcessed | WagerTransactionPendingReference | WalletBalanceChanged) ;;
*) fail "event type '$event'; want one of the wallet's event types" ;;
esac
step "got $event"

step "ok: wallet $wallet"
