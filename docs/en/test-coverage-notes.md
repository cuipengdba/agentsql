# v0.5 test coverage and offline restrictions (released)

**Language:** [English](test-coverage-notes.md) | [Simplified Chinese](../test-coverage-notes.md)

This article records the **source code coverage of batch 67 new boundary/error path test**, and does not write the existence of test files or this document verification as test passing, real database passing or release gate passing. For the implementation requirements on the release date, see [ release date list ](release-day-checklist-v0.5.md).

## New scope of batch sixty-seven

| Test source code | Encoded assertion | not proven by this |
| --- | --- | --- |
| `internal/parser/boundary_error_paths_test.go` | Empty strings, blanks, unclosed strings, incomplete `ORDER BY` and stacked statements of MySQL, DM and SQL Server return `ErrUnparseable`; the dialect/original text of non-empty AST is retained, repeated parsing error text is stable, and the same parser can process valid SELECT after failure; invalid UTF-8, unclosed comments, and overlong SQL of DM/SQL Server are rejected; concurrent malformed input maintains dialect isolation. | Full Oracle/YashanDB syntax, real database execution, standard Yashan `NewParser` routing, and all possible malformed input. |
| `internal/pipeline/boundary_error_paths_test.go` | Empty SQL, blank SQL, incomplete `ORDER BY` returned via `Process` `decision=error`, `DB_SYNTAX_ERROR` / parse stage, no results; fake executor/session method zero calls, audit fixture records an error. | Independent end-to-end evidence of zero access to the real database, all pipeline error paths, and full warehouse test results on release day. |

The dialects and error codes of the above assertions are taken from the corresponding test source code; the offline acceptance range of the parser cannot be used to expand the support conclusion of DM/Oracle/YashanDB in the complete gateway. For the DM/Oracle Batch 63 return range, see [ dialect boundary ](dm-oracle-dialect.md), and for the YashanDB Batch 66 offline profile and standard entry restrictions, see [YashanDB boundary ](yashan-dialect.md).

## Offline execution and dependency boundaries

- This document batch did not run Go tests, connect to the database, pull images, or download dependencies. The above table is the source code verification conclusion. **is not the PASS record** of this batch.
- The test package of `internal/pipeline` includes `e2e_mysql_test.go` and `e2e_postgres_test.go`, which import `testcontainers-go`; whether to skip the real database path under `-short` should be based on the corresponding test guard and the actual running output. Even if only the boundary test name is run, Go must first compile the test dependencies of the package; if the offline environment lacks toolchain, CGO or module cache, compilation failure cannot be attributed to boundary assertions.
- repository [PostgreSQL parser performance review ](PARSER_PERF.md) record: In the specified Linux/Go 1.26.8 environment, `go test -short ./internal/parser ./internal/rules ./internal/pipeline -count=1 -p 1` passed the three guarantees; the full repository `go test -short ./... -p 1` was not completed within the time limit, so the short status of the full repository cannot be written as PASS. P99 The two sets of 5/5 results only represent the designated environment, and exclusive retesting is still required on the release day.
- On release day, parser, rules, pipeline and full warehouse tests need to be re-run in an environment where dependencies are prepared and complete exit codes can be recorded; real database E2E is also executed according to the release manifest and explicit environment gates. When dependencies are missing offline, it is recorded as "not executed/unfinished" and cannot be recorded as a passed test.
