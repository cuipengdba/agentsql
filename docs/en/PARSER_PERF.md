# PostgreSQL parser performance review (2026-10-05)

**Language:** [English](PARSER_PERF.md) | [Simplified Chinese](../PARSER_PERF.md)

## Caliber and recurrence

The release gate is still `TestParseProjectionLineageP99Budget/postgres`: 8 `UNION ALL` branches, 8 columns per branch, 2,000 complete `Parser.Parse` times in a single round, the latest rank P99 must be **≤ 5 ms**, 5 consecutive rounds passed. It measures complete parsing versus lineage, not database execution latency. `ns/op` of the standard Go benchmark is an average and cannot replace P99. The threshold and number of samples in the test source code have not been modified.

This time we use `golang:1.26-bookworm` (Go 1.26.8), Linux/amd64, WSL2 6.18.40.1, Intel i7-6700 (4 cores and 8 threads), `GOMAXPROCS=8`. The source code is mounted in read-only mode; no other builds or tests of this task were executed in parallel during the performance measurement. There is no proof that the host machine is completely free of other background loads.

Run in the root directory of the Linux repository:

```sh
sh scripts/parser-bench.sh gate
sh scripts/parser-bench.sh bench
sh scripts/parser-bench.sh profile
```

Reproduce with the same image and read-only source code on Docker Desktop:

```sh
docker run --rm --platform linux/amd64 -e GOMAXPROCS=8 -e GOTOOLCHAIN=local \
  -v "$PWD:/src:ro" -w /src golang:1.26-bookworm \
  sh scripts/parser-bench.sh gate
```

`scripts/parser-bench.sh all` sequentially runs parser short tests, gate, benchmark, and profile. The script exits immediately when the gate fails; the `profile` survey can still be run independently. profile file is written to the container's temporary directory, and the script prints the CPU and `alloc_space` summaries on stdout.

## Five round results

This time, the following two sets of results were measured directly on the existing code, in ms. The second group was run after adding benchmarks and boundary tests; the production parser logic was not changed.

| rounds | Initial baseline P50 | Initial baseline P99 | Review P50 | Review P99 | review gate |
| ---: | ---: | ---: | ---: | ---: | :---: |
| 1 | 0.625 | 1.924 | 0.618 | 1.917 | PASS |
| 2 | 0.542 | 1.881 | 0.576 | 2.075 | PASS |
| 3 | 0.546 | 1.869 | 0.557 | 1.994 | PASS |
| 4 | 0.567 | 1.934 | 0.579 | 2.011 | PASS |
| 5 | 0.574 | 1.958 | 0.551 | 1.911 | PASS |

The exit codes of both groups of commands are both 0, **5/5 PASS**. The 9.50 ms P99 reported by users was not replicated in this environment. The optimized P99 previously recorded by the repository under the same CPU model and Go image series was 4.456/4.775/6.404/6.554/4.173 ms, with only 3/5 PASS (`docs/release-notes-v0.5.md`). There are no production code performance changes at this time, so the difference between the two results cannot be attributed to new optimizations; the specific contributions of host load, scheduling, or GC have not been independently measured. Past failure records are still valid, and five rounds should be exclusively executed on the release day on a designated, fixed Linux runner with no extraneous load; if any round exceeds 5 ms, the release will be stopped according to the existing threshold.

## Scenarios, costs and profiles

`BenchmarkPostgresParseCorpus` covers SELECT, JOIN, WHERE with numeric and string literals, CTE, DDL, and 8×8 UNION SQL with release gates. The ranges of the three benchmarks are as follows; they are the average time taken and the amount allocated per time, not P99.

| scene | ns/op range | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| SELECT | 25,032–25,866 | 8,932 | 120 |
| JOIN | 59,079–71,154 | 23,300 | 280 |
| WHERE + literals | 92,126–92,936 | 26,364 | 328 |
| CTE | 75,803–79,436 | 27,814 | 337 |
| DDL | 26,870–32,515 | 10,831 | 144 |
| 8×8 UNION | 759,008–822,845 | 316,434–316,439 | 3,578 |

In the segmented benchmark, `pg_query.ParseToJSON` is 74–89 µs / 1 allocation for typical SQL, 367–392 µs / 2,888 allocations for generic JSON tree decoding, and 767–787 µs / 3,578 allocations for the full `Parser.Parse`. These segments cannot simply add up to sequential end-to-end latency, which still means that JSON decoding accounts for the majority of the visible cost.

`alloc_space` of the complete parsing profile in 5 seconds: `encoding/json.(*decodeState).objectInterface` flat accounts for 57.95%, Decoder refill accounts for 10.56%, `resolveQualifiedBinding` accounts for 4.86%, and `pg_query`'s GoString accounts for 3.68%. In the CPU profile, `runtime.cgocall` accounts for 8.63% and `runtime.futex` accounts for 8.75%. JSON decoder, map traversal, malloc and GC scan all have visible costs. The `cgocall` sample only illustrates CPU samples at call boundaries and cannot attribute all tail latencies to cgo; there is no single stable hotspot that is sufficient to prove that a small production code modification will reduce P99 for all runners.

## Semantics and Release Conclusions

There is no replacement of the PostgreSQL AST, caching of parsing results, or adjustment of the 5 ms threshold. `go test -short ./internal/parser -count=1` passed. A new test is added to verify that string literals exceeding 1 MiB will not enter `Normalized`, and semicolons in literals will not be misjudged as multi-statements, and true bi-statements will still return `ErrUnparseable`. `AST.RawSQL` The original SQL is still saved according to the existing contract, and the caller cannot use it as a masked log field. The original tests cover NULL/three-valued logic, comments and case, concurrent parsing, depth, and hard caps on the number of projections. parser The native gate PASS cannot alone indicate that the entire v0.5.0 release has been approved.

`go test -short ./internal/parser ./internal/rules ./internal/pipeline -count=1 -p 1` All three packages in the same Linux image have PASS and exit code 0. The first cold compilation/testing attempt of full warehouse `go test -short ./... -p 1` has a 10-minute upper limit; only the PASS of `cmd/agentsql` and `cmd/agentsqlctl` is obtained and ends in a non-zero status. The remaining packages have no results, so the short test status of full warehouse is **and incomplete**, which cannot be recorded as PASS. Full warehouse verification still needs to be completed on the CI runner with persistent Go cache before release.
