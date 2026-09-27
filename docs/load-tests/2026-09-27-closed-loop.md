# Load test

Environment:
- load generator host: linux/amd64, AMD Ryzen 5 5600G with Radeon Graphics, 12 logical CPUs, 15.6 GiB RAM
- loadgen built with go1.26.8; targets http://localhost:8081, http://localhost:8082, http://localhost:8083
- started 2026-09-27T14:33:05Z

Workload:
- closed loop with 64 requests in flight for 1m0s
- 50 wallets; 0% of new operations on one hot wallet; 5% of requests replay an earlier operation
- new operations: BET 60%, WIN 25%, LOSS 5%, REFUND 5%, ROLLBACK 5% (reversals of the worker's own processed operations, a BET when none is left)

| Requests | Elapsed | Throughput | Errors |
|---:|---:|---:|---:|
| 101664 | 60.1 s | 1692.1 req/s | 0 (0.00%) |

Latency in ms, measured by the client from the moment each request is sent:

| Operation | Requests | p50 | p95 | p99 | Max |
|---|---:|---:|---:|---:|---:|
| all | 101664 | 33.3 | 80.8 | 117.4 | 355.3 |
| BET | 58023 | 33.7 | 80.7 | 118.7 | 281.7 |
| WIN | 24176 | 33.8 | 82.2 | 117.2 | 355.3 |
| LOSS | 4879 | 30.0 | 77.9 | 112.5 | 210.8 |
| REFUND | 4784 | 35.7 | 82.8 | 119.6 | 247.6 |
| ROLLBACK | 4750 | 36.0 | 84.2 | 117.6 | 312.4 |
| REPLAY | 5052 | 23.3 | 69.1 | 106.3 | 220.0 |

| Outcome | Requests |
|---|---:|
| processed | 96612 |
| replayed | 5052 |

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
| Events published during the run and the drain | 22570 |
| Largest backlog seen | 181293 events |
| Oldest pending event, worst seen | 57.4 s |
| Commit to publish, p50 / p95 / p99 / max | 78.37 / 161.43 / 170.16 / 172.50 s over 22470 events |
| Drain after the load | 165875 events left after 2m0s |

Reconciliation: 50 of 50 wallets consistent.
