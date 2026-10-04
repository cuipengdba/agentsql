package businessdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// PostgresDMLMaxBindAttempts is consumed by the future S6 coordinator. A
	// closure race requires a fresh native transaction; the binder never begins
	// or retries a transaction behind the coordinator's terminal owner.
	PostgresDMLMaxBindAttempts = 2
	postgresDMLClosureVersion  = "agentsql.pg-dml-closure.v1"
	postgresDMLEnrollVersion   = "agentsql.pg-dml-enrollment.v1"
)

// PostgresDMLManifest is the DML extension ABI layered on the common prepared
// statement seal. TargetRelationOID is the analyzed Query.resultRelation, not
// a name resolved by the Go adapter.
type PostgresDMLManifest struct {
	PostgresPreparedManifest
	TargetRelationOID uint32
	Shape             b5dml.StatementShape
	Returning         bool
}

// PostgresDMLEnrollment is candidate-phase, read-only evidence. It contains no
// connection or execution capability and is compared against a fresh manifest
// after ordered OID locks have been acquired in the native write transaction.
type PostgresDMLEnrollment struct {
	Manifest          PostgresDMLManifest
	Catalog           PostgresCatalogFrame
	Facts             b5dml.StatementFacts
	ClosureDigest     string
	BinderFingerprint string
	Fingerprint       string
}

// PostgresDMLAuthorization supplies only S5a lattice inputs that do not come
// from the database binder. Approval is intentionally absent: it cannot fill a
// missing action, write-target, or reference grant.
type PostgresDMLAuthorization struct {
	PrincipalID          string
	DatasourceID         string
	Policies             []b5dml.Policy
	PreliminaryAllowed   bool
	DatasourceSupported  bool
	ReservedTarget       bool
	PolicySnapshotDigest string
	PlanDigest           string
	Attestations         []b5dml.BinderAttestation
}

// PostgresDMLNativeTx is an adapter over a native transaction already owned by
// S5a/S4b. It does not implement BEGIN, cleanup, COMMIT, ROLLBACK, or terminal
// ownership and never exposes the raw transaction after attachment.
type PostgresDMLNativeTx struct {
	mu         sync.Mutex
	tx         pgx.Tx
	backendPID uint32
	claimed    bool
}

// AttachPostgresDMLNativeTx joins the DML binder to S5a's proven native-BEGIN
// state. The BeginMachine remains the sole cleanup state machine.
func AttachPostgresDMLNativeTx(ctx context.Context, tx pgx.Tx, begin *b5dml.BeginMachine) (*PostgresDMLNativeTx, error) {
	if ctx == nil || tx == nil || begin == nil || begin.ResourceState() != b5dml.BeginNativeBegun {
		return nil, catalogAuthError("AUTH_DML_NATIVE_BEGIN_REQUIRED")
	}
	var pid uint32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil || pid == 0 {
		return nil, postgresDatabaseError(ctx, DBStageMetadata, "verify PostgreSQL DML native transaction", err)
	}
	return &PostgresDMLNativeTx{tx: tx, backendPID: pid}, nil
}

// PostgresPreparedDML holds only a random server-side prepared statement in
// the caller-owned native transaction. Close deallocates it; it never ends the
// transaction, because terminal disposition belongs to S4b.
type PostgresPreparedDML struct {
	mu       sync.Mutex
	native   *PostgresDMLNativeTx
	name     string
	manifest PostgresDMLManifest
	fpre     PostgresCatalogFrame
	executed bool
	closed   bool
	discard  bool
}

func (prepared *PostgresPreparedDML) Manifest() PostgresDMLManifest {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.manifest
}

func (prepared *PostgresPreparedDML) Fpre() PostgresCatalogFrame {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.fpre
}

func (prepared *PostgresPreparedDML) DiscardRequired() bool {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.discard
}

// EnrollPostgresDML performs the candidate phase in an isolated read-only
// repeatable-read transaction. No candidate prepared statement survives.
func (executor *PostgresExecutor) EnrollPostgresDML(ctx context.Context, rawSQL, datasourceID string, budget PostgresCatalogBudget) (PostgresDMLEnrollment, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil || datasourceID == "" {
		return PostgresDMLEnrollment{}, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
	}
	manifest, frame, facts, err := executor.discoverPostgresDML(ctx, rawSQL, datasourceID, budget)
	if err != nil {
		return PostgresDMLEnrollment{}, err
	}
	return dmlEnrollmentFrom(manifest, frame, facts), nil
}

func (executor *PostgresExecutor) discoverPostgresDML(ctx context.Context, rawSQL, datasourceID string, budget PostgresCatalogBudget) (PostgresDMLManifest, PostgresCatalogFrame, b5dml.StatementFacts, error) {
	businessRank, err := lockrank.Acquire(ctx, lockrank.Business)
	if err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, err
	}
	defer businessRank.Release()
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL DML candidate connection", err)
	}
	defer connection.Release()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL DML candidate transaction", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, err
	}
	if err := setPostgresBinderSearchPath(ctx, tx); err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, err
	}
	name, err := randomPreparedName()
	if err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, catalogAuthError("AUTH_DATABASE_ERROR")
	}
	manifest, err := prepareAndReadPostgresDMLManifest(ctx, tx, name, rawSQL, budget)
	if err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, err
	}
	frame, err := scanPostgresDMLCatalog(ctx, tx, manifest, budget)
	if err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, err
	}
	facts, err := postgresDMLFacts(manifest, frame, datasourceID)
	if err != nil {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, err
	}
	decision := b5dml.AnalyzeStatement(facts)
	if !decision.Allowed {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, &postgresDMLStatementError{reason: decision.Reason}
	}
	if closure := b5dml.CheckClosure(b5dml.DialectPostgreSQL, facts.Shape, nil); !closure.Allowed {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, catalogAuthError(string(closure.Reason))
	}
	_ = deallocatePostgresPrepared(ctx, tx, name)
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return PostgresDMLManifest{}, PostgresCatalogFrame{}, b5dml.StatementFacts{}, postgresDatabaseError(ctx, DBStageRollback, "rollback PostgreSQL DML candidate", err)
	}
	return manifest, frame, facts, nil
}

// PrepareBoundPostgresDML performs the execution-transaction half of the
// two-phase binder. A retryable closure race must be handled outside this
// transaction, with at most PostgresDMLMaxBindAttempts total attempts.
func (executor *PostgresExecutor) PrepareBoundPostgresDML(ctx context.Context, native *PostgresDMLNativeTx, rawSQL string, enrollment *PostgresDMLEnrollment, auth PostgresDMLAuthorization, budget PostgresCatalogBudget) (*PostgresPreparedDML, b5dml.AuthorizationDecision, error) {
	if executor == nil || ctx == nil || native == nil || native.tx == nil || enrollment == nil || budget == nil || strings.TrimSpace(rawSQL) == "" {
		return nil, b5dml.AuthorizationDecision{}, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
	}
	native.mu.Lock()
	defer native.mu.Unlock()
	if native.claimed {
		return nil, b5dml.AuthorizationDecision{}, catalogAuthError("AUTH_PREPARED_STATE_INVALID")
	}
	if err := setPostgresBinderSearchPath(ctx, native.tx); err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	if err := lockPostgresRelations(ctx, native.tx, enrollment.Manifest.Relations, budget); err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	name, err := randomPreparedName()
	if err != nil {
		return nil, b5dml.AuthorizationDecision{}, catalogAuthError("AUTH_DATABASE_ERROR")
	}
	manifest, err := prepareAndReadPostgresDMLManifest(ctx, native.tx, name, rawSQL, budget)
	if err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = deallocatePostgresPrepared(context.Background(), native.tx, name)
		}
	}()
	if manifest.BackendPID != native.backendPID || !samePostgresClosure(enrollment.Manifest.Relations, manifest.Relations) {
		return nil, b5dml.AuthorizationDecision{}, newPostgresDMLClosureRace("AUTH_BIND_CLOSURE_MISMATCH")
	}
	actual, err := readPostgresUserLocks(ctx, native.tx, budget)
	if err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	if !sameOIDSet(actual, manifestRelationOIDs(manifest.PostgresPreparedManifest)) {
		return nil, b5dml.AuthorizationDecision{}, newPostgresDMLClosureRace("AUTH_BIND_CLOSURE_MISMATCH")
	}
	frame, err := scanPostgresDMLCatalog(ctx, native.tx, manifest, budget)
	if err != nil {
		if isPostgresDMLClosureDenial(err) {
			return nil, b5dml.AuthorizationDecision{}, newPostgresDMLClosureRace("AUTH_BIND_CLOSURE_MISMATCH")
		}
		return nil, b5dml.AuthorizationDecision{}, err
	}
	facts, err := postgresDMLFacts(manifest, frame, auth.DatasourceID)
	if err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	current := dmlEnrollmentFrom(manifest, frame, facts)
	if enrollment.Fingerprint != current.Fingerprint {
		return nil, b5dml.AuthorizationDecision{}, newPostgresDMLClosureRace("AUTH_CATALOG_RACE")
	}
	if err := verifyDMLAttestations(manifest.Capability, auth.Attestations); err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	if _, err := native.tx.Exec(ctx, `SELECT agentsql_catalog.seal_prepared($1)`, name); err != nil {
		return nil, b5dml.AuthorizationDecision{}, postgresDatabaseError(ctx, DBStageMetadata, "seal PostgreSQL DML prepared statement", err)
	}
	sealed, err := readPostgresDMLManifest(ctx, native.tx, name, manifest.Capability, budget)
	if err != nil {
		return nil, b5dml.AuthorizationDecision{}, err
	}
	if !samePostgresDMLPreSeal(manifest, sealed) || sealed.PlanGeneration == 0 {
		return nil, b5dml.AuthorizationDecision{}, catalogAuthError("AUTH_PREPARED_INVALIDATED")
	}
	input := b5dml.AuthorizationInput{
		PrincipalID: auth.PrincipalID, DatasourceID: auth.DatasourceID,
		Dialect: b5dml.DialectPostgreSQL, CurrentServerMajor: manifest.Capability.ServerMajor,
		Action: facts.Action, Target: facts.Target, Writes: facts.Writes, References: facts.References,
		Policies: auth.Policies, PreliminaryAllowed: auth.PreliminaryAllowed,
		DatasourceSupported: auth.DatasourceSupported, ReservedTarget: auth.ReservedTarget,
		CatalogConsistent: true, ClosureProven: true,
		PolicySnapshotDigest: auth.PolicySnapshotDigest, CatalogSnapshotDigest: frame.Fingerprint,
		ClosureDigest: current.ClosureDigest, PlanDigest: auth.PlanDigest, Attestations: auth.Attestations,
	}
	decision := b5dml.Authorize(input)
	if !decision.Allowed() {
		return nil, decision, &postgresDMLAuthorizationError{decision: decision}
	}
	manifest = sealed
	native.claimed = true
	cleanup = false
	return &PostgresPreparedDML{native: native, name: name, manifest: manifest, fpre: frame}, decision, nil
}

// Execute executes only the sealed random name and then verifies the seal,
// relation-lock closure, and catalog fingerprint before returning the command
// tag. Any verification failure requires terminal discard/rollback.
func (prepared *PostgresPreparedDML) Execute(ctx context.Context, budget PostgresCatalogBudget) (pgconn.CommandTag, error) {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed || prepared.executed || prepared.native == nil || prepared.native.tx == nil || ctx == nil || budget == nil {
		return pgconn.CommandTag{}, catalogAuthError("AUTH_PREPARED_STATE_INVALID")
	}
	tag, err := prepared.native.tx.Exec(ctx, `EXECUTE `+quoteInternalPreparedName(prepared.name))
	if err != nil {
		prepared.discard = true
		return pgconn.CommandTag{}, postgresDatabaseError(ctx, DBStageExecute, "execute sealed PostgreSQL DML", err)
	}
	prepared.executed = true
	manifest, err := readPostgresDMLManifest(ctx, prepared.native.tx, prepared.name, prepared.manifest.Capability, budget)
	if err != nil || !samePreparedSeal(prepared.manifest.PostgresPreparedManifest, manifest.PostgresPreparedManifest) || manifest.TargetRelationOID != prepared.manifest.TargetRelationOID || manifest.Shape != prepared.manifest.Shape || manifest.Returning != prepared.manifest.Returning {
		prepared.discard = true
		if err != nil {
			return pgconn.CommandTag{}, err
		}
		return pgconn.CommandTag{}, catalogAuthError("AUTH_PREPARED_INVALIDATED")
	}
	actual, err := readPostgresUserLocks(ctx, prepared.native.tx, budget)
	if err != nil || !sameOIDSet(actual, manifestRelationOIDs(manifest.PostgresPreparedManifest)) {
		prepared.discard = true
		if err != nil {
			return pgconn.CommandTag{}, err
		}
		return pgconn.CommandTag{}, catalogAuthError("AUTH_BIND_CLOSURE_MISMATCH")
	}
	fpost, err := scanPostgresDMLCatalog(ctx, prepared.native.tx, manifest, budget)
	if err != nil || fpost.Fingerprint != prepared.fpre.Fingerprint {
		prepared.discard = true
		if err != nil {
			return pgconn.CommandTag{}, err
		}
		return pgconn.CommandTag{}, catalogAuthError("AUTH_CATALOG_RACE")
	}
	return tag, nil
}

func (prepared *PostgresPreparedDML) Close(ctx context.Context) error {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed {
		return nil
	}
	prepared.closed = true
	if prepared.native != nil {
		prepared.native.mu.Lock()
		defer prepared.native.mu.Unlock()
		prepared.native.claimed = false
	}
	if prepared.discard || prepared.native == nil || prepared.native.tx == nil {
		return nil
	}
	return deallocatePostgresPrepared(ctx, prepared.native.tx, prepared.name)
}

func prepareAndReadPostgresDMLManifest(ctx context.Context, tx pgx.Tx, name, rawSQL string, budget PostgresCatalogBudget) (PostgresDMLManifest, error) {
	if strings.TrimSpace(rawSQL) == "" {
		return PostgresDMLManifest{}, catalogAuthError("AUTH_EXPRESSION_IDENTITY_UNSUPPORTED")
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresDMLManifest{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT agentsql_catalog.prepare_dml($1,$2)`, name, rawSQL); err != nil {
		return PostgresDMLManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "prepare PostgreSQL analyzed DML", err)
	}
	capability, err := readPostgresDMLCapability(ctx, tx, budget)
	if err != nil {
		return PostgresDMLManifest{}, err
	}
	manifest, err := readPostgresDMLManifest(ctx, tx, name, capability, budget)
	if err != nil {
		return PostgresDMLManifest{}, err
	}
	if err := validatePostgresDMLManifest(manifest, budget); err != nil {
		return PostgresDMLManifest{}, err
	}
	if err := validatePostgresObjectAllowlist(manifest.Objects, capability.Allowlist); err != nil {
		return PostgresDMLManifest{}, err
	}
	return manifest, nil
}

func readPostgresDMLCapability(ctx context.Context, tx pgx.Tx, budget PostgresCatalogBudget) (PostgresBinderCapability, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresBinderCapability{}, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT agentsql_catalog.dml_capabilities()`).Scan(&raw); err != nil {
		return PostgresBinderCapability{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL DML binder capability", err)
	}
	if err := budget.ChargeBinderBytes(len(raw)); err != nil {
		return PostgresBinderCapability{}, err
	}
	var capability PostgresBinderCapability
	if err := jsonUnmarshalNoUnknown(raw, &capability); err != nil || capability.ABI != b5dml.BinderABI || capability.ServerMajor < 14 || capability.ServerMajor > 18 || capability.Matview || capability.BuildHash == "" || capability.ExtensionHash == "" || capability.NodeManifestHash == "" || capability.AllowlistHash == "" {
		return PostgresBinderCapability{}, catalogAuthError("AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	expected := postgresDMLBinderAttestation(capability.ServerMajor)
	if capability.BuildHash != expected.BuildHash || capability.ExtensionHash != expected.ExtensionHash || capability.NodeManifestHash != expected.NodeManifestHash || capability.AllowlistHash != expected.AllowlistHash {
		return PostgresBinderCapability{}, catalogAuthError("AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	return capability, nil
}

// jsonUnmarshalNoUnknown is kept as a small seam so DML and SELECT capability
// decoding remain independent contracts while sharing the wire structure.
func jsonUnmarshalNoUnknown(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON capability values")
		}
		return err
	}
	return nil
}

func readPostgresDMLManifest(ctx context.Context, tx pgx.Tx, name string, capability PostgresBinderCapability, budget PostgresCatalogBudget) (PostgresDMLManifest, error) {
	base, err := readPostgresManifest(ctx, tx, name, capability, budget)
	if err != nil {
		return PostgresDMLManifest{}, err
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresDMLManifest{}, err
	}
	var target uint32
	var shape string
	var returning bool
	if err := tx.QueryRow(ctx, `SELECT target_relation_oid,dml_shape,has_returning FROM agentsql_catalog.prepared_dml_manifest($1)`, name).Scan(&target, &shape, &returning); err != nil {
		return PostgresDMLManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL DML manifest", err)
	}
	parsedShape, ok := postgresDMLShape(shape)
	if !ok {
		return PostgresDMLManifest{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
	}
	return PostgresDMLManifest{PostgresPreparedManifest: base, TargetRelationOID: target, Shape: parsedShape, Returning: returning}, nil
}

func validatePostgresDMLManifest(manifest PostgresDMLManifest, budget PostgresCatalogBudget) error {
	if manifest.StatementName == "" || manifest.BackendPID == 0 || manifest.RoleOID == 0 || manifest.AnalyzedDigest == "" || manifest.DependencyDigest == "" || manifest.Invalidated || manifest.ReplanCount != 0 || manifest.TargetRelationOID == 0 || manifest.HasRecursive || manifest.HasModifyingCTE {
		return catalogAuthError("AUTH_BINDER_INCOMPLETE")
	}
	if _, ok := postgresDMLAction(manifest.CommandType); !ok {
		return catalogAuthError("AUTH_BINDER_INCOMPLETE")
	}
	if err := budget.ChargeNodes(manifest.NodeCount); err != nil {
		return err
	}
	if err := budget.ChargeEdges(manifest.EdgeCount); err != nil {
		return err
	}
	if err := budget.ChargeWork(manifest.WorkUnits); err != nil {
		return err
	}
	seen := make(map[uint32]struct{}, len(manifest.Relations))
	for _, relation := range manifest.Relations {
		if relation.OID == 0 || relation.Kind != 'r' || relation.ViewDepth != 0 {
			return catalogAuthError("AUTH_RELATION_KIND_UNSUPPORTED")
		}
		seen[relation.OID] = struct{}{}
	}
	if _, ok := seen[manifest.TargetRelationOID]; !ok {
		return catalogAuthError("AUTH_BINDER_INCOMPLETE")
	}
	if err := budget.ChargePaths(len(manifest.Relations) + len(manifest.Columns)); err != nil {
		return err
	}
	for _, use := range manifest.Columns {
		if !use.ContributorComplete || use.ResultComposite || use.RelationOID == 0 {
			return catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
		if _, ok := seen[use.RelationOID]; !ok {
			return catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
		if use.Usage == "write_target" && (use.RelationOID != manifest.TargetRelationOID || use.Attnum <= 0 || use.WholeRow) {
			return catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
	}
	return nil
}

func scanPostgresDMLCatalog(ctx context.Context, tx pgx.Tx, manifest PostgresDMLManifest, budget PostgresCatalogBudget) (PostgresCatalogFrame, error) {
	if err := scanPostgresDMLNegativeClosure(ctx, tx, manifest, budget); err != nil {
		return PostgresCatalogFrame{}, err
	}
	return scanPostgresCatalog(ctx, tx, manifestRelationOIDs(manifest.PostgresPreparedManifest), manifestViewDepths(manifest.PostgresPreparedManifest), manifest.Objects, manifest.Capability.Allowlist, budget)
}

func scanPostgresDMLNegativeClosure(ctx context.Context, tx pgx.Tx, manifest PostgresDMLManifest, budget PostgresCatalogBudget) error {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return err
	}
	const query = `WITH target(relid) AS (SELECT unnest($1::oid[])), findings(priority,reason) AS (
 SELECT 1,'AUTH_IMPLICIT_OBJECT_UNCLOSED' FROM pg_catalog.pg_trigger t JOIN target x ON x.relid=t.tgrelid WHERE NOT t.tgisinternal
 UNION ALL SELECT 2,'AUTH_CONSTRAINT_CLOSURE_UNSUPPORTED' FROM pg_catalog.pg_constraint c JOIN target x ON x.relid=c.conrelid OR x.relid=c.confrelid
 UNION ALL SELECT 3,'AUTH_DEFAULT_CLOSURE_UNSUPPORTED' FROM pg_catalog.pg_attrdef d JOIN target x ON x.relid=d.adrelid
 UNION ALL SELECT 3,'AUTH_DEFAULT_CLOSURE_UNSUPPORTED' FROM pg_catalog.pg_attribute a JOIN target x ON x.relid=a.attrelid WHERE a.attidentity<>'' OR a.attgenerated<>''
 UNION ALL SELECT 4,'AUTH_REWRITE_CLOSURE_UNSUPPORTED' FROM pg_catalog.pg_class c JOIN target x ON x.relid=c.oid WHERE c.relkind<>'r' OR c.relispartition OR c.relrowsecurity OR c.relforcerowsecurity
 UNION ALL SELECT 4,'AUTH_REWRITE_CLOSURE_UNSUPPORTED' FROM pg_catalog.pg_rewrite r JOIN target x ON x.relid=r.ev_class
 UNION ALL SELECT 4,'AUTH_REWRITE_CLOSURE_UNSUPPORTED' FROM pg_catalog.pg_inherits i JOIN target x ON x.relid=i.inhrelid OR x.relid=i.inhparent
 UNION ALL SELECT 5,'AUTH_EXPRESSION_CLOSURE_UNSUPPORTED' FROM pg_catalog.pg_index i JOIN target x ON x.relid=i.indrelid WHERE i.indexprs IS NOT NULL OR i.indpred IS NOT NULL
) SELECT reason FROM findings ORDER BY priority LIMIT 1`
	var reason string
	err := tx.QueryRow(ctx, query, oidArrayLiteral(manifestRelationOIDs(manifest.PostgresPreparedManifest))).Scan(&reason)
	if err == nil {
		if chargeErr := budget.ChargeCatalogRows(1); chargeErr != nil {
			return chargeErr
		}
		return catalogAuthError(reason)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return postgresDatabaseError(ctx, DBStageMetadata, "scan PostgreSQL DML closure", err)
	}
	return nil
}

func postgresDMLFacts(manifest PostgresDMLManifest, frame PostgresCatalogFrame, datasourceID string) (b5dml.StatementFacts, error) {
	action, ok := postgresDMLAction(manifest.CommandType)
	if !ok || datasourceID == "" {
		return b5dml.StatementFacts{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
	}
	relations := make(map[uint32]b5dml.RelationIdentity, len(frame.Relations))
	for _, relation := range frame.Relations {
		relations[relation.OID] = b5dml.RelationIdentity{DatasourceID: datasourceID, DatabaseOID: frame.DatabaseOID, RelationOID: relation.OID, RelationKind: relation.Kind, Schema: relation.Schema, Name: relation.Name, CatalogFingerprint: frame.Fingerprint}
	}
	target, ok := relations[manifest.TargetRelationOID]
	if !ok {
		return b5dml.StatementFacts{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
	}
	columns := make(map[string]b5dml.ColumnIdentity, len(frame.Columns))
	insertFacts := make([]b5dml.InsertColumnFact, 0)
	for _, column := range frame.Columns {
		relation, ok := relations[column.RelationOID]
		if !ok {
			return b5dml.StatementFacts{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
		identity := b5dml.ColumnIdentity{Relation: relation, Attnum: column.Attnum, Name: column.Name, TypeOID: column.TypeOID, TypeModifier: column.Typmod, CollationOID: column.Collation}
		columns[dmlColumnKey(column.RelationOID, column.Attnum)] = identity
		if column.RelationOID == manifest.TargetRelationOID {
			insertFacts = append(insertFacts, b5dml.InsertColumnFact{Column: identity, Assignment: b5dml.AssignmentOmitted, Identity: column.Identity != 0, Generated: column.Generated != 0})
		}
	}
	writes := make([]b5dml.WriteTarget, 0)
	references := make([]b5dml.Reference, 0)
	for _, use := range manifest.Columns {
		column, present := columns[dmlColumnKey(use.RelationOID, use.Attnum)]
		if use.Usage == "write_target" {
			if !present {
				return b5dml.StatementFacts{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
			}
			source := b5dml.WriteSourceExplicit
			if use.Site == "write.implicit_null" {
				source = b5dml.WriteSourceImplicitNull
			}
			writes = append(writes, b5dml.ColumnWrite(column, source))
			for index := range insertFacts {
				if insertFacts[index].Column == column {
					if use.Site == "write.explicit_default" {
						insertFacts[index].Assignment = b5dml.AssignmentExplicitDefault
					} else if source == b5dml.WriteSourceExplicit {
						insertFacts[index].Assignment = b5dml.AssignmentExplicitValue
					}
				}
			}
			continue
		}
		kind := b5dml.ReferenceColumn
		var systemName string
		if use.WholeRow || use.Attnum == 0 {
			kind = b5dml.ReferenceWholeRow
		} else if use.Attnum < 0 {
			kind = b5dml.ReferenceSystemColumn
			systemName = postgresSystemColumnName(use.Attnum)
		} else if use.ResultComposite {
			kind = b5dml.ReferenceComposite
		} else if !present {
			kind = b5dml.ReferenceRecord
		}
		reference := b5dml.Reference{Kind: kind, Site: postgresDMLReferenceSite(use.Site), Relation: relations[use.RelationOID], Column: column, SystemName: systemName}
		references = append(references, reference)
	}
	if action == b5dml.ActionDelete {
		writes = []b5dml.WriteTarget{b5dml.RowDelete(target)}
	}
	sort.Slice(insertFacts, func(i, j int) bool { return insertFacts[i].Column.Attnum < insertFacts[j].Column.Attnum })
	return b5dml.StatementFacts{Dialect: b5dml.DialectPostgreSQL, Action: action, Shape: manifest.Shape, Target: target, Writes: writes, References: references, InsertColumns: insertFacts, Returning: manifest.Returning}, nil
}

func dmlEnrollmentFrom(manifest PostgresDMLManifest, frame PostgresCatalogFrame, facts b5dml.StatementFacts) PostgresDMLEnrollment {
	h := sha256.New()
	writeCanonicalString(h, postgresDMLClosureVersion)
	writeCanonicalString(h, manifest.Capability.BuildHash)
	writeCanonicalString(h, manifest.Capability.ExtensionHash)
	writeCanonicalString(h, manifest.Capability.NodeManifestHash)
	writeCanonicalString(h, manifest.Capability.AllowlistHash)
	writeCanonicalString(h, manifest.AnalyzedDigest)
	writeCanonicalString(h, manifest.DependencyDigest)
	writeCanonicalString(h, frame.Fingerprint)
	writeCanonicalString(h, manifest.CommandType)
	writeCanonicalUint(h, uint64(manifest.TargetRelationOID))
	writeCanonicalUint(h, uint64(manifest.Shape))
	writeCanonicalBool(h, manifest.Returning)
	closure := postgresDMLClosureVersion + ":" + hex.EncodeToString(h.Sum(nil))
	h = sha256.New()
	writeCanonicalString(h, postgresDMLEnrollVersion)
	writeCanonicalString(h, closure)
	writeCanonicalUint(h, uint64(manifest.RoleOID))
	writeCanonicalString(h, manifest.RoleName)
	writeCanonicalString(h, manifest.SearchPath)
	writeCanonicalString(h, facts.Target.CatalogFingerprint)
	fingerprint := postgresDMLEnrollVersion + ":" + hex.EncodeToString(h.Sum(nil))
	return PostgresDMLEnrollment{Manifest: manifest, Catalog: frame, Facts: facts, ClosureDigest: closure, BinderFingerprint: closure, Fingerprint: fingerprint}
}

func samePostgresDMLPreSeal(left, right PostgresDMLManifest) bool {
	return left.StatementName == right.StatementName && left.BackendPID == right.BackendPID &&
		left.TransactionID == right.TransactionID && left.RoleOID == right.RoleOID &&
		left.RoleName == right.RoleName && left.SearchPath == right.SearchPath &&
		left.AnalyzedDigest == right.AnalyzedDigest && left.DependencyDigest == right.DependencyDigest &&
		right.ReplanCount == 0 && !right.Invalidated &&
		samePostgresClosure(left.Relations, right.Relations) &&
		left.TargetRelationOID == right.TargetRelationOID && left.Shape == right.Shape &&
		left.Returning == right.Returning
}

func verifyDMLAttestations(capability PostgresBinderCapability, values []b5dml.BinderAttestation) error {
	if len(values) != 5 {
		return catalogAuthError("AUTH_BINDER_ATTESTATION_MISMATCH")
	}
	seen := make(map[int]struct{}, 5)
	matched := false
	for _, value := range values {
		if value.ServerMajor < 14 || value.ServerMajor > 18 || value.ABI != b5dml.BinderABI || value.BuildHash == "" || value.ExtensionHash == "" || value.NodeManifestHash == "" || value.AllowlistHash == "" {
			return catalogAuthError("AUTH_BINDER_ATTESTATION_MISMATCH")
		}
		if _, duplicate := seen[value.ServerMajor]; duplicate {
			return catalogAuthError("AUTH_BINDER_ATTESTATION_MISMATCH")
		}
		seen[value.ServerMajor] = struct{}{}
		if value != postgresDMLBinderAttestation(value.ServerMajor) {
			return catalogAuthError("AUTH_BINDER_ATTESTATION_MISMATCH")
		}
		if value.ServerMajor == capability.ServerMajor {
			matched = value.BuildHash == capability.BuildHash && value.ExtensionHash == capability.ExtensionHash && value.NodeManifestHash == capability.NodeManifestHash && value.AllowlistHash == capability.AllowlistHash
		}
	}
	if len(seen) != 5 || !matched {
		return catalogAuthError("AUTH_BINDER_ATTESTATION_MISMATCH")
	}
	return nil
}

// PostgresDMLBinderAttestations returns the immutable PG14-18 artifact hashes
// accepted by this build. Callers put this exact set into the S5a
// proof; runtime capability bytes must also match the current-major entry.
func PostgresDMLBinderAttestations() []b5dml.BinderAttestation {
	values := make([]b5dml.BinderAttestation, 0, 5)
	for major := 14; major <= 18; major++ {
		values = append(values, postgresDMLBinderAttestation(major))
	}
	return values
}

func postgresDMLBinderAttestation(major int) b5dml.BinderAttestation {
	suffix := fmt.Sprintf("%d", major)
	return b5dml.BinderAttestation{
		Mode: b5dml.BinderAttestationNative, ServerMajor: major, ABI: b5dml.BinderABI,
		BuildHash:        postgresDMLArtifactHash("agentsql-binder-dml-build-v1-pg" + suffix),
		ExtensionHash:    postgresDMLArtifactHash("agentsql-binder-dml-source-v1-pg" + suffix),
		NodeManifestHash: postgresDMLArtifactHash("query-dml-write-reference-v1-pg" + suffix),
		AllowlistHash:    postgresDMLArtifactHash("builtin-exact-oids-dml-v1-pg" + suffix),
	}
}

func postgresDMLArtifactHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func postgresDMLAction(command string) (b5dml.Action, bool) {
	switch command {
	case "INSERT":
		return b5dml.ActionInsert, true
	case "UPDATE":
		return b5dml.ActionUpdate, true
	case "DELETE":
		return b5dml.ActionDelete, true
	default:
		return b5dml.ActionUnknown, false
	}
}

func postgresDMLShape(shape string) (b5dml.StatementShape, bool) {
	values := map[string]b5dml.StatementShape{"SIMPLE": b5dml.ShapeSimple, "UPDATE_FROM": b5dml.ShapeUpdateFrom, "DELETE_USING": b5dml.ShapeDeleteUsing, "INSERT_SELECT": b5dml.ShapeInsertSelect, "UPSERT": b5dml.ShapeUpsert, "MERGE": b5dml.ShapeMerge, "CTE": b5dml.ShapeCTE, "WRITABLE_CTE": b5dml.ShapeWritableCTE, "DML_SUBQUERY": b5dml.ShapeDMLSubquery}
	value, ok := values[shape]
	return value, ok
}

func postgresDMLReferenceSite(site string) b5dml.ReferenceSite {
	switch site {
	case "returning":
		return b5dml.ReferenceReturning
	case "conflict_check", "conflict_update":
		return b5dml.ReferenceConflictCheck
	case "internal_read", "security_qual":
		return b5dml.ReferenceInternalRead
	case "expression_query":
		return b5dml.ReferenceSubquery
	case "join_where":
		return b5dml.ReferenceWhere
	default:
		return b5dml.ReferenceExpression
	}
}

func postgresSystemColumnName(attnum int16) string {
	switch attnum {
	case -1:
		return "ctid"
	case -2:
		return "xmin"
	case -3:
		return "cmin"
	case -4:
		return "xmax"
	case -5:
		return "cmax"
	case -6:
		return "tableoid"
	default:
		return fmt.Sprintf("system_attnum_%d", attnum)
	}
}

func dmlColumnKey(relation uint32, attnum int16) string {
	return fmt.Sprintf("%d/%d", relation, attnum)
}

type postgresDMLStatementError struct{ reason b5dml.StatementReason }

func (err *postgresDMLStatementError) Error() string               { return string(err.reason) }
func (err *postgresDMLStatementError) AuthorizationReason() string { return string(err.reason) }

type postgresDMLAuthorizationError struct{ decision b5dml.AuthorizationDecision }

func (err *postgresDMLAuthorizationError) Error() string { return string(err.decision.Reason()) }
func (err *postgresDMLAuthorizationError) AuthorizationReason() string {
	return string(err.decision.Reason())
}

type PostgresDMLClosureRaceError struct{ reason string }

func newPostgresDMLClosureRace(reason string) error {
	return &PostgresDMLClosureRaceError{reason: reason}
}
func (err *PostgresDMLClosureRaceError) Error() string               { return err.reason }
func (err *PostgresDMLClosureRaceError) AuthorizationReason() string { return err.reason }
func (err *PostgresDMLClosureRaceError) RetryableOutsideTransaction() bool {
	return true
}

func IsPostgresDMLClosureRace(err error) bool {
	var race *PostgresDMLClosureRaceError
	return errors.As(err, &race)
}

func isPostgresDMLClosureDenial(err error) bool {
	typed, ok := err.(interface{ AuthorizationReason() string })
	if !ok {
		return false
	}
	switch typed.AuthorizationReason() {
	case "AUTH_IMPLICIT_OBJECT_UNCLOSED", "AUTH_CONSTRAINT_CLOSURE_UNSUPPORTED",
		"AUTH_DEFAULT_CLOSURE_UNSUPPORTED", "AUTH_REWRITE_CLOSURE_UNSUPPORTED",
		"AUTH_EXPRESSION_CLOSURE_UNSUPPORTED", "AUTH_TYPE_CLOSURE_UNSUPPORTED",
		"AUTH_IMPLICIT_OBJECT_UNSUPPORTED", "AUTH_RELATION_KIND_UNSUPPORTED":
		return true
	default:
		return false
	}
}

var (
	_ interface{ AuthorizationReason() string } = (*postgresDMLStatementError)(nil)
	_ interface{ AuthorizationReason() string } = (*postgresDMLAuthorizationError)(nil)
	_ interface{ AuthorizationReason() string } = (*PostgresDMLClosureRaceError)(nil)
)
