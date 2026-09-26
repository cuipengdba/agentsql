# Easy-deploy binder S0 findings

Date: 2026-09-25 (Asia/Shanghai)  
Repository baseline: `feature/v0.4`, `407abb53574d3e8502ae698040eaa62bd82f4cd5`  
Scope: evidence only; no production code, commit, or feature activation.

## Environment and evidence set

The runner used disposable official containers and the same fixture through a
superuser (`spike`) and an ordinary `NOSUPERUSER/NOCREATEDB/NOCREATEROLE`
login (`regular`). Exact server builds were:

| major | server |
|---:|---|
| 14 | 14.24, Debian build, amd64 |
| 15 | 15.19, Debian build, amd64 |
| 16 | 16.15, Debian build, amd64 |
| 17 | 17.11, Debian build, amd64 |
| 18 | 18.6, Debian build, amd64 |

There are 19 statement shapes × 5 majors × 2 account types = **190** primary
cells. Every cell could be prepared and explained. That is a server acceptance
result, not an authorization proof. Raw records are in `raw/pgNN-*.jsonl`; the
compact table is `raw/matrix.csv`.

## Decisive observations

1. **There is no SQL API for the analyzed tree.** In all five majors,
   `pg_prepared_statements` exposes only `name, statement, prepare_time,
   parameter_types, from_sql, generic_plans, custom_plans`. It exposes no
   relation/column OIDs, attnums, rewritten tree, dependency digest,
   invalidation generation, or reparse count. A prepared statement has no
   catalog object OID that can be joined to `pg_depend`.
2. **EXPLAIN is a plan, not a binder manifest.** JSON output contains names and
   deparsed expression strings, not authority-bearing relation OIDs/attnums or
   output/reference/write-target classification. The join plan included
   columns not requested as final output; `UPDATE ... SET b=...` showed `ctid`
   and the assigned constant but did not identify `b` as a write target.
3. **Views lose their authorization identity in the plan.** PREPARE held locks
   on the view and its base relation, while EXPLAIN listed only the base table.
   Nested views behaved the same. `pg_depend` gave rule-wide column edges but
   not per-output lineage or clause-site usage.
4. **Trigger bodies are opaque.** PREPARE and EXPLAIN of an UPDATE on a table
   with an AFTER trigger locked/listed only the target table. The trigger's
   PL/pgSQL body writes `edb.audit_log`, but the function's `pg_depend` rows did
   not reference that table. In contrast, an SQL rewrite rule did lock and plan
   `audit_log`. Therefore relation locks/catalog dependencies cannot be treated
   as a complete arbitrary-DML closure; trigger presence must be rejected unless
   a stronger binder closes it.
5. **Partition information appears at a different phase.** PREPARE locked only
   the partitioned parent. EXPLAIN planning added the selected child lock and
   listed only the child in the plan. A pre-plan lock set and a plan relation set
   are each incomplete descriptions of arbitrary execution.
6. **Prepared statements silently rebind across transactions.** In all 10
   major/account combinations, dropping and recreating `edb.aba` changed its
   OID, yet the old named prepared statement successfully explained against the
   new relation. The before/after plan text was identical, and the only visible
   counters were generic/custom plan counts. Cross-request prepared-plan reuse
   therefore cannot attest stable object identity.
7. **Plan digests are unstable.** With identical SQL and catalog, changing
   planner GUCs produced an Index Scan versus Seq Scan in all 10 combinations;
   all plan digests differed. `EXPLAIN (GENERIC_PLAN ...)` was unavailable in
   PG14/15 and available in PG16–18, but it still returned a plan rather than an
   analyzed/dependency identity.
8. **Role and search path matter.** The RLS plan differed between ordinary and
   superuser in all majors. The same unqualified function SQL prepared under
   `edb,pg_catalog` and `alt,pg_catalog` produced different meanings in all 10
   combinations. Binding must use the execution role and a fixed search path;
   role/search-path values belong in the proof.
9. **EXPLAIN may run user code while planning.** Explaining a constant call to
   an `IMMUTABLE` PL/pgSQL function that calls `pg_sleep(0.050)` took
   52.568–54.065ms. EXPLAIN is therefore not a safe pre-authorization operation
   for arbitrary expressions.
10. **Ordinary-account catalog/lock access was sufficient for the catalog
    kernel.** The ordinary login could read the tested `pg_class`,
    `pg_attribute`, `pg_depend`, `pg_rewrite`, `pg_trigger`, `pg_policy`, and
    `pg_get_*` metadata, inspect its own locks, PREPARE, EXPLAIN, and take the
    required table locks. Metadata visibility matched the superuser for the
    fixture. This does not make deparsed metadata a typed analyzed tree.
11. **Transaction-scoped relation fencing works.** In all five majors, a
    PREPARE-held `AccessShareLock` made concurrent `ALTER TABLE` fail with
    SQLSTATE `55P03` at approximately the 250ms lock timeout; after rollback the
    same DDL succeeded. Known OIDs can therefore be locked in numeric order and
    verified with `pg_locks` for a closed relation subset.

## What SQL/catalog can prove

It can prove server acceptance; resolve explicitly named catalog objects to
OIDs; read stable relation/column identity fields; enumerate and reject known
triggers, rules, RLS, defaults, generated columns, constraints, indexes,
partitions and inheritance; take and verify transaction-scoped relation locks;
and compute a canonical catalog fingerprint from typed catalog columns.

For a deliberately closed, gateway-parsed grammar over schema-qualified
ordinary base tables, those facts are enough to construct a conservative
manifest and cross-check that PREPARE acquired no unexpected data-relation
locks. They are not enough to infer that manifest for arbitrary SQL.

## What SQL/catalog cannot prove

- arbitrary output/reference lineage through views, joins, set operations,
  whole-row/composite forms, aliases and rewritten expressions;
- exact function/operator/cast/collation/type identities used by an arbitrary
  analyzed expression;
- a plan-independent analyzed-tree digest equivalent to the C extension;
- complete implicit execution closure for procedural trigger/routine bodies;
- exact DML write-target/reference classification from PREPARE/EXPLAIN;
- cross-request prepared-statement object identity or an invalidation/reparse
  generation.

These gaps are information-boundary gaps, not missing catalog queries.

## Timing

Each cell used a warmed physical connection, 20 warmups and 120 serial samples
on Docker Desktop loopback. Nearest-rank percentiles:

| path | p95 range across five majors/two roles |
|---|---:|
| BEGIN + fixed path + explicit lock + PREPARE + typed catalog snapshot + DEALLOCATE + ROLLBACK | **9.546–11.875ms** |
| BEGIN + lock + catalog revalidation of an already parsed closed shape + ROLLBACK | **6.477–8.876ms** |
| existing C-extension per-request reference supplied for comparison | **~86ms p95** |

The pure-SQL figures exclude query execution/response sealing and prove a much
narrower semantic set, so they are not an apples-to-apples claim that SQL is a
faster analyzed-tree binder. They show that catalog/lock overhead itself is not
the blocker; missing semantic information is.

## S0 decision

**NO**: SQL PREPARE/EXPLAIN/catalogs cannot replace the C extension for arbitrary
SELECT/DML or produce an equivalent analyzed/dependency digest.

**YES, conditionally**: a zero-server-install default is feasible as a closed
grammar catalog binder over ordinary base tables. It must derive facts from a
gateway parser/resolver, use PREPARE only as same-session acceptance/lock-set
cross-check, never use EXPLAIN or deparsed plan strings as authorization facts,
and reject every shape or catalog state outside its proof envelope. Views and
richer PostgreSQL semantics continue to require the optional C binder.

## Cleanup audit

Every successful run terminated its exact testcontainers handle. The final
Docker audit found no container with the `agentsql.easy-deploy.s0=true` label;
only the two pre-existing unlabeled demo containers remained
(`agentsql-demo-demo-postgres-1` and `agentsql-demo-demo-mysql-1`). No image,
volume, network, or container prune was used.
