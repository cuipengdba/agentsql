# T25 gateway overhead baseline

**Language:** [English](README.md) | [Simplified Chinese](../../perf/README.md)

This catalog only defines repeatable measurement methods and does not record performance figures that have not been reproduced by actual measurements.

## Memory executor baseline

The following benchmark uses the memory fake executor to run the complete eight-stage AgentSQL process, including authentication, data source and policy loading, SQL parse, static/dynamic rules, controlled Query, result masking and auditing, but strips away the real database network and execution time:

```bash
go test ./internal/pipeline -run '^$' -bench BenchmarkPipelineReadOnlyParallel -benchtime=2s -count=5
```

The benchmark uses `b.RunParallel` and enables `ReportAllocs`. The report should record the Go version, CPU, OS, concurrency, `ns/op`, `B/op`, and `allocs/op` for each run. The acceptance criterion is that the additional overhead of the gateway P99 for the read-only path is less than 5ms/request. The final number is subject to the actual measured environment.

## Optional real PostgreSQL comparison

real database benchmark is skipped by default. Run after explicitly providing the standalone test library URL:

```bash
export AGENTSQL_BENCH_DSN='postgres://user:password@127.0.0.1:5432/agentsql_bench?sslmode=disable'
go test ./internal/pipeline -run '^$' -bench BenchmarkPipelineReadOnlyRealDatabase -benchtime=2s -count=5
```

The results include real database connection, EXPLAIN and query time, which are only used for comparison and are not equivalent to the pure additional overhead of the gateway.

## Results archive

After the actual measurement is completed, record the environment, original commands, five samples and P99 conclusions in `docs/perf/t25_bench_result.md`; do not pre-fill performance numbers before running through the real environment.

## B2 column level SELECT production observation gate

PG16/18 true container payload is skipped by default. Explicitly run:

```bash
AGENTSQL_B2_COLUMN_LOAD=1 AGENTSQL_B2_LOAD_SECONDS=10 \
go test ./internal/authorizedexecute -run TestB2ColumnSelectProductionLoad -count=1 -v
```

The output `B2_COLUMN_LOAD` JSON includes throughput, p95/p99, increment relative to S0 5.5ms P99, catalog+binder p95, stable error code distribution and reservation trigger proof. The gate requires that the error code is empty and the end-to-end P99 does not exceed 250ms; the delta must be reported as is and cannot be replaced by a threshold. Production Prometheus exposes both `agentsql_b2_state` and `agentsql_b2_phase_duration_seconds`.
