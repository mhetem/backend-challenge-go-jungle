# Serviço de processamento de wagers

Processamento de wallets e wagers em Go: ledger append-only no PostgreSQL, ingestão síncrona
via HTTP e via SQS FIFO, inbox e outbox transacionais, e tokens de serviço emitidos pelo
Keycloak. Toda instância executa todos os componentes, e a corretude não depende de quantas
instâncias estão rodando.

As decisões técnicas, os contratos e as limitações estão em [ARCHITECTURE.md](ARCHITECTURE.md).

## Pré-requisitos

- Docker Engine com Docker Compose v2 (desenvolvido com o Compose 5.4).
- `make`, `curl` e um shell POSIX.
- Para rodar fora do Docker e para os testes:
  - Go 1.26.8, fixado em `go.mod`. Com um Go mais antigo instalado, `GOTOOLCHAIN=auto` (o
    padrão) baixa a versão certa;
  - um compilador C (`gcc`), exigido pelo `-race`.
- Opcional, só para `make check` e `make generate`:
  ```sh
  go install honnef.co/go/tools/cmd/staticcheck@latest
  go install golang.org/x/vuln/cmd/govulncheck@latest
  go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
  ```
- Portas livres no host: 8080 (Keycloak), 4566 (MiniStack), 55432 (PostgreSQL),
  8081–8083 e 9091–9093 (as três instâncias), 8084 e 9094 (`make run`).

## Início rápido

```sh
cp .env.example .env
docker compose up --build -d --wait     # ou: make up
./scripts/smoke.sh                       # ou: make smoke
```

O compose sobe PostgreSQL, Keycloak (com o realm importado), MiniStack (SQS e IAM), cria as
filas (`aws-init`), aplica as migrations (`migrate`) e só então inicia `app-1`, `app-2` e
`app-3`. `--wait` retorna quando todos estão saudáveis.

`scripts/smoke.sh` percorre o fluxo inteiro espalhado pelas três instâncias: abre uma
carteira, faz `BET`, replay, `WIN`, um `ROLLBACK` que chega antes da sua `BET`, uma `BET`
por SQS, lê o ledger, reconcilia e lê um evento de `wallet-events.fifo`. Sai com erro em
qualquer divergência.

Para derrubar tudo e apagar os volumes: `make down` (`docker compose down -v`).

## Configuração

### `.env`

`.env.example` traz só valores locais de desenvolvimento. O compose, o `make`, os scripts e
os testes leem o `.env`; cada senha existe em um único lugar e as URLs são montadas a partir
dela.

| Variável | Valor de exemplo | Uso |
|---|---|---|
| `POSTGRES_USER`, `POSTGRES_PASSWORD` | `postgres` | Superusuário do container. Só cria os papéis e, nos testes, os bancos por teste |
| `POSTGRES_DB` | `wallet` | Banco de desenvolvimento |
| `POSTGRES_PORT` | `55432` | Porta do PostgreSQL no host |
| `WALLET_MIGRATOR_PASSWORD` | `wallet_migrator` | Papel dono do schema, usado pelas migrations |
| `WALLET_APP_PASSWORD` | `wallet_app` | Papel da aplicação, só DML |
| `ADMIN_DATABASE_URL` | superusuário em `localhost:55432` | Testes: criar e apagar bancos por teste |
| `MIGRATE_DATABASE_URL` | `wallet_migrator` em `localhost:55432` | `cmd/migrate` e testes |
| `DATABASE_URL` | `wallet_app` em `localhost:55432` | Aplicação rodando no host |
| `KC_BOOTSTRAP_ADMIN_USERNAME`, `KC_BOOTSTRAP_ADMIN_PASSWORD` | `admin` | Console do Keycloak em http://localhost:8080 |
| `AWS_REGION` | `us-east-1` | Região do SDK |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | `local` | Credenciais do emulador. Não use 12 dígitos: o MiniStack trata isso como outra conta |
| `AWS_ENDPOINT_URL` | `http://localhost:4566` | Emulador visto do host (os containers usam `http://aws:4566`) |
| `HTTP_ADDR`, `ADMIN_ADDR` | `:8084`, `:9094` | Portas do `make run`, livres das instâncias do compose |
| `PROVIDER_A_SECRET`, `PROVIDER_B_SECRET`, `WALLET_BACKOFFICE_SECRET`, `PROVIDER_A_SHORTLIVED_SECRET`, `NO_ROLE_CLIENT_SECRET`, `NO_AUDIENCE_CLIENT_SECRET` | `<client>-local-secret` | Segredos dos clients do Keycloak; o realm os lê na importação |

Cuidados:
- As senhas do PostgreSQL só valem num volume novo, porque a imagem só roda o `initdb` com
  o volume vazio. Depois de mudar senhas no `.env`, rode `make down` (apaga o volume) e
  suba de novo; senão todo login falha com `28P01`.
- Mantenha as senhas sem `$`, `@`, `:` e `/`, porque elas entram em URLs.
- O Keycloak importa o realm só se ele ainda não existe. Depois de mudar segredos ou o
  arquivo do realm, rode `docker compose up -d --force-recreate keycloak`.
- A porta 55432 evita a 5432 de um PostgreSQL nativo, que capturaria as conexões e
  apareceria como falha de senha.

### Variáveis do serviço

Todas são opcionais, exceto `DATABASE_URL` e `AWS_REGION`. Valores inválidos encerram o
processo na inicialização, com todos os problemas listados.

| Variável | Padrão | Uso |
|---|---|---|
| `INSTANCE_ID` | hostname | Identifica a instância em logs, `application_name` e donos de claim da outbox |
| `COMPONENTS` | `http,consumer,outbox,resolver` | Componentes que a instância executa |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error` |
| `HTTP_ADDR` | `:8080` | Servidor público (API e `/health/*`) |
| `ADMIN_ADDR` | `:9090` | Servidor admin (`/metrics` e `/health/*`) |
| `START_TIMEOUT` | `30s` | Prazo para conectar ao banco e resolver as filas |
| `SHUTDOWN_TIMEOUT` | `20s` | Prazo do shutdown; tem de ser menor que `SQS_VISIBILITY_TIMEOUT` |
| `DATABASE_URL` | obrigatória | PostgreSQL com o papel `wallet_app` |
| `DB_MAX_CONNS` | `16` | Tamanho máximo do pool |
| `DB_LOCK_TIMEOUT` | `2s` | `SET LOCAL lock_timeout` de cada transação |
| `DB_STATEMENT_TIMEOUT` | `5s` | `SET LOCAL statement_timeout` de cada transação |
| `DB_TX_ATTEMPTS` | `3` | Execuções de uma transação em conflito de concorrência |
| `DB_TX_BACKOFF` | `20ms` | Base do backoff entre essas execuções |
| `AWS_REGION` | obrigatória | Região do SQS |
| `AWS_ENDPOINT_URL` | vazio (AWS real) | Endpoint do emulador |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | cadeia padrão do SDK | Credenciais |
| `SQS_INPUT_QUEUE` | `wager-transactions.fifo` | Fila de entrada |
| `SQS_INPUT_DLQ` | `wager-transactions-dlq.fifo` | DLQ da entrada, usada pelo consumer |
| `SQS_EVENTS_QUEUE` | `wallet-events.fifo` | Destino dos eventos |
| `SQS_VISIBILITY_TIMEOUT` | `30s` | Visibilidade pedida em cada receive (segundos inteiros) |
| `CONSUMER_WORKERS` | `4` | Receivers por instância |
| `CONSUMER_WAIT_TIME` | `20s` | Long polling, de 1 s a 20 s |
| `CONSUMER_MESSAGE_TIMEOUT` | `10s` | Prazo de uma mensagem; menor que `SHUTDOWN_TIMEOUT` |
| `OUTBOX_POLL_INTERVAL` | `500ms` | Intervalo do publisher |
| `OUTBOX_BATCH_SIZE` | `50` | Eventos por claim |
| `OUTBOX_LEASE` | `30s` | Duração do claim |
| `OUTBOX_BACKOFF_BASE`, `OUTBOX_BACKOFF_CAP` | `1s`, `5m` | Backoff de eventos que falharam |
| `RESOLVER_POLL_INTERVAL` | `1s` | Intervalo do resolver de referências pendentes |
| `RESOLVER_BATCH_SIZE` | `100` | Transações por ciclo |
| `RESOLVER_MAX_FAILURES` | `3` | Erros permanentes seguidos até `FAILED` |
| `PENDING_REF_TTL` | `15m` | Prazo para a referência aparecer |
| `PENDING_REF_MAX_ATTEMPTS` | `20` | Tentativas antes de rejeitar |
| `PENDING_REF_BACKOFF_BASE`, `PENDING_REF_BACKOFF_CAP` | `1s`, `1m` | Backoff entre tentativas |
| `OIDC_ISSUER` | `http://localhost:8080/realms/wagering` | Valor exato de `iss` |
| `OIDC_JWKS_URL` | issuer + `/protocol/openid-connect/certs` | De onde vêm as chaves (no compose, `http://keycloak:8080/...`) |
| `OIDC_AUDIENCE` | `wagering-api` | Audiência exigida |

Só nos testes: `FAILPOINTS` (binário com a tag `failpoints`, ver
[Simulação de falhas](#simulação-de-falhas)) e `GORACE`, que a suíte e2e define.

## Execução local

### Três instâncias (compose)

```sh
make up                          # docker compose up --build -d --wait
docker compose ps
docker compose logs -f app-2
```

| Instância | API | Admin (`/metrics`, `/health/*`) |
|---|---|---|
| `app-1` | http://localhost:8081 | http://localhost:9091 |
| `app-2` | http://localhost:8082 | http://localhost:9092 |
| `app-3` | http://localhost:8083 | http://localhost:9093 |

As três compartilham banco e filas e rodam todos os componentes. `docker compose up --build`,
sem `-d`, faz o mesmo em primeiro plano.

### Uma instância no host

```sh
cp .env.example .env
make deps          # postgres, keycloak e aws, depois o provisionamento das filas
make migrate-up
make run           # go run ./cmd/wallet: API em :8084, admin em :9094
```

`COMPONENTS` escolhe o que a instância executa, por exemplo `COMPONENTS=http,resolver make run`.
A instância do host pode rodar junto com as do compose: todas compartilham banco e filas.

## Provisionamento das filas

`make deps` sobe as dependências e roda o one-shot `aws-init`
([deploy/aws/provision.sh](deploy/aws/provision.sh)); `make up` o roda antes das
instâncias. O script é idempotente e pode ser repetido com `docker compose run --rm aws-init`.

| Fila | Configuração |
|---|---|
| `wager-transactions.fifo` | Entrada. Long polling de 20 s, visibilidade de 30 s, redrive para a DLQ depois de 10 recebimentos |
| `wager-transactions-dlq.fifo` | DLQ da entrada, retenção de 14 dias |
| `wallet-events.fifo` | Eventos de saída, redrive depois de 10 recebimentos |
| `wallet-events-dlq.fifo` | DLQ dos eventos, retenção de 14 dias |

Todas são FIFO com `ContentBasedDeduplication=false`. O script também cria os usuários IAM
`provider-producer`, `wallet-service` e `events-reader`, cada um com uma policy restrita. O
MiniStack não aplica IAM; na AWS as mesmas policies valem.

## Migrations

As migrations ficam em [migrations/](migrations/), embutidas no binário `migrate`, e usam o
papel `wallet_migrator` (`MIGRATE_DATABASE_URL`).

```sh
make migrate-up        # aplica as pendentes
make migrate-status    # lista aplicadas e pendentes
make migrate-down      # reverte a última
make migrate-reset     # reverte todas
```

Cada alvo roda `go run ./cmd/migrate <comando>`. No compose, o one-shot `migrate` roda `up`
antes das instâncias. A migration `00002` cadastra os providers locais `provider-a` e
`provider-b`.

## Autenticação

O realm `wagering` é importado de
[deploy/keycloak/realm-wagering.json](deploy/keycloak/realm-wagering.json) quando o Keycloak
sobe. Todos os clients são confidenciais e usam `client_credentials`:

| Client | Papel | Pode |
|---|---|---|
| `provider-a` | `wager-provider`, `provider_id=provider-a` | Enviar wagers de `provider-a` e ler as próprias transações |
| `provider-b` | `wager-provider`, `provider_id=provider-b` | O mesmo, como `provider-b` |
| `wallet-backoffice` | `wallet-operator` | Abrir, ler e reconciliar carteiras; ler qualquer transação |
| `provider-a-shortlived` | `wager-provider` | Como `provider-a`, com tokens de 5 s (teste de expiração) |
| `no-role-client` | nenhum | Nada (teste de token sem papel) |
| `no-audience-client` | `wager-provider` | Nada: o token não tem a audiência `wagering-api` |

```sh
OPERATOR=$(./scripts/get-token.sh wallet-backoffice)
PROVIDER=$(./scripts/get-token.sh provider-a)
```

Os tokens duram 5 minutos. O script lê o segredo do `.env` e imprime só o access token.

## API

Toda rota exige `Authorization: Bearer <token>`, exceto `/health/live` e `/health/ready`.
Os exemplos usam `app-1`; as três instâncias respondem da mesma forma. Os códigos HTTP e os
corpos de erro de cada situação estão em
[ARCHITECTURE.md › Contrato HTTP](ARCHITECTURE.md#contrato-http).

Abrir uma carteira (`wallet-backoffice`):

```sh
PLAYER=0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1
curl -s -X POST http://localhost:8081/wallets \
  -H "Authorization: Bearer $OPERATOR" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"
```

```json
{"id":"0192f291-27dd-7d3f-8071-5f8685deef37","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","balance":{"amount":"1000.00","currency":"BRL"},"version":1,"createdAt":"…","updatedAt":"…"}
```

Uma segunda carteira para o mesmo jogador e moeda responde 409 `WALLET_ALREADY_EXISTS`, com
`Location` da existente.

```sh
WALLET=0192f291-27dd-7d3f-8071-5f8685deef37   # o id devolvido acima
```

Enviar uma aposta (`provider-a`). `Idempotency-Key` é obrigatório:

```sh
curl -s -X POST http://localhost:8081/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"transaction-123\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-987\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
```

```json
{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d","externalTransactionId":"transaction-123","status":"PROCESSED","balance":{"amount":"975.00","currency":"BRL"},"walletVersion":2,"idempotentReplay":false}
```

- O mesmo comando, repetido em qualquer instância, devolve o mesmo resultado com
  `"idempotentReplay":true`, mesmo depois de outras movimentações.
- A mesma chave com outro conteúdo responde 409 `IDEMPOTENCY_KEY_REUSED`. O mesmo
  `externalTransactionId` com outra chave responde 409 `EXTERNAL_TRANSACTION_ID_CONFLICT`.
- `WIN`, `LOSS` (valor `"0.00"`), `REFUND` e `ROLLBACK` usam o mesmo corpo. Reversões
  acrescentam `"referenceExternalTransactionId":"transaction-123"`.
- Uma reversão cuja referência ainda não chegou responde 202, com `Location` e
  `"status":"PENDING_REFERENCE"`. O resolver a conclui quando a referência chega.
- Uma rejeição de negócio responde 422 com `"status":"REJECTED"` e o `failureCode`.

Consultar transações:

```sh
curl -s http://localhost:8081/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $PROVIDER"
curl -s http://localhost:8081/wagering/transactions/0192f298-345e-7e38-af88-e43f851a819d -H "Authorization: Bearer $PROVIDER"
```

A resposta traz o estado completo, inclusive `attempts`, `nextAttemptAt` e
`referenceDeadlineAt` de uma pendência. Outro provider recebe 403 (por id externo) ou 404
(por id), sem saber se a transação existe.

Carteira, ledger e reconciliação (`wallet-backoffice`):

```sh
curl -s http://localhost:8081/wallets/$WALLET -H "Authorization: Bearer $OPERATOR"
curl -s "http://localhost:8081/wallets/$WALLET/ledger?limit=50" -H "Authorization: Bearer $OPERATOR"
curl -s "http://localhost:8081/wallets/$WALLET/ledger?limit=50&cursor=<nextCursor>" -H "Authorization: Bearer $OPERATOR"
curl -s -X POST http://localhost:8081/wallets/$WALLET/reconciliation -H "Authorization: Bearer $OPERATOR"
```

```json
{"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37","storedBalance":{"amount":"975.00","currency":"BRL"},"calculatedBalance":{"amount":"975.00","currency":"BRL"},"difference":{"amount":"0.00","currency":"BRL"},"consistent":true,"continuousVersions":true,"checkedEntries":2}
```

Health checks, públicos:

```sh
curl -s http://localhost:8081/health/live
curl -s http://localhost:8081/health/ready    # 503 se o banco ou o SQS não respondem, ou durante o shutdown
```

## Envio de wager via SQS

```sh
./scripts/send-wager.sh "$WALLET" "$PLAYER" BET 25.00 transaction-124
./scripts/send-wager.sh "$WALLET" "$PLAYER" ROLLBACK 25.00 transaction-125 transaction-124
```

Argumentos: `<walletId> <playerId> [kind=BET] [amount=25.00] [externalTransactionId]
[referenceExternalTransactionId]`. O script roda a AWS CLI dentro do serviço `aws-init` do
compose, então o host não precisa dela. Ele envia este envelope com
`MessageGroupId=walletId` e `MessageDeduplicationId=messageId`:

```json
{
  "messageId": "msg-transaction-124",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-27T12:00:00Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-124",
    "idempotencyKey": "provider-a:transaction-124",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-1",
    "gameId": "game-1",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}
```

`MESSAGE_ID`, `DEDUP_ID`, `IDEMPOTENCY_KEY`, `PROVIDER_ID`, `CURRENCY`, `ROUND_ID` e
`GAME_ID` sobrescrevem os padrões. Por exemplo, `MESSAGE_ID=msg-1 DEDUP_ID=outro` reenvia a
mesma mensagem com outro deduplication id: ela chega ao consumer e a inbox a descarta.

O resultado aparece em `GET /providers/provider-a/wagering/transactions/transaction-124`.
Para olhar os eventos publicados ou a DLQ:

```sh
docker compose run --rm --no-deps --entrypoint sh aws-init -c \
  'aws sqs receive-message --max-number-of-messages 10 --message-attribute-names All \
     --queue-url "$(aws sqs get-queue-url --queue-name wallet-events.fifo --query QueueUrl --output text)"'
```

Troque `wallet-events.fifo` por `wager-transactions-dlq.fifo` para ver mensagens recusadas,
com o motivo no atributo `failureReason`.

## Observabilidade

- Logs JSON em stderr: `docker compose logs -f app-1`. Cada linha traz `instanceId` e, quando
  existem, `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`.
- Métricas Prometheus na porta admin: `curl -s http://localhost:9091/metrics | grep ^wallet_`.
  A lista completa está em [ARCHITECTURE.md › Observabilidade](ARCHITECTURE.md#observabilidade).
- `X-Correlation-Id` enviado numa requisição volta na resposta e aparece nos logs e na
  transação gravada.

## Testes

Comandos pedidos no desafio, sem dependências externas:

```sh
go test ./...
go test -race ./...
go vet ./...
```

Os testes de integração e e2e usam build tags e containers reais:

| Nível | Tag | Comando | Precisa de |
|---|---|---|---|
| Unitário | nenhuma | `make test`, `make test-race` | nada |
| Integração | `integration` | `make test-integration` | `make deps` |
| Múltiplas instâncias e falhas | `e2e`, `failpoints` | `make test-e2e` | `make deps` |
| Smoke do compose | — | `make smoke` | `make up` |
| Tudo que o CI roda (menos e2e e smoke) | — | `make check` | ferramentas opcionais dos pré-requisitos |

`go vet -tags integration ./...` e `go vet -tags e2e,failpoints ./...` cobrem o código com tags.

### Preparar as dependências

```sh
cp .env.example .env
make deps
```

Os testes leem as variáveis do `.env` pelo ambiente. O `make` já as exporta; para rodar
`go test` direto, exporte antes:

```sh
set -a; . ./.env; set +a
go test -race -tags integration -count=1 ./test/integration/consumer/...
```

Nenhum teste usa o banco de desenvolvimento: cada teste cria o seu banco (`test_<uuid>`),
aplica as migrations e o apaga no fim. Os testes que tocam SQS criam as próprias filas.

### Integração

`make test-integration` roda `go test -race -tags integration -count=1 -timeout 15m ./...`
contra PostgreSQL, Keycloak e MiniStack reais: schema e invariantes, repositórios, casos de
uso concorrentes, HTTP com tokens reais, autenticação, resolver, outbox, consumer (inbox,
reentrega, DLQ, redrive, shutdown) e o ciclo de vida do Fx com `goleak`.

### Múltiplas instâncias

`make test-e2e` roda
`go test -race -tags e2e,failpoints -count=1 -timeout 20m ./test/e2e/... ./internal/platform/failpoint/...`.
A suíte compila o serviço com `-race -tags failpoints` e sobe processos independentes (três
na maioria dos cenários), sobre um banco e filas criados para cada cenário. Leva alguns
minutos. Os
cenários estão em
[ARCHITECTURE.md › Múltiplas instâncias](ARCHITECTURE.md#múltiplas-instâncias-e-injeção-de-falhas).

Dois cenários congelam `postgres` e `aws` com `docker compose pause`, então não rode a
suíte e2e junto com a de integração. O teste sempre despausa ao terminar.

### Simulação de falhas

Pontos de falha, só num binário compilado com a tag `failpoints`:

```sh
go build -tags failpoints -o bin/wallet-fp ./cmd/wallet
set -a; . ./.env; set +a
FAILPOINTS=consumer.after_commit=exit HTTP_ADDR=:8085 ADMIN_ADDR=:9095 ./bin/wallet-fp
```

O processo sai com código 137 no ponto indicado, como num `SIGKILL`. Pontos disponíveis:
`consumer.after_commit`, `outbox.after_claim`, `outbox.after_publish`,
`resolver.after_reschedule` e `usecase.after_pending_reference_commit` (vários separados por
vírgula).

Com o compose:

```sh
docker compose kill app-2                                  # queda abrupta
docker compose stop app-2                                  # SIGTERM e shutdown gracioso
docker compose start app-2
docker compose pause postgres && sleep 15 && docker compose unpause postgres
docker compose pause aws && sleep 15 && docker compose unpause aws
```

Durante a pausa do banco, a readiness responde 503, as requisições de negócio respondem 503
e os consumers param de receber (`wallet_consumer_paused` 1). Depois da pausa nada se perde,
e `POST /wallets/{id}/reconciliation` confirma o saldo.

## Estrutura

```
cmd/wallet             serviço (fx.New(...).Run()) e subcomando healthcheck
cmd/migrate            migrations: up, down, status, reset
internal/domain        Money, Wallet, WagerTransaction, ledger, regras e eventos (só stdlib)
internal/app           casos de uso, portas, comandos e hash do payload
internal/adapters      postgres (pgx + sqlc), sqs, httpapi, oidc
internal/workers       consumer, outbox, resolver
internal/platform      config, logging, metrics, health, lifecycle, failpoint
internal/bootstrap     composição Fx
migrations             SQL do goose
deploy                 papéis do PostgreSQL, realm do Keycloak, provisionamento das filas
scripts                get-token.sh, send-wager.sh, smoke.sh
test/integration       testes com a tag integration
test/e2e               testes com várias instâncias e failpoints
```
