package businessdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	closedGrammarManifestHash = "catalog-closed-v1-select-s3-dml-s4"
	closedQueryPackHash       = "catalog-closed-v1-self-tested-query-pack-v2"
	closedEncoderVersion      = "catalog-closed-canonical-v1"
	closedBuiltinManifestHash = "catalog-closed-v1-exact-operator-builtins"
	closedCacheVersion        = "agentsql.closed-cache.v2"
)

type ClosedRelationRef struct {
	Schema string
	Name   string
}

type ClosedCatalogCandidate struct {
	Refs       []ClosedRelationRef
	Frame      PostgresCatalogFrame
	Identity   SemanticIdentity
	Digest     string
	Capability CapabilityAttestation
}

type NativeCapabilityExpectation struct {
	ABI              string
	ServerMajor      int
	ExtensionVersion string
	BuildHash        string
	ExtensionHash    string
	NodeManifestHash string
	AllowlistHash    string
}

type BinderCapabilityHandshake struct {
	SelectedMode         BinderMode
	Closed               CapabilityAttestation
	Native               CapabilityAttestation
	NativeHealth         string
	NativeFilesAvailable bool
	NativeInstalled      bool
}

// ProbeEasyDeployBinderCapabilities performs the production dual-mode
// handshake. Extension absence, permission failure or mismatch makes native C
// unhealthy but does not make the closed catalog capability fail.
func (executor *PostgresExecutor) ProbeEasyDeployBinderCapabilities(ctx context.Context, expectation NativeCapabilityExpectation, budget PostgresCatalogBudget) (BinderCapabilityHandshake, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil {
		return BinderCapabilityHandshake{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return BinderCapabilityHandshake{}, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL easy-deploy probe", err)
	}
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		connection.Release()
		return BinderCapabilityHandshake{}, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL easy-deploy probe", err)
	}
	defer func() { _ = cleanupPostgresPreparedConnection(executor, connection, tx, "") }()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return BinderCapabilityHandshake{}, err
	}
	identity, serverVersion, err := readClosedSessionIdentity(ctx, tx, budget)
	if err != nil {
		return BinderCapabilityHandshake{}, err
	}
	if err := closedCatalogSelfTest(ctx, tx, budget); err != nil {
		return BinderCapabilityHandshake{}, err
	}
	closed := closedCapability(serverVersion, identity.DatabaseOID)
	handshake := BinderCapabilityHandshake{SelectedMode: BinderModeCatalogClosedV1, Closed: closed,
		Native: CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion,
			Mode: BinderModeNativeCV1, ServerVersionNum: serverVersion, ServerMajor: serverVersion / 10000,
			DatabaseOID: identity.DatabaseOID}}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return BinderCapabilityHandshake{}, err
	}
	var available, availableVersion, installed, installedVersion bool
	const extensionSQL = `SELECT
  EXISTS(SELECT 1 FROM pg_catalog.pg_available_extensions WHERE name='agentsql_binder'),
	EXISTS(SELECT 1 FROM pg_catalog.pg_available_extension_versions WHERE name='agentsql_binder' AND version='0.4'),
	EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder'),
	EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder' AND extversion='0.4')`
	if err := tx.QueryRow(ctx, extensionSQL).Scan(&available, &availableVersion, &installed, &installedVersion); err != nil {
		if ctx.Err() != nil {
			return BinderCapabilityHandshake{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		// Extension discovery is native-only evidence. A compatible kernel may
		// omit or restrict these catalogs while still satisfying every closed
		// catalog self-test, so do not turn that into a closed-mode failure.
		handshake.NativeHealth = "AUTH_BINDER_CAPABILITY_MISMATCH"
		return handshake, nil
	}
	handshake.NativeFilesAvailable = available
	handshake.NativeInstalled = installed
	if !available || !installed {
		handshake.NativeHealth = BinderCodeModeRequired
		return handshake, nil
	}
	if !availableVersion || !installedVersion {
		handshake.NativeHealth = "AUTH_BINDER_CAPABILITY_MISMATCH"
		return handshake, nil
	}
	capability, capabilityErr := readPostgresBinderCapability(ctx, tx, budget)
	if capabilityErr != nil || !nativeCapabilityMatches(capability, expectation) {
		handshake.NativeHealth = "AUTH_BINDER_CAPABILITY_MISMATCH"
		return handshake, nil
	}
	native := CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion,
		Mode: BinderModeNativeCV1, Available: true, ServerVersionNum: serverVersion,
		ServerMajor: capability.ServerMajor, DatabaseOID: identity.DatabaseOID, ABI: capability.ABI,
		ExtensionVersion: capability.ExtensionVersion, BuildHash: capability.BuildHash,
		ExtensionHash: capability.ExtensionHash, NodeManifestHash: capability.NodeManifestHash,
		AllowlistHash: capability.AllowlistHash,
		Capabilities:  []string{"analyzed_tree", "exact_expression_oids", "ordinary_view_lineage", "matview_typed_lineage", "matview_relkind_lock", "prepared_generation"},
		Precision:     []PrecisionDeclaration{{Name: "semantic_facts", Exact: true}, {Name: "plan_generation", Exact: true}}}
	native.Digest, err = native.CanonicalDigest()
	if err != nil {
		handshake.NativeHealth = "AUTH_BINDER_CAPABILITY_MISMATCH"
		return handshake, nil
	}
	handshake.Native = native
	handshake.NativeHealth = "healthy"
	handshake.SelectedMode = BinderModeNativeCV1
	return handshake, nil
}

func nativeCapabilityMatches(value PostgresBinderCapability, expected NativeCapabilityExpectation) bool {
	if expected.ABI == "" || expected.ServerMajor == 0 || expected.ExtensionVersion == "" || expected.BuildHash == "" || expected.ExtensionHash == "" ||
		expected.NodeManifestHash == "" || expected.AllowlistHash == "" {
		return false
	}
	return value.ABI == expected.ABI && value.ServerMajor == expected.ServerMajor &&
		value.ExtensionVersion == expected.ExtensionVersion && value.BuildHash == expected.BuildHash &&
		value.ExtensionHash == expected.ExtensionHash && value.NodeManifestHash == expected.NodeManifestHash &&
		value.AllowlistHash == expected.AllowlistHash && value.Matview
}

// PostgresBinderNativeExpectation returns the immutable artifact attestation
// embedded in this gateway build. It intentionally does not accept values from
// the datasource or installer as authority.
func PostgresBinderNativeExpectation(major int) (NativeCapabilityExpectation, bool) {
	if major < 14 || major > 18 {
		return NativeCapabilityExpectation{}, false
	}
	suffix := strconv.Itoa(major)
	return NativeCapabilityExpectation{
		ABI:              postgresBinderABI,
		ServerMajor:      major,
		ExtensionVersion: "0.4-s3m",
		BuildHash:        binderArtifactHash("agentsql-binder-build-v2-pg" + suffix),
		ExtensionHash:    binderArtifactHash("agentsql-binder-source-v2-pg" + suffix),
		NodeManifestHash: binderArtifactHash("query-rte-var-join-matview-v2-pg" + suffix),
		AllowlistHash:    binderArtifactHash("builtin-exact-oids-v1-pg" + suffix),
	}, true
}

func binderArtifactHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func closedCapability(serverVersion int, databaseOID uint32) CapabilityAttestation {
	value := CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion,
		Mode: BinderModeCatalogClosedV1, Available: true, ServerVersionNum: serverVersion,
		ServerMajor: serverVersion / 10000, DatabaseOID: databaseOID,
		GrammarManifestHash: closedGrammarManifestHash, CatalogQueryPackHash: closedQueryPackHash,
		CanonicalEncoderVersion: closedEncoderVersion, BuiltinManifestHash: closedBuiltinManifestHash,
		Capabilities: []string{"schema_qualified_base_relation", "catalog_identity", "ordered_oid_locks", "prepare_lock_crosscheck",
			"closed_select_direct", "closed_select_self_join", "closed_select_inner_join", "closed_select_left_join",
			"closed_select_expression", "closed_select_in_exists", "closed_dml_insert_values", "closed_dml_update_where",
			"closed_dml_delete_where", "closed_dml_implicit_null"},
		Precision: []PrecisionDeclaration{{Name: "base_relation_identity", Exact: true},
			{Name: "column_identity", Exact: true}, {Name: "implicit_object_negative_gate", Exact: true},
			{Name: "plan_generation", Exact: false, Note: "unavailable; request-local PREPARE is never identity authority"}}}
	value.Digest, _ = value.CanonicalDigest()
	return value
}

// DiscoverClosedCatalog is the candidate transaction. Its result is a hint;
// PrepareClosedCatalog always re-resolves and re-fingerprints under locks.
func (executor *PostgresExecutor) DiscoverClosedCatalog(ctx context.Context, refs []ClosedRelationRef, budget PostgresCatalogBudget) (ClosedCatalogCandidate, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil {
		return ClosedCatalogCandidate{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	refs, err := normalizeClosedRefs(refs)
	if err != nil {
		return ClosedCatalogCandidate{}, err
	}
	businessRank, err := lockrank.Acquire(ctx, lockrank.Business)
	if err != nil {
		return ClosedCatalogCandidate{}, err
	}
	defer businessRank.Release()
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return ClosedCatalogCandidate{}, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL closed candidate", err)
	}
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		connection.Release()
		return ClosedCatalogCandidate{}, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL closed candidate", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_ = cleanupPostgresPreparedConnection(executor, connection, tx, "")
		}
	}()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return ClosedCatalogCandidate{}, err
	}
	identity, serverVersion, err := readClosedSessionIdentity(ctx, tx, budget)
	if err != nil {
		return ClosedCatalogCandidate{}, err
	}
	frame, err := scanClosedCatalog(ctx, tx, refs, budget)
	if err != nil {
		return ClosedCatalogCandidate{}, err
	}
	identity.CatalogDigest = frame.Fingerprint
	candidate := ClosedCatalogCandidate{Refs: refs, Frame: frame, Identity: identity,
		Digest: frame.Fingerprint, Capability: closedCapability(serverVersion, frame.DatabaseOID)}
	cleanupErr := cleanupPostgresPreparedConnection(executor, connection, tx, "")
	cleaned = true
	if cleanupErr != nil {
		return ClosedCatalogCandidate{}, postgresDatabaseError(ctx, DBStageRollback, "rollback PostgreSQL closed candidate", cleanupErr)
	}
	return candidate, nil
}

type PostgresClosedPrepared struct {
	mu           sync.Mutex
	executor     *PostgresExecutor
	connection   *pgxpool.Conn
	tx           pgx.Tx
	name         string
	refs         []ClosedRelationRef
	fpre         PostgresCatalogFrame
	preseal      PreSeal
	closed       bool
	executed     bool
	businessRank *lockrank.Lease
}

func (prepared *PostgresClosedPrepared) Program() BoundProgram {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.preseal.Program
}

func (prepared *PostgresClosedPrepared) Fpre() PostgresCatalogFrame {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.fpre
}

// Execute runs only the request-owned prepared SELECT created by the closed
// binder. It accepts no SQL and keeps the catalog locks until VerifyPost.
func (prepared *PostgresClosedPrepared) Execute(ctx context.Context, rowLimit int) (model.QueryResult, error) {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed || prepared.executed || prepared.tx == nil || rowLimit <= 0 {
		return model.QueryResult{}, catalogAuthError("AUTH_PREPARED_STATE_INVALID")
	}
	result, err := prepared.executor.queryWithRunner(ctx, prepared.tx, `EXECUTE `+quoteInternalPreparedName(prepared.name), rowLimit)
	if err != nil {
		return model.QueryResult{}, err
	}
	prepared.executed = true
	return result, nil
}

// PrepareClosedCatalog performs ordered locks, locked re-resolution, Fpre and
// standard server PREPARE. It never calls EXPLAIN; execution is exposed only
// through the request-owned, SQL-free PostgresClosedPrepared capability.
func (executor *PostgresExecutor) PrepareClosedCatalog(ctx context.Context, rawSQL string, candidate ClosedCatalogCandidate, facts SemanticFacts, budget PostgresCatalogBudget) (*PostgresClosedPrepared, error) {
	return executor.prepareClosedCatalogResolved(ctx, rawSQL, candidate, budget,
		func(context.Context, pgx.Tx, PostgresCatalogFrame, PostgresCatalogBudget) (SemanticFacts, error) {
			return facts, nil
		})
}

type closedFactsResolver func(context.Context, pgx.Tx, PostgresCatalogFrame, PostgresCatalogBudget) (SemanticFacts, error)

func (executor *PostgresExecutor) prepareClosedCatalogResolved(ctx context.Context, rawSQL string, candidate ClosedCatalogCandidate,
	budget PostgresCatalogBudget, resolve closedFactsResolver) (*PostgresClosedPrepared, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil || resolve == nil || strings.TrimSpace(rawSQL) == "" || candidate.Digest == "" {
		return nil, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	businessRank, err := lockrank.Acquire(ctx, lockrank.Business)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			businessRank.Release()
		}
	}()
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return nil, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL closed execution", err)
	}
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		connection.Release()
		return nil, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL closed execution", err)
	}
	name := ""
	defer func() {
		if !transferred {
			_ = cleanupPostgresPreparedConnection(executor, connection, tx, name)
		}
	}()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return nil, err
	}
	identity, _, err := readClosedSessionIdentity(ctx, tx, budget)
	if err != nil {
		return nil, err
	}
	if !sameClosedIdentity(candidate.Identity, identity) {
		return nil, NewIdentityDriftFailure()
	}
	if err := lockClosedRelations(ctx, tx, candidate.Frame.Relations, budget); err != nil {
		return nil, err
	}
	fpre, err := scanClosedCatalog(ctx, tx, candidate.Refs, budget)
	if err != nil {
		return nil, err
	}
	if fpre.Fingerprint != candidate.Frame.Fingerprint || !sameFrameRelationOIDs(fpre, candidate.Frame) {
		return nil, NewCatalogFailure("AUTH_CATALOG_RACE")
	}
	facts, err := resolve(ctx, tx, fpre, budget)
	if err != nil {
		return nil, err
	}
	name, err = randomClosedPreparedName()
	if err != nil {
		return nil, catalogAuthError("AUTH_DATABASE_ERROR")
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "PREPARE "+quoteInternalPreparedName(name)+" AS "+rawSQL); err != nil {
		return nil, NewPrecisionFailure(BinderCodeModeRequired)
	}
	actual, err := readPostgresUserLocks(ctx, tx, budget)
	if err != nil {
		return nil, err
	}
	expected := frameRelationOIDs(fpre)
	if !sameOIDSet(actual, expected) {
		return nil, binderFailure(BinderFailureCatalog, "AUTH_BIND_CLOSURE_MISMATCH")
	}
	identityAfter, _, err := readClosedSessionIdentity(ctx, tx, budget)
	if err != nil {
		return nil, err
	}
	if !sameClosedIdentity(identity, identityAfter) {
		return nil, NewIdentityDriftFailure()
	}
	facts, err = canonicalizeClosedFacts(facts, identityAfter, fpre)
	if err != nil {
		return nil, err
	}
	factsDigest, err := facts.Digest()
	if err != nil {
		return nil, err
	}
	program := BoundProgram{Mode: BinderModeCatalogClosedV1, Facts: facts, SemanticFactsDigest: factsDigest,
		EngineEvidenceDigest: closedEngineEvidenceDigest(factsDigest, fpre.Fingerprint), Capability: candidate.Capability,
		LockExpectation: makeLockExpectations(expected), CatalogRoots: expected, ExecutionHandle: name}
	preseal := PreSeal{Program: program, CatalogPreDigest: fpre.Fingerprint,
		ActualLockDigest: digestOIDs(actual), IdentityDigest: digestSemanticIdentity(identityAfter)}
	prepared := &PostgresClosedPrepared{executor: executor, connection: connection, tx: tx, name: name,
		refs: append([]ClosedRelationRef(nil), candidate.Refs...), fpre: fpre, preseal: preseal, businessRank: businessRank}
	if err := executor.registerResource(prepared); err != nil {
		return nil, err
	}
	transferred = true
	return prepared, nil
}

// canonicalizeClosedFacts is the single canonicalization boundary shared by
// candidate preparation and transaction-bound re-resolution. It deliberately
// excludes request-local handles and transaction identifiers while binding the
// stable execution identity and the locked catalog fingerprint.
func canonicalizeClosedFacts(facts SemanticFacts, identity SemanticIdentity, frame PostgresCatalogFrame) (SemanticFacts, error) {
	datasourceIdentity := facts.Identity.DatasourceIdentity
	// DatasourceIdentity is a control-plane routing/cache namespace. It is not
	// PostgreSQL catalog evidence and direct PrepareClosedCatalog callers may
	// legitimately omit it. The locked catalog fingerprint, in contrast, is
	// required to seal every relation and remains fail-closed.
	if frame.Fingerprint == "" {
		return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	facts.Schema, facts.SchemaVersion = SemanticFactsSchemaID, SemanticFactsVersion
	facts.Identity = identity
	facts.Identity.DatasourceIdentity = datasourceIdentity
	facts.Identity.CatalogDigest = frame.Fingerprint
	// A closed statement is always freshly prepared for this request. Generation
	// one is a lifecycle identity, not a claim that SQL exposes PostgreSQL's
	// private plan invalidation counter.
	facts.Identity.PlanGeneration = 1
	if err := sealFactsWithFrame(&facts, frame); err != nil {
		return SemanticFacts{}, err
	}
	return facts, nil
}

func closedEngineEvidenceDigest(factsDigest, catalogFingerprint string) string {
	engine := sha256.Sum256([]byte(closedEncoderVersion + "\x00" + factsDigest + "\x00" + catalogFingerprint))
	return "closed-ast-catalog:" + hex.EncodeToString(engine[:])
}

func randomClosedPreparedName() (string, error) {
	name, err := randomPreparedName()
	if err != nil {
		return "", err
	}
	return "asqlclosed_" + strings.TrimPrefix(name, "agentsql_"), nil
}

func (prepared *PostgresClosedPrepared) VerifyPost(ctx context.Context, budget PostgresCatalogBudget) (BinderProof, error) {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	// Closed SELECT is an executable request capability, so its post-proof is
	// valid only after that capability has run. Closed DML is prepared in this
	// read-only catalog transaction solely to produce a bind proof; B5 executes
	// it later in its separately sealed transaction capability.
	requiresExecution := prepared.preseal.Program.Facts.StatementClass == BinderStatementSelect
	if prepared.closed || (requiresExecution && !prepared.executed) || prepared.tx == nil || budget == nil {
		return BinderProof{}, catalogAuthError("AUTH_PREPARED_STATE_INVALID")
	}
	fpost, err := scanClosedCatalog(ctx, prepared.tx, prepared.refs, budget)
	if err != nil {
		return BinderProof{}, err
	}
	actual, err := readPostgresUserLocks(ctx, prepared.tx, budget)
	if err != nil {
		return BinderProof{}, err
	}
	identity, _, err := readClosedSessionIdentity(ctx, prepared.tx, budget)
	if err != nil {
		return BinderProof{}, err
	}
	if digestSemanticIdentity(identity) != prepared.preseal.IdentityDigest {
		return BinderProof{}, NewIdentityDriftFailure()
	}
	return NewFinalBinderProof(prepared.preseal, fpost, actual)
}

func (prepared *PostgresClosedPrepared) Close(ctx context.Context) error {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed {
		return nil
	}
	prepared.closed = true
	if prepared.executor != nil {
		defer prepared.executor.unregisterResource(prepared)
	}
	if prepared.businessRank != nil {
		defer prepared.businessRank.Release()
		prepared.businessRank = nil
	}
	if prepared.tx == nil || prepared.connection == nil {
		return nil
	}
	err := cleanupPostgresPreparedConnection(prepared.executor, prepared.connection, prepared.tx, prepared.name)
	prepared.tx = nil
	prepared.connection = nil
	return err
}

func (prepared *PostgresClosedPrepared) closeForExecutor(context.Context) error {
	return prepared.Close(context.Background())
}

func scanClosedCatalog(ctx context.Context, tx pgx.Tx, refs []ClosedRelationRef, budget PostgresCatalogBudget) (PostgresCatalogFrame, error) {
	if len(refs) == 0 {
		return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	if err := budget.ChargeRelations(len(refs)); err != nil {
		return PostgresCatalogFrame{}, err
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresCatalogFrame{}, err
	}
	schemas, names := refArrays(refs)
	const relationSQL = `WITH requested(schema_name,relation_name) AS (
  SELECT s.schema_name,n.relation_name
  FROM pg_catalog.unnest($1::text[]) WITH ORDINALITY s(schema_name,ord)
  JOIN pg_catalog.unnest($2::text[]) WITH ORDINALITY n(relation_name,ord) USING(ord)
)
SELECT current_setting('server_version_num')::int,d.oid,c.oid,n.oid,n.nspname,c.relname,
       c.relkind::text,c.relpersistence::text,c.relam,c.reloftype,c.relispartition,
       pg_catalog.format('%I.%I',n.nspname,c.relname)
FROM requested r
JOIN pg_catalog.pg_namespace n ON n.nspname=r.schema_name
JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid AND c.relname=r.relation_name
CROSS JOIN pg_catalog.pg_database d
WHERE d.datname=current_database()
ORDER BY c.oid`
	rows, err := tx.Query(ctx, relationSQL, schemas, names)
	if err != nil {
		return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	frame := PostgresCatalogFrame{Relations: make([]PostgresRelationIdentity, 0, len(refs))}
	seen := make(map[uint32]struct{}, len(refs))
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			rows.Close()
			return PostgresCatalogFrame{}, err
		}
		var relation PostgresRelationIdentity
		var kind, persistence string
		if err := rows.Scan(&frame.ServerVersion, &frame.DatabaseOID, &relation.OID, &relation.NamespaceOID,
			&relation.Schema, &relation.Name, &kind, &persistence, &relation.RelationAM, &relation.OfType,
			&relation.IsPartition, &relation.Qualified); err != nil || len(kind) != 1 || len(persistence) != 1 {
			rows.Close()
			return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		if _, duplicate := seen[relation.OID]; duplicate {
			rows.Close()
			return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		seen[relation.OID] = struct{}{}
		relation.DatabaseOID, relation.Kind, relation.Persistence = frame.DatabaseOID, kind[0], persistence[0]
		if relation.Kind == 'v' {
			rows.Close()
			return PostgresCatalogFrame{}, NewCapabilityFailure(BinderCodeModeRequired)
		}
		if relation.Kind != 'r' || relation.Persistence != 'p' || relation.IsPartition || relation.OfType != 0 ||
			relation.Schema == "pg_catalog" || relation.Schema == "information_schema" || strings.HasPrefix(relation.Schema, "pg_toast") {
			rows.Close()
			return PostgresCatalogFrame{}, NewPrecisionFailure("AUTH_RELATION_SHAPE_UNSUPPORTED")
		}
		if err := budget.ChargeCatalogBytes(relationRecordBytes(relation)); err != nil {
			rows.Close()
			return PostgresCatalogFrame{}, err
		}
		frame.Relations = append(frame.Relations, relation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	rows.Close()
	if len(frame.Relations) != len(refs) || frame.DatabaseOID == 0 || frame.ServerVersion <= 0 || frame.ServerVersion/10000 <= 0 {
		return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	oids := frameRelationOIDs(frame)
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresCatalogFrame{}, err
	}
	const columnSQL = `SELECT a.attrelid,a.attnum,a.attname,a.atttypid,a.atttypmod,a.attcollation,
 a.attnotnull,a.attidentity::text,a.attgenerated::text
FROM pg_catalog.pg_attribute a
WHERE a.attrelid=ANY($1::oid[]) AND a.attnum>0 AND NOT a.attisdropped
ORDER BY a.attrelid,a.attnum`
	rows, err = tx.Query(ctx, columnSQL, oidArrayLiteral(oids))
	if err != nil {
		return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			rows.Close()
			return PostgresCatalogFrame{}, err
		}
		if err := budget.ChargeColumnMetadata(1); err != nil {
			rows.Close()
			return PostgresCatalogFrame{}, err
		}
		var column PostgresColumnIdentity
		var identity, generated string
		if err := rows.Scan(&column.RelationOID, &column.Attnum, &column.Name, &column.TypeOID, &column.Typmod,
			&column.Collation, &column.NotNull, &identity, &generated); err != nil || column.Attnum <= 0 || len(identity) > 1 || len(generated) > 1 {
			rows.Close()
			return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		if identity != "" {
			column.Identity = identity[0]
		}
		if generated != "" {
			column.Generated = generated[0]
		}
		if err := budget.ChargeCatalogBytes(columnRecordBytes(column)); err != nil {
			rows.Close()
			return PostgresCatalogFrame{}, err
		}
		frame.Columns = append(frame.Columns, column)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PostgresCatalogFrame{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	rows.Close()
	if err := readClosedDependencies(ctx, tx, oids, budget, &frame); err != nil {
		return PostgresCatalogFrame{}, err
	}
	if err := rejectClosedImplicitObjects(ctx, tx, oids, budget); err != nil {
		return PostgresCatalogFrame{}, err
	}
	frame.Fingerprint = fingerprintPostgresCatalog(frame)
	return frame, nil
}

func readClosedDependencies(ctx context.Context, tx pgx.Tx, oids []uint32, budget PostgresCatalogBudget, frame *PostgresCatalogFrame) error {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return err
	}
	const query = `SELECT classid,objid,objsubid,refclassid,refobjid,refobjsubid,deptype::text
FROM pg_catalog.pg_depend WHERE objid=ANY($1::oid[]) OR refobjid=ANY($1::oid[])
ORDER BY classid,objid,objsubid,refclassid,refobjid,refobjsubid,deptype`
	rows, err := tx.Query(ctx, query, oidArrayLiteral(oids))
	if err != nil {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	defer rows.Close()
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			return err
		}
		if err := budget.ChargeEdges(1); err != nil {
			return err
		}
		var value PostgresDependency
		var kind string
		if err := rows.Scan(&value.ClassID, &value.ObjectID, &value.ObjectSubID, &value.RefClassID, &value.RefObjectID, &value.RefObjectSubID, &kind); err != nil || len(kind) != 1 {
			return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		value.Type = kind[0]
		frame.Dependencies = append(frame.Dependencies, value)
	}
	if err := rows.Err(); err != nil {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	return nil
}

func rejectClosedImplicitObjects(ctx context.Context, tx pgx.Tx, oids []uint32, budget PostgresCatalogBudget) error {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return err
	}
	const query = `WITH target(relid) AS (SELECT pg_catalog.unnest($1::oid[])), findings(owner_oid,kind,object_oid) AS (
 SELECT t.tgrelid,'trigger',t.oid FROM pg_catalog.pg_trigger t JOIN target x ON x.relid=t.tgrelid WHERE NOT t.tgisinternal
 UNION ALL SELECT r.ev_class,'rule',r.oid FROM pg_catalog.pg_rewrite r JOIN target x ON x.relid=r.ev_class
 UNION ALL SELECT c.oid,'rls',COALESCE(p.oid,c.oid) FROM pg_catalog.pg_class c JOIN target x ON x.relid=c.oid LEFT JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid WHERE c.relrowsecurity OR c.relforcerowsecurity OR p.oid IS NOT NULL
 UNION ALL SELECT i.inhrelid,'inherits_parent',i.inhparent FROM pg_catalog.pg_inherits i JOIN target x ON x.relid=i.inhrelid
 UNION ALL SELECT i.inhparent,'inherits_child',i.inhrelid FROM pg_catalog.pg_inherits i JOIN target x ON x.relid=i.inhparent
 UNION ALL SELECT p.partrelid,'partitioned_table',p.partrelid FROM pg_catalog.pg_partitioned_table p JOIN target x ON x.relid=p.partrelid
 UNION ALL SELECT q.conrelid,'foreign_key',q.oid FROM pg_catalog.pg_constraint q JOIN target x ON q.conrelid=x.relid OR q.confrelid=x.relid WHERE q.contype='f'
 UNION ALL SELECT q.conrelid,'check',q.oid FROM pg_catalog.pg_constraint q JOIN target x ON q.conrelid=x.relid WHERE q.contype IN ('c','x')
 UNION ALL SELECT d.adrelid,CASE WHEN a.attgenerated<>'' THEN 'generated' ELSE 'default' END,d.oid FROM pg_catalog.pg_attrdef d JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum JOIN target x ON x.relid=d.adrelid
 UNION ALL SELECT a.attrelid,CASE WHEN a.attgenerated<>'' THEN 'generated' ELSE 'identity' END,a.attrelid FROM pg_catalog.pg_attribute a JOIN target x ON x.relid=a.attrelid WHERE a.attnum>0 AND NOT a.attisdropped AND (a.attidentity<>'' OR a.attgenerated<>'')
 UNION ALL SELECT i.indrelid,CASE WHEN i.indexprs IS NOT NULL THEN 'expression_index' ELSE 'partial_index' END,i.indexrelid FROM pg_catalog.pg_index i JOIN target x ON x.relid=i.indrelid WHERE i.indexprs IS NOT NULL OR i.indpred IS NOT NULL
) SELECT owner_oid,kind,object_oid FROM findings ORDER BY 1,2,3 LIMIT 1`
	var owner, object uint32
	var kind string
	err := tx.QueryRow(ctx, query, oidArrayLiteral(oids)).Scan(&owner, &kind, &object)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	if err := budget.ChargeCatalogRows(1); err != nil {
		return err
	}
	if strings.HasPrefix(kind, "inherit") || kind == "partitioned_table" {
		return NewPrecisionFailure("AUTH_RELATION_SHAPE_UNSUPPORTED")
	}
	return NewPrecisionFailure("AUTH_IMPLICIT_OBJECT_UNSUPPORTED")
}

func lockClosedRelations(ctx context.Context, tx pgx.Tx, relations []PostgresRelationIdentity, budget PostgresCatalogBudget) error {
	ordered := append([]PostgresRelationIdentity(nil), relations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].OID < ordered[j].OID })
	for _, relation := range ordered {
		if relation.OID == 0 || relation.Kind != 'r' {
			return NewPrecisionFailure("AUTH_RELATION_SHAPE_UNSUPPORTED")
		}
		if err := budget.ChargeCatalogRoundTrips(1); err != nil {
			return err
		}
		var qualified string
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.format('%I.%I',n.nspname,c.relname) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1 AND c.relkind='r' AND c.relpersistence='p'`, relation.OID).Scan(&qualified); err != nil {
			return NewCatalogFailure("AUTH_CATALOG_RACE")
		}
		if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified+" IN ACCESS SHARE MODE"); err != nil {
			return NewLockFailure()
		}
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_locks WHERE pid=pg_backend_pid() AND locktype='relation' AND relation=$1 AND mode='AccessShareLock' AND granted)`, relation.OID).Scan(&locked); err != nil || !locked {
			return NewLockFailure()
		}
	}
	return nil
}

func readClosedSessionIdentity(ctx context.Context, tx pgx.Tx, budget PostgresCatalogBudget) (SemanticIdentity, int, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return SemanticIdentity{}, 0, err
	}
	var value SemanticIdentity
	var version int
	const query = `SELECT pg_catalog.set_config('search_path','pg_catalog',true),
 current_setting('server_version_num')::int,d.oid,session_user,current_user,r.oid,current_setting('search_path')
FROM pg_catalog.pg_database d JOIN pg_catalog.pg_roles r ON r.rolname=current_user
WHERE d.datname=current_database()`
	var ignored string
	if err := tx.QueryRow(ctx, query).Scan(&ignored, &version, &value.DatabaseOID, &value.SessionUser, &value.CurrentUser, &value.RoleOID, &value.FixedSearchPath); err != nil ||
		version == 0 || value.DatabaseOID == 0 || value.RoleOID == 0 || value.FixedSearchPath != "pg_catalog" {
		return SemanticIdentity{}, 0, NewIdentityDriftFailure()
	}
	sum := sha256.Sum256([]byte(value.FixedSearchPath))
	value.SearchPathDigest = hex.EncodeToString(sum[:])
	return value, version, nil
}

func closedCatalogSelfTest(ctx context.Context, tx pgx.Tx, budget PostgresCatalogBudget) error {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return err
	}
	var classOID, attrOID, dependOID, triggerOID, rewriteOID, policyOID, inheritOID, partitionOID uint32
	var requiredColumns int
	const query = `WITH required(relid,attname) AS (VALUES
 ('pg_catalog.pg_class'::regclass,'relam'),
 ('pg_catalog.pg_class'::regclass,'reloftype'),
 ('pg_catalog.pg_class'::regclass,'relispartition'),
 ('pg_catalog.pg_class'::regclass,'relrowsecurity'),
 ('pg_catalog.pg_class'::regclass,'relforcerowsecurity'),
 ('pg_catalog.pg_attribute'::regclass,'attidentity'),
 ('pg_catalog.pg_attribute'::regclass,'attgenerated'),
 ('pg_catalog.pg_partitioned_table'::regclass,'partrelid')
) SELECT 'pg_catalog.pg_class'::regclass::oid,'pg_catalog.pg_attribute'::regclass::oid,
 'pg_catalog.pg_depend'::regclass::oid,'pg_catalog.pg_trigger'::regclass::oid,
 'pg_catalog.pg_rewrite'::regclass::oid,'pg_catalog.pg_policy'::regclass::oid,
	 'pg_catalog.pg_inherits'::regclass::oid,'pg_catalog.pg_partitioned_table'::regclass::oid,
	 (SELECT count(*) FROM required r JOIN pg_catalog.pg_attribute a ON a.attrelid=r.relid AND a.attname=r.attname AND a.attnum>0 AND NOT a.attisdropped)`
	if err := tx.QueryRow(ctx, query).Scan(&classOID, &attrOID, &dependOID, &triggerOID, &rewriteOID, &policyOID, &inheritOID, &partitionOID, &requiredColumns); err != nil ||
		classOID == 0 || attrOID == 0 || dependOID == 0 || triggerOID == 0 || rewriteOID == 0 || policyOID == 0 || inheritOID == 0 || partitionOID == 0 {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	if requiredColumns != 8 {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	return nil
}

func normalizeClosedRefs(values []ClosedRelationRef) ([]ClosedRelationRef, error) {
	if len(values) == 0 {
		return nil, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	result := append([]ClosedRelationRef(nil), values...)
	for _, value := range result {
		if strings.TrimSpace(value.Schema) != value.Schema || strings.TrimSpace(value.Name) != value.Name || value.Schema == "" || value.Name == "" {
			return nil, NewPrecisionFailure(BinderCodeModeRequired)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Schema == result[j].Schema {
			return result[i].Name < result[j].Name
		}
		return result[i].Schema < result[j].Schema
	})
	for i := 1; i < len(result); i++ {
		if result[i] == result[i-1] {
			return nil, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
	}
	return result, nil
}

func sealFactsWithFrame(facts *SemanticFacts, frame PostgresCatalogFrame) error {
	if facts == nil {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	byOID := make(map[uint32]PostgresRelationIdentity, len(frame.Relations))
	for _, relation := range frame.Relations {
		byOID[relation.OID] = relation
	}
	if len(facts.Relations) == 0 {
		for _, relation := range frame.Relations {
			facts.Relations = append(facts.Relations, SemanticRelation{DatabaseOID: relation.DatabaseOID, RelationOID: relation.OID, NamespaceOID: relation.NamespaceOID, Schema: relation.Schema, Name: relation.Name, Kind: relation.Kind, Persistence: relation.Persistence, CatalogFingerprint: frame.Fingerprint})
		}
	}
	for index := range facts.Relations {
		relation, ok := byOID[facts.Relations[index].RelationOID]
		if !ok {
			return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		facts.Relations[index].DatabaseOID = relation.DatabaseOID
		facts.Relations[index].NamespaceOID = relation.NamespaceOID
		facts.Relations[index].Schema = relation.Schema
		facts.Relations[index].Name = relation.Name
		facts.Relations[index].Kind = relation.Kind
		facts.Relations[index].Persistence = relation.Persistence
		facts.Relations[index].CatalogFingerprint = frame.Fingerprint
	}
	columns := make(map[string]PostgresColumnIdentity, len(frame.Columns))
	for _, column := range frame.Columns {
		columns[closedColumnKey(column.RelationOID, column.Attnum)] = column
	}
	for index := range facts.ColumnUses {
		column, ok := columns[closedColumnKey(facts.ColumnUses[index].RelationOID, facts.ColumnUses[index].Attnum)]
		if !ok {
			return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		facts.ColumnUses[index].Name = column.Name
		facts.ColumnUses[index].TypeOID = column.TypeOID
		facts.ColumnUses[index].TypeModifier = column.Typmod
		facts.ColumnUses[index].CollationOID = column.Collation
		if facts.ColumnUses[index].Attnum <= 0 {
			return NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
		}
	}
	for index := range facts.WriteTargets {
		if facts.WriteTargets[index].Kind == b5dml.WriteTargetRow {
			continue
		}
		column, ok := columns[closedColumnKey(facts.WriteTargets[index].RelationOID, facts.WriteTargets[index].Attnum)]
		if !ok || facts.WriteTargets[index].Attnum <= 0 {
			return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		facts.WriteTargets[index].Name = column.Name
		facts.WriteTargets[index].TypeOID = column.TypeOID
		facts.WriteTargets[index].TypeModifier = column.Typmod
		facts.WriteTargets[index].CollationOID = column.Collation
	}
	return nil
}

func refArrays(refs []ClosedRelationRef) ([]string, []string) {
	schemas := make([]string, len(refs))
	names := make([]string, len(refs))
	for i, v := range refs {
		schemas[i] = v.Schema
		names[i] = v.Name
	}
	return schemas, names
}
func frameRelationOIDs(frame PostgresCatalogFrame) []uint32 {
	result := make([]uint32, 0, len(frame.Relations))
	for _, v := range frame.Relations {
		result = append(result, v.OID)
	}
	return uniqueSortedOIDs(result)
}
func sameFrameRelationOIDs(a, b PostgresCatalogFrame) bool {
	return sameOIDSet(frameRelationOIDs(a), frameRelationOIDs(b))
}
func sameClosedIdentity(a, b SemanticIdentity) bool {
	return a.DatabaseOID == b.DatabaseOID && a.SessionUser == b.SessionUser && a.CurrentUser == b.CurrentUser && a.RoleOID == b.RoleOID && a.FixedSearchPath == b.FixedSearchPath && a.SearchPathDigest == b.SearchPathDigest
}
func digestSemanticIdentity(v SemanticIdentity) string {
	h := sha256.New()
	writeSemanticIdentity(h, v)
	return hex.EncodeToString(h.Sum(nil))
}
func closedColumnKey(oid uint32, attnum int16) string {
	return strconv.FormatUint(uint64(oid), 10) + ":" + strconv.Itoa(int(attnum))
}
func makeLockExpectations(oids []uint32) []RelationLockExpectation {
	result := make([]RelationLockExpectation, len(oids))
	for i, oid := range oids {
		result[i] = RelationLockExpectation{RelationOID: oid, Mode: "AccessShareLock"}
	}
	return result
}

type ClosedCacheKey struct {
	DatasourceIdentity string
	DatabaseOID        uint32
	ServerMajor        int
	RoleOID            uint32
	SearchPathDigest   string
	SQLDigest          string
	CapabilityDigest   string
	CatalogFingerprint string
}

type ClosedASTCacheEntry struct {
	Version   string
	ASTDigest string
	Candidate ClosedCatalogCandidate
	Facts     SemanticFacts
	sequence  uint64
}

// ClosedASTCache stores only candidate hints. Callers must still use
// PrepareClosedCatalog, which performs locks, Fpre/Fpost and PREPARE checks.
type ClosedASTCache struct {
	mu      sync.Mutex
	max     int
	next    uint64
	entries map[ClosedCacheKey]ClosedASTCacheEntry
}

func NewClosedASTCache(maxEntries int) *ClosedASTCache {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &ClosedASTCache{max: maxEntries, entries: make(map[ClosedCacheKey]ClosedASTCacheEntry)}
}

func (cache *ClosedASTCache) Put(key ClosedCacheKey, entry ClosedASTCacheEntry) bool {
	if cache == nil || !validClosedCacheKey(key) || entry.ASTDigest == "" || entry.Candidate.Digest != key.CatalogFingerprint {
		return false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.next++
	entry.Version = closedCacheVersion
	entry.sequence = cache.next
	entry.Candidate = cloneClosedCandidate(entry.Candidate)
	entry.Facts = cloneSemanticFacts(entry.Facts)
	if _, exists := cache.entries[key]; !exists && len(cache.entries) >= cache.max {
		var oldestKey ClosedCacheKey
		oldest := uint64(^uint64(0))
		for k, v := range cache.entries {
			if v.sequence < oldest {
				oldest = v.sequence
				oldestKey = k
			}
		}
		delete(cache.entries, oldestKey)
	}
	cache.entries[key] = entry
	return true
}

func (cache *ClosedASTCache) Get(key ClosedCacheKey) (ClosedASTCacheEntry, bool) {
	if cache == nil || !validClosedCacheKey(key) {
		return ClosedASTCacheEntry{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.entries[key]
	if !ok || value.Version != closedCacheVersion {
		return ClosedASTCacheEntry{}, false
	}
	value.Candidate = cloneClosedCandidate(value.Candidate)
	value.Facts = cloneSemanticFacts(value.Facts)
	return value, true
}

func (cache *ClosedASTCache) InvalidateCatalog(fingerprint string) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key := range cache.entries {
		if key.CatalogFingerprint == fingerprint {
			delete(cache.entries, key)
		}
	}
}
func validClosedCacheKey(v ClosedCacheKey) bool {
	return v.DatasourceIdentity != "" && v.DatabaseOID != 0 && v.ServerMajor > 0 && v.RoleOID != 0 && v.SearchPathDigest != "" && v.SQLDigest != "" && v.CapabilityDigest != "" && v.CatalogFingerprint != ""
}
func cloneClosedCandidate(v ClosedCatalogCandidate) ClosedCatalogCandidate {
	v.Refs = append([]ClosedRelationRef(nil), v.Refs...)
	v.Frame.Relations = append([]PostgresRelationIdentity(nil), v.Frame.Relations...)
	v.Frame.Columns = append([]PostgresColumnIdentity(nil), v.Frame.Columns...)
	v.Frame.Dependencies = append([]PostgresDependency(nil), v.Frame.Dependencies...)
	v.Capability.Capabilities = append([]string(nil), v.Capability.Capabilities...)
	v.Capability.Precision = append([]PrecisionDeclaration(nil), v.Capability.Precision...)
	return v
}
func cloneSemanticFacts(v SemanticFacts) SemanticFacts {
	v.Relations = append([]SemanticRelation(nil), v.Relations...)
	v.ColumnUses = append([]SemanticColumnUse(nil), v.ColumnUses...)
	v.WriteTargets = append([]SemanticWriteTarget(nil), v.WriteTargets...)
	v.ViewExpansions = append([]SemanticViewExpansion(nil), v.ViewExpansions...)
	v.ObjectUses = append([]SemanticObjectUse(nil), v.ObjectUses...)
	return v
}
