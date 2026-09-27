# Load test

Environment:
- load generator host: linux/amd64, AMD Ryzen 5 5600G with Radeon Graphics, 12 logical CPUs, 15.6 GiB RAM
- loadgen built with go1.26.8; targets http://localhost:8081, http://localhost:8082, http://localhost:8083
- started 2026-09-27T18:23:07Z

Workload:
- closed loop with 64 requests in flight for 1m0s
- 50 wallets; 0% of new operations on one hot wallet; 5% of requests replay an earlier operation
- new operations: BET 60%, WIN 25%, LOSS 5%, REFUND 5%, ROLLBACK 5% (reversals of the worker's own processed operations, a BET when none is left)

| Requests | Elapsed | Throughput | Errors |
|---:|---:|---:|---:|
| 72576 | 60.0 s | 1208.6 req/s | 0 (0.00%) |

Latency in ms, measured by the client from the moment each request is sent:

| Operation | Requests | p50 | p95 | p99 | Max |
|---|---:|---:|---:|---:|---:|
| all | 72576 | 46.7 | 112.4 | 162.0 | 502.6 |
| BET | 41225 | 47.5 | 112.5 | 162.2 | 403.8 |
| WIN | 17418 | 47.8 | 114.5 | 161.5 | 502.6 |
| LOSS | 3495 | 38.1 | 103.4 | 156.3 | 359.0 |
| REFUND | 3463 | 49.9 | 114.3 | 165.1 | 272.6 |
| ROLLBACK | 3454 | 50.1 | 120.0 | 175.7 | 357.5 |
| REPLAY | 3521 | 32.3 | 89.6 | 140.5 | 320.3 |

| Outcome | Requests |
|---|---:|
| processed | 69055 |
| replayed | 3521 |

Conflicts:

| Signal | Count |
|---|---:|
| Transactions rerun by `InTx` (serialization) | 0 |
| Transactions rerun by `InTx` (deadlock) | 0 |
| Transactions rerun by `InTx` (lock_timeout) | 0 |
| Transactions rerun by `InTx` (conflict) | 0 |
| HTTP 409 responses | 0 |
| Outbox claims lost to another instance | 0 |

Outbox:

| Measure | Value |
|---|---:|
| Events published during the run and the drain | 22640 |
| Largest backlog seen | 127299 events |
| Oldest pending event, worst seen | 56.6 s |
| Commit to publish, p50 / p95 / p99 / max | 76.57 / 158.79 / 167.70 / 170.01 s over 22586 events |
| Drain after the load | 112029 events left after 2m0s |

Reconciliation: 50 of 50 wallets consistent.
