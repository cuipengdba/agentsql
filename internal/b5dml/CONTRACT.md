# B5 S5a DML/begin contract (feature off)

This package is an isolated contract implementation. The feature-off S5b
adapter in `internal/authorizedexecute/internal/businessdb/postgres_dml_binder.go`
is its only non-test consumer. There is still no production caller, migration,
or feature flag. `agentsql-binder-dml-1` remains mandatory. Native mode requires
the complete PostgreSQL 14–18 attestation set; catalog-closed mode requires one
embedded grammar/query-pack attestation for the exact probed server major.

## Grant schema and lattice

S1b must add an independent DML grant schema. The existing SELECT policy schema
cannot exactly encode an action bound to a catalog relation, a column/row write
target, reference-only internal reads, or closure. It also carries SELECT mask
semantics, which are forbidden on the write path. No `columnauth.ProtectionPlan`
type is accepted or imported here.

The decision order is:

1. preliminary agent/profile/statement-class deny;
2. dialect/datasource unsupported;
3. reserved schema/OID target deny;
4. action deny, then missing action allow;
5. policy/catalog/binder identity unproven, then closure unproven;
6. write-target deny, then missing write-target allow;
7. unsupported reference form, reference deny, then missing reference allow;
8. allow.

For every required action, write target, and reference, any exact applicable
deny is absorbing. In the absence of a deny, allows from multiple policies are
unioned, so separate policies may supply separate required elements. Approval
does not participate in this meet and cannot supply a missing grant.

The proof digest uses length-framed canonical fields and includes every policy
ID/revision/grant, complete relation and column catalog identity, policy and
catalog snapshot digests, closure and plan digests, the decision, and either all
five independent native PG14–18 binder attestations or the exact current-major
catalog-closed attestation. Unknown/incomplete identities fail closed. Formal
persisted proof/JSON schema naming remains S1b work.

## Statement meaning

- INSERT emits every live target column. An omitted plain column is an implicit
  NULL write and still needs a write grant. Explicit DEFAULT, omitted defaults,
  identity/sequence, and generated columns are rejected.
- UPDATE writes exactly its target-list columns. FROM columns remain references.
- DELETE has one relation-level row write target, not a fabricated set of all
  table columns. USING/WHERE columns remain references.
- UPDATE FROM and DELETE USING preserve this split for audit/golden comparison,
  then reject as complex v0.4 forms. RETURNING always rejects externally.
- Whole-row Vars, `count(table_alias)`, record/composite values, and system
  columns such as `ctid`, `xmin`, and `tableoid` are typed references that are
  deliberately unsupported rather than silently expanded.

## Closure and native BEGIN

The closure matrix rejects triggers (including the observed trigger write to a
relation absent from the manifest), FK/cascade, constraint/internal reads,
rule/view/partition/UDF/RLS, defaults/identity/generated, expression/partial
indexes, domains/coercions, and eight complex DML/CTE/subquery shapes. MySQL is
always unsupported before business connection acquisition.

BEGIN cleanup is based on resource facts. Before connection acquisition it only
releases the unused claim. A pinned connection is reusable only for proven
not-sent/zero-byte BEGIN or a correlated full rejection after every release
check passes. Missing/contradictory BEGIN ACK never causes a guessed ROLLBACK:
confirmed backend absence is discarded; otherwise the resource is quarantined
as `DISCARD_UNCONFIRMED`. Once native BEGIN is proven, cleanup acquires the S4a
generation-bound terminal owner and requests exactly one typed rollback. Every
failure result remains `consumed_begin_failed`.
