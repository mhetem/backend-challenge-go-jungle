#!/bin/sh
set -eu

usage='usage: send-wager.sh <walletId> <playerId> [kind=BET] [amount=25.00] [externalTransactionId] [referenceExternalTransactionId]'
wallet="${1:?$usage}"
player="${2:?$usage}"
kind="${3:-BET}"
amount="${4:-25.00}"
external="${5:-tx-$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')}"
reference="${6:-}"
if [ -f .env ]; then
    set -a
    . ./.env
    set +a
fi
provider="${PROVIDER_ID:-provider-a}"
message_id="${MESSAGE_ID:-msg-$external}"

extra=""
if [ -n "$reference" ]; then
    extra=",\"referenceExternalTransactionId\":\"$reference\""
fi
body=$(printf '{"messageId":"%s","type":"WagerTransactionRequested","occurredAt":"%s","data":{"providerId":"%s","externalTransactionId":"%s","idempotencyKey":"%s","playerId":"%s","walletId":"%s","roundId":"%s","gameId":"%s","kind":"%s","money":{"amount":"%s","currency":"%s"}%s}}' \
    "$message_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$provider" "$external" "${IDEMPOTENCY_KEY:-$provider:$external}" \
    "$player" "$wallet" "${ROUND_ID:-round-1}" "${GAME_ID:-game-1}" "$kind" "$amount" "${CURRENCY:-BRL}" "$extra")
printf '%s\n' "$body" >&2

docker compose run --rm -T --no-deps --entrypoint sh \
    -e QUEUE="${SQS_INPUT_QUEUE:-wager-transactions.fifo}" -e BODY="$body" -e GROUP="$wallet" -e DEDUP="${DEDUP_ID:-$message_id}" \
    aws-init -c 'aws sqs send-message --queue-url "$(aws sqs get-queue-url --queue-name "$QUEUE" --query QueueUrl --output text)" \
        --message-body "$BODY" --message-group-id "$GROUP" --message-deduplication-id "$DEDUP"'
