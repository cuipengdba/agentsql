# AgentSQL v0.4 B2 S5 column-level SELECT load observation

**Language:** [English](b2-column-auth-s5-20260925.md) | [Simplified Chinese](../../perf/b2-column-auth-s5-20260925.md)

Date: 2026-09-25

This report is short-window engineering evidence for S5, not a GA or release-by-default conclusion. The factory default of B2 is still off.

## environment and commands

- Docker Desktop 29.8.0, ~11.9 GB of available memory.
- PostgreSQL: PG16, PG18 true container built by `dbext/postgres/agentsql_binder/Dockerfile.test`.
- Go 1.25.14，CGO enabled，Windows named pipe `npipe:////./pipe/docker_engine`。
- Each major: 4 workers, 10 seconds; full column-level SELECT path includes binder, catalog Fpre/Fpost, read-only execution, mask, encode, audit callback, final fence callback, and seal.

```powershell
$env:AGENTSQL_B2_COLUMN_LOAD='1'
$env:AGENTSQL_B2_LOAD_SECONDS='10'
go test ./internal/authorizedexecute -run '^TestB2ColumnSelectProductionLoad$' -count=1 -v
```

## result

| PG | Complete request | Hesitation | p95 | p99 | 5.5ms increments to S0 P99 | catalog+binder p95 | error code | reservation trigger |
|---|---:|---:|---:|---:|---:|---:|---|---|
| 16 | 405 | 40.20/s | 112.01ms | 125.57ms | +120.07ms | 86.89ms | 0 | Yes |
| 18 | 432 | 42.97/s | 110.00ms | 122.92ms | +117.42ms | 86.13ms | 0 | Yes |

The end-to-end P99 of both majors is lower than the 250ms upper limit of this gate, and far lower than the 2s total lock wall upper limit; no authorization, database, frame, audit or reservation errors were observed. The reservation evidence is obtained by first occupying 4 slots of the same agent and asserting that the next application will return `AUTH_CONCURRENCY_LIMIT` stably.

The delta cannot be interpreted as pure SQL execution cost: binder, lock, catalog Fpre/Fpost are re-executed per request by safety design, so catalog+binder is the main overhead. This short window is not a substitute for a total attack acceptance of S7 or a long SLO/lock contention walkthrough of a real deployment of S8.
