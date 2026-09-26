#!/bin/sh
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    --set migrator_password="$WALLET_MIGRATOR_PASSWORD" \
    --set app_password="$WALLET_APP_PASSWORD" <<'SQL'
CREATE ROLE wallet_migrator LOGIN PASSWORD :'migrator_password';
CREATE ROLE wallet_app LOGIN PASSWORD :'app_password';
ALTER ROLE wallet_app SET idle_in_transaction_session_timeout = '30s';
ALTER DATABASE :"DBNAME" OWNER TO wallet_migrator;
SQL
