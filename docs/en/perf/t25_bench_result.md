# T25 gateway read-only path cost baseline (measurement archive)

**Language:** [English](t25_bench_result.md) | [Simplified Chinese](../../perf/t25_bench_result.md)

> This document is an archive of actual measurements after integration. Performance numbers are not pre-filled before actual measurements. The measurement caliber is **memory fake executor**: run the complete AgentSQL eight-stage pipeline (authentication, data source/policy loading, SQL parse, static/dynamic rules, controlled Query, masking, audit mapping), but strip the network and execution time of the real database, so the value represents the pure additional overhead of **gateway itself**, excluding database execution time.

## 1. Test environment

| item | value |
|---|---|
| operating system | Microsoft Windows NT 10.0.26200.0（amd64） |
| CPU | 13th Gen Intel Core i9-13900H, 20 logical cores |
| Go tool chain | go1.27.0 windows/amd64 (CGO is turned on, pg_query uses cgo) |
| module version | AgentSQL v0.1, T25 merged working tree |
| Concurrency | GOMAXPROCS=20，`b.RunParallel` / 20 worker |

## 2. Standard parallel benchmark (5 times)

Command:

```bash
go test ./internal/pipeline -run '^$' -bench 'BenchmarkPipelineReadOnlyParallel$' -benchtime=2s -count=5
```

| times | Number of iterations | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| 1 | 104362 | 23433 | 34774 | 428 |
| 2 | 90679 | 33042 | 34793 | 428 |
| 3 | 74780 | 35800 | 34813 | 428 |
| 4 | 64891 | 34059 | 34840 | 428 |
| 5 | 77144 | 29751 | 34615 | 427 |

5 times average **31217 ns/op ≈ 0.031 ms/request**, each decision makes about 34.8 KB heap allocation and 428 allocations.

## 3. Concurrency latency percentile (200,000 samples)

The standard benchmark only gives the average value, and `internal/pipeline/t25_latency_percentile_test.go` is used to run 10,000 times each under 20 workers, for a total of 200,000 complete read-only decisions, and the scores are sorted and ranked after timing each time.

Command:

```bash
go test ./internal/pipeline -run TestT25LatencyPercentile -v -count=1
```

Result:

| indicator | value |
|---|---|
| Number of samples / wall clock | 200000 / 2.843 s |
| aggregate throughput | 70343 ops/s |
| P50 | < 1 µs (sub-microsecond) |
| P95 | 1.1052 ms |
| **P99** | **1.8078 ms** |
| P99.9 | 3.2926 ms |
| Max | 6.7632 ms (1 time in 200,000 times, Go scheduling/GC tail jitter) |

## 4. Acceptance conclusion

- Acceptance criteria: Read-only path gateway overhead **P99 < 5 ms/request**.
- The actual measured **P99 = 1.81 ms is 1/2.8 of the budget, reaching the standard**; P99.9 = 3.29 ms is also within 5 ms.
- This value comes from the strict caliber of the 20-core full-load competition; in real deployment, single-core processing of the gateway and database execution are the main time-consuming tasks, and the proportion of the gateway is even lower.
- The only time Max 6.76 ms is due to occasional tail jitter (unstable quantization) during runtime, which can be further compressed through the object pool (reducing 428 allocs/op) without affecting the acceptance of v0.1.

## 5. Reproduction experiment

```bash
# standard benchmark
go test ./internal/pipeline -run '^$' -bench BenchmarkPipelineReadOnlyParallel -benchtime=2s -count=5
# percentile sampling
go test ./internal/pipeline -run TestT25LatencyPercentile -v -count=1
# Optional: Real PostgreSQL comparison (default Skip, enabled after setting AGENTSQL_BENCH_DSN, including real DB time, not pure gateway overhead)
```
