# DM8 / Oracle dialect deepens the boundary (v0.5 batch 13)

**Language:** [English](dm_oracle_deep.en.md) | [Simplified Chinese](dm_oracle_deep.md)

Status date: 2026-10-02. This article only records facts that have been verified by code, unit tests, or specified containers; unverified items are listed separately.

## 1. Circumstantial evidence

- `dm8-v06`: `dm8_single:dm8_20241022_rev244896_x86_rh6_64`, `127.0.0.1:5236`, the container is Up.
- `oracle-v06`: `container-registry.oracle.com/database/free:latest`, `127.0.0.1:1521`, the container is healthy.
- Oracle used SYSTEM to log in to `FREEPDB1` successfully; `SESSION_USER` and `CURRENT_SCHEMA` are both `SYSTEM`, and the version is Oracle AI Database 26ai Free 23.26.3.0.0.
- The DM returns `-2501 Invalid username or password` using the `SYSDBA/SYSDBA` login given in the mission sheet. Therefore, this batch does not mark the DM real database query or EXPLAIN as passed.

## 2. parser research and decision

The approval parser currently only registers `postgres` and `mysql`; rule assembly, static evaluation, and column-level authorization paths only accept these two dialects. DM/Oracle cannot safely alias to any existing dialect:

- The placeholders are different: PostgreSQL `$N`, DM driver `?`, Oracle driver `:N`; the ordinary `Query` interface does not receive parameters and cannot be rewritten and guessed for binding.
- There are semantic differences in pagination such as `LIMIT/OFFSET`, `FETCH FIRST/OFFSET`, and history `ROWNUM`. Automatic splicing will change the sorting, subquery, or lock semantics.
- Oracle treats empty strings as `NULL`; implicit comparisons of characters/numbers/dates and DM compatibility mode cannot be inherited by PostgreSQL/MySQL.
- There are differences in quotes, reserved words, alias visibility, case folding, `DUAL`, and pseudo columns.

This batch does not modify the main parser and does not declare DM/Oracle to be connected to the full authorization pipeline. The dialect execution layer only accepts a very narrow generic `SELECT`: the first word must be `SELECT`, comments, semicolons, bind markers, parentheses (functions and subqueries), `INTO`, write/DDL/transaction keywords, and `NEXTVAL` are prohibited. SQL that is legal but not in this subset also fails to close. The original SQL text does not perform paging or placeholder rewriting.

If you later connect to the pipeline, you need to have independent DM and Oracle parser/semantic proof layer, and provide corresponding rule sets simultaneously; you cannot just add an alias to `parser.NewParser`.

## 3. Business inquiry

Already implemented:

- DM shares strict read-only access with Oracle's pool-level `Query` and physical session `Query`.
- A positive `rowLimit` must be passed in; the number of columns, cells, single rows, and total result bytes of `collectRows` are reused.
- When reading row `rowLimit+1`, only set `Truncated=true` and do not return additional rows; then close the result set.
- Use datasource statement timeout; timeout, network and driver errors return stable `DBError` without exposing driver messages.
- `Execute`, `BeginWriteTx` and all write transactions remain fail-closed.

Actual measurement: Oracle pool-level query generates three rows from `DUAL CONNECT BY`, `rowLimit=2` returns two rows and `Truncated=true`; physical session query `SYS.DUAL` returns `X`. DM The same code path and single test have been completed, but the current instance credentials are inconsistent, and the real database needs to be tested.

## 4. EXPLAIN normalization

Oracle has implemented and tested on `oracle-v06`:

1. Generate a safe `STATEMENT_ID` of no more than 30 bytes within the same physical connection and database transaction.
2. Execute `EXPLAIN PLAN SET STATEMENT_ID='<内部值>' FOR <已验证 SELECT>`.
3. Fixed reading `ID,PARENT_ID,OPERATION,OPTIONS,CARDINALITY,COST` of `PLAN_TABLE` up to 4096 nodes.
4. Requires unique ID, full parent node, root node `ID=0` and is `SELECT STATEMENT`, root cardinality/cost must be a non-negative finite number.
5. Root `CARDINALITY` maps to `EstScanRows`, root `COST` maps to `EstCost`; `INDEX` maps to `UsesIndex`, and `TABLE ACCESS/FULL` maps to `SeqScan`.
6. `Raw` only saves the node number, operation, option, cardinality and cost, and does not read the object name; the PLAN_TABLE write is rolled back on success or failure.

Unknown column shape, illegal tree, non-SELECT root, overflow, truncation, or parsing failure are all fail-closed. Oracle real plan normalization passed, residual count is 0 after rollback with `STATEMENT_ID` for manual study.

DM EXPLAIN is not implemented: It is currently impossible to use the given credentials to collect the real output of the image, and continues to return execution/fail-closed; parsing cannot be guessed based on other version text formats.

## 5. Misclassification

Category only reads structured driver error codes: DM `dm8.DmError.ErrCode`, Oracle `network.OracleError.ErrCode`. The unknown driver code is unified `execution`, and the original message is not retained.

| dialect | Driver code | stable classification |
| --- | --- | --- |
| DM | `-2501` | authentication |
| DM | `-2007` | syntax |
| DM | `-5501` to `-5999` | permission |
| DM | `6001/6060/9007/9008/20001` or network error | connection |
| Oracle | `1017` | authentication |
| Oracle | `1031` | permission |
| Oracle | Explicitly listed common `ORA-009xx` syntax codes | syntax |
| Oracle | `904/942/1` | column-not-found / object-not-found / constraint |
| Oracle | Instance/session termination code, `12100..12699`, or network error | connection |

Context deadline/cancel is always prioritized as timeout/interrupted. The single test covers DM/Oracle's connection, permissions, syntax, authentication and unknown codes; Oracle's real database also verifies that the wrong password is authentication and the invalid SELECT is syntax.

DM error codes are based on the interval description in the appendix of Dameng's official "Programmer's Manual" and the `-2007` example in the official FAQ; Oracle codes are based on the definitions of ORA-00900, ORA-01017, and ORA-01031 in Oracle's official error help.

## 6. Unrealized and subsequent small batches

- DM real database business SELECT: The code has been implemented. The current `SYSDBA/SYSDBA` is rejected by the instance. `TestDMDiscoveryE2E` will be run again after the actual password is provided.
- DM EXPLAIN: Not implemented, not tested, kept fail-closed.
- Oracle/DM INSERT, UPDATE, DELETE, DDL, explicit transactions: not implemented, remain fail-closed.
- DM/Oracle's complete parser, rule matrix, column-level authorization/masking pipeline: not implemented; this batch of narrow query capabilities does not mean that these boundaries have been opened.
- SELECTs with functions, subqueries, CTEs, annotations, bind variables, or parentheses: rejected even if the database syntax is valid, leaving small batches to standalone parsers.
- When Oracle PLAN_TABLE does not exist or the account does not have permissions: a stable error of permission/object/execution is returned and no attempt is made to create PLAN_TABLE.
