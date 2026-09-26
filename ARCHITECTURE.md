# Arquitetura

## Money

Valores monetários são `int64` em minor units (centavos) mais um código ISO 4217. Só moedas
com duas casas decimais são aceitas: BRL, USD e EUR. O intervalo vai até
±92.233.720.368.547.758,07, muito além de qualquer wallet. Nenhum caminho usa `float`.

Contrato externo:
- O `amount` é sempre uma string JSON no formato `^(0|[1-9]\d*)\.\d{2}$`, e a `currency`
  é o código em maiúsculas: `{"amount":"25.00","currency":"BRL"}`.
- Os valores abaixo são rejeitados, nunca arredondados:
  - `25.00` como número JSON;
  - `"25"`, `"1.5"`, `"01.00"`, `"+1.00"` e `"1e2"`;
  - espaços;
  - valores negativos.
- Como o formato é estrito, cada valor tem uma única representação textual, e o hash do
  payload não precisa normalizar amounts.

Internamente:
- O parse acumula dígito a dígito com checagem de overflow. `strconv.ParseFloat` nunca é
  usado.
- `ParseSigned` aceita um `-` inicial para valores internos (por exemplo, a soma do ledger
  na reconciliação) e rejeita `-0.00`.
- `Add`, `Sub` e `Neg` retornam `ErrOverflow` em vez de dar a volta. Operações entre moedas
  diferentes retornam `ErrCurrencyMismatch`.
- O zero value de `Money` é inválido: todo método retorna `ErrUninitialized`, então um valor
  esquecido nunca vira `0.00` em silêncio.
- Erros de parse são `*ParseError{Input, Reason}`. O `Reason` é um dos sentinels
  (`ErrInvalidAmount`, `ErrInvalidCurrency`, `ErrOverflow`, `ErrInvalidJSON`) e funciona com
  `errors.Is` e `errors.As`.

## Acesso ao banco e mapeamento de Money

## Fronteira da transação

## Idempotência

## Locking e concorrência

## Máquina de estados

## Falhas transitórias vs permanentes

## Referências pendentes

## Política de reversão

## Códigos de falha

## Inbox e outbox

## Contratos SQS

## Contratos e roteamento de eventos

## Identity provider e validação de token

## Modelo de permissões

## Controle de acesso ao broker

## Emulador AWS local

SQS e IAM locais rodam no [MiniStack](https://github.com/ministackorg/ministack), fixado em
`ministackorg/ministack:1.5.17`. Ele é gratuito e sobe a partir de um clone limpo, sem conta
nem token.

Três candidatos foram testados com a AWS CLI em 2026-09-26:

| | LocalStack 2026.8.4 | LocalStack 4.14.0 | MiniStack 1.5.17 |
|---|---|---|---|
| Sobe sem conta ou auth token | Não, sai com código 55 (license activation failed) | Sim | Sim |
| Filas FIFO, `MessageDeduplicationId`, redrive para DLQ FIFO | Não testável sem token | Sim | Sim |
| Policies IAM aplicadas | Recurso pago | Não, `ENFORCE_IAM=1` é ignorado | Não |
| `SenderId` distingue credenciais | Não testável sem token | Não, `000000000000` para toda chave | Não, `000000000000` para toda chave |

O LocalStack 4.14.0 é a última imagem que roda sem token. O repositório da edição community
foi arquivado em 2026-03-23, então ele não recebe mais correções. O MiniStack tem licença MIT
e continua recebendo releases.

Nos dois candidatos que subiram, os comportamentos do SQS dos quais o consumer depende
bateram com a AWS:
- Um envio sem deduplication id é rejeitado quando `ContentBasedDeduplication=false`.
- Um deduplication id repetido devolve o `MessageId` original e a mensagem é entregue uma
  única vez.
- Enquanto um message group tem uma mensagem in flight, nenhuma outra mensagem desse grupo é
  entregue.
- `ChangeMessageVisibility=0` libera a mensagem na hora e incrementa `ApproximateReceiveCount`.
- Depois de `maxReceiveCount` recebimentos, a mensagem vai para a DLQ FIFO.

## Módulos Fx e sequência de shutdown

## Observabilidade

## Limitações, interpretações e trabalho pendente

- O IAM não é aplicado localmente. Os usuários e policies por papel continuam sendo
  provisionados, e só são aplicados na AWS real.
- Localmente, o `SenderId` é o account id para qualquer credencial, então o consumer não o usa
  para identificar o provider. O provider do envelope é validado contra o banco.
