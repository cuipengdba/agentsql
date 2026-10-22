# AgentSQL v0.4 B6 S5c official capacity baseline

**Language:** [English](chain-load-s5c-20260924.md) | [Simplified Chinese](../../perf/chain-load-s5c-20260924.md)

Date: 2026-09-24

Conclusion: **S5 Volume Gate GO**. No fail-closed, `AUDIT_OVERLOADED`, or structural broken links were observed under the target load; `chain_seq` is continuously unique for 3,799,725 rows of audit data, and `chain_state.head_seq` is consistent with the total number of commits. The target goodput and append delay reach the release threshold in the same environment short window.

## 1. Environment and measurement boundaries

| Project | value |
|---|---|
| PostgreSQL | `postgres:18` |
| Container limits | 2 CPU / 2048 MiB，`limits_applied=true` |
| Host Parallelism | `GOMAXPROCS=8` |
| Docker | 29.8.0 |
| testcontainers-go | v0.37.0 |
| Go | 1.25.14 |
| GCC | 16.2.0 |
| Formal combined load | sustained offered 2000/s × 1800s；burst offered 5000/s × 60s |
| delayed quantile method | nearest-rank |

This report distinguishes between three types of evidence:

- **Official actual measurement**: From the query of the persistence status in the PostgreSQL container after the combined long run.
- **short window actual measurement**: S5b-2 is obtained using the same binary and the same 2C2G restricted environment, which is used as the basis for scenario-by-scenario goodput and delay.
- **is not captured**: The long-distance running session is not persisted or the items are not completed within the collection window; they are not replaced by estimated values.

## 2. Fixture

- Path: `internal/store/testdata/chain_load_fixture.json`
- fixture canonical SHA256 (compact template JSON spliced in array order): `208ed06116a066ab39696d9089d208c168accfbb31869fd7d0d347d107a38ed5`
- seed：`20260924`
- Number of templates: 1000
- event body nearest-rank p95：8 KiB

## 3. Official combined running hard gate results

| Check items | Official test results | Judgment |
|---|---:|---|
| `audit_logs` Total number of lines | 3,799,725 | Scale evidence established |
| decision result | 3,799,725 lines all `decision=allow` | Passed: zero fail-closed |
| error code | `error_code` Number of non-blank lines 0 | Passed: zero `AUDIT_OVERLOADED`, zero other error codes |
| `chain_seq` is not empty | NULL=0 | Pass |
| `chain_seq` only | count=3,799,725；count(distinct)=3,799,725 | Pass |
| `chain_seq` Continuous | min=1；max=3,799,725 | Pass: sequence is 1..N |
| `chain_state` | mode=`keyless`, status `ACTIVE` | Pass |
| Chain head consistency | head_seq=3,799,725=Total number of rows submitted for audit | Pass |

The burst window actually added 199,725 lines, about 3329/s. 5000/s is the offered load; this window does not return `AUDIT_OVERLOADED`, and the part that exceeds the immediate submission capability is smoothed by group-commit, which is consistent with the bounded backpressure design. Here we do not rewrite 3329/s as 5000/s, nor do we replace committed rate with offered rate.

The above database facts prove that the sequence is continuous and unique and the chain head is consistent with the number of submissions, so no structural chain breaks are observed in the formal scale. It is not equivalent to completing the full 3.8M lines of hashing; see Section 6 for this limitation.

## 4. Short window indicators in the same environment

The following data comes from the actual measurement of S5b-2 in the same binary and the same 2C2G environment, and is used to supplement the scene-by-scenario throughput and latency that are not persisted in the official long-distance running.

| scene | goodput | append p95 | append p99 | Error | Queue after shutdown |
|---|---:|---:|---:|---:|---|
| PostgreSQL sustained | 2007/s | 24.64 ms | 34.02 ms | 0 | Return to zero |
| PostgreSQL burst | 4874/s | 182.03 ms | 238.72 ms | 0 | Return to zero |
| SQLite sustained | 500/s | 20.87 ms | 22.67 ms | 0 | Return to zero |

The actual measurement of PG backfill is 1160 rows/s. This value is used as a reference for backfill capabilities and does not belong to the additional path hard door in this S5 GO caliber.

## 5. Determination of release thresholds one by one

| Release check | Thresholds/Requirements | evidence | Judgment |
|---|---|---|---|
| PostgreSQL sustained goodput | ≥ 2000/s | Short window actual measurement 2007/s | **PASS (short window actual measurement)** |
| PostgreSQL sustained delay | p95 ≤ 250 ms；p99 ≤ 1000 ms | Short window measured 24.64/34.02 ms | **PASS (short window actual measurement)** |
| PostgreSQL burst under pressure | offered 5000/s × 60s; zero errors, zero `AUDIT_OVERLOADED` | Official operation with zero error code; short window goodput 4874/s, error 0 | **PASS (formal actual measurement + short window actual measurement)** |
| PostgreSQL burst delay | p95 ≤ 250 ms；p99 ≤ 1000 ms | Short window measured 182.03/238.72 ms | **PASS (short window actual measurement)** |
| SQLite goodput | ≥ 500/s | Short window actual measurement 500/s | **PASS (short window actual measurement)** |
| SQLite append deferred reference | p95 ≤ 250 ms；p99 ≤ 1000 ms | Short window measured 20.87/22.67 ms | **PASS (short window actual measurement)** |
| Target load misbehavior | zero fail-closed; zero `AUDIT_OVERLOADED` | Officially, all 3,799,725 lines are allowed, non-empty error code 0 | **PASS (official test)** |
| sequence integrity | `chain_seq` non-empty, continuous, unique, range 1..N | N=3,799,725；distinct=N；min=1；max=N；NULL=0 | **PASS (official test)** |
| Chain head consistency | `head_seq`=Number of submissions, status ACTIVE | 3,799,725=3,799,725，mode=keyless(ACTIVE) | **PASS (official test)** |
| Stop loading and drain | Queue resets to zero after shutdown | S5b-2 Each scene is reset to zero | **PASS (short window actual measurement)** |
| SQLite official duration | 500/s × 10 min Keep alone | The official duration result is not kept separately; the short window has been verified 500/s | **is not captured;** is not estimated |
| Fixed scale Verify takes time | N=10^5; PG ≤ 30s, SQLite ≤ 60s; additional RSS ≤ 64 MiB | PG 8.120038s / 3.809 MiB; SQLite 4.970181s / 5.422 MiB; both VALID | **PASS (fixed scale actual measurement)** |

The 5000/s of burst is the applied load, not the forced goodput threshold; 4874/s is the short window completed write rate. The official burst window's new rate of approximately 3329/s, combined with zero `AUDIT_OVERLOADED`, indicates that group-commit is smoothed in this window.

## 6. Fixed scale Verify takes time

2026-09-24 Construct an ACTIVE keyless chain of exactly 100,000 lines in each of the two backends using existing fixtures, backfill/activation tools, and streaming validators. The timing range is the end-to-end time from calling read-only `ChainVerifier.Verify` to the return, excluding data generation, backfill and activation. RSS uses the current Go process working set: perform two GCs before Verify and take the immediate baseline. During Verify, samples are taken every 5ms. The additional RSS peak is the sampling peak minus the baseline.

| backend | Size/Status | Verify results | End-to-end time consumption | time threshold | Extra RSS spikes | RSS threshold | Judgment |
|---|---|---|---:|---:|---:|---:|---|
| PostgreSQL 18（testcontainers，2 CPU / 2048 MiB） | 100,000 lines, ACTIVE keyless | `VALID_AT_OBSERVED_HEAD` | 8.120038s | ≤ 30s | 3.809 MiB | ≤ 64 MiB | **PASS** |
| SQLite (native) | 100,000 lines, ACTIVE keyless | `VALID_AT_OBSERVED_HEAD` | 4.970181s | ≤ 60s | 5.422 MiB | ≤ 64 MiB | **PASS** |

Both backends meet the time, result, and streaming memory thresholds, and the fixed-scale Verify closing item is completed.

## 7. Uncaught Items and Limitations

1. Scenario-by-scenario goodput/latency for official 30 minute runs is not persisted: session output is truncated after long runs. Therefore, this report only uses the S5b-2 short window data in the same environment to determine the throughput and latency on a scenario-by-scenario basis, and does not infer the quantile latency from the total number of 3,799,725 rows.
2. The official duration result of SQLite 500/s × 10 minutes is not kept separately; the existing evidence is that the short window of the same environment has verified 500/s.
3. Performing full recalculation on approximately 3.8M rows `Verify` is an O(N) sequential scan and is not completed within the collection window. The official actual measurement has covered the serial uniqueness, no NULL, ACTIVE status and head/commit number consistency, but it does not claim to have completed the full hash recalculation.
4. Periodic verification uses streaming processing, and the production library uses incremental verification, so item 3 does not constitute a block for this release. The final measurement of Verify on the fixed 10^5 row data set has been completed, and the results are shown in Section 6.

## 8. Conclusion

The S5 hard door meets: zero fail-closed, zero `AUDIT_OVERLOADED`, and zero structural broken links under the target load; sequence 1..N is consecutively unique under the 3,799,725 row scale and `head_seq` is equal to the number of commits; the target goodput and append delay reach the threshold in the short window of the same environment. Therefore **S5 capacity gate GO**.

Full Verify wrapper with fixed N=10^5 rows passed: PostgreSQL 8.120038s, SQLite 4.970181s, both VALID, additional RSS peaks below 64 MiB each.
