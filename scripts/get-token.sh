#!/bin/sh
set -eu

client="${1:?usage: get-token.sh <client-id>}"
if [ -f .env ]; then
    set -a
    . ./.env
    set +a
fi
issuer="${OIDC_ISSUER:-http://localhost:8080/realms/wagering}"
var="$(printf '%s' "$client" | tr 'a-z-' 'A-Z_')_SECRET"
secret="$(printenv "$var" || true)"
if [ -z "$secret" ]; then
    echo "get-token: $var is not set (see .env.example)" >&2
    exit 1
fi

curl -fsS "$issuer/protocol/openid-connect/token" \
    -d grant_type=client_credentials \
    --data-urlencode "client_id=$client" \
    --data-urlencode "client_secret=$secret" |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
