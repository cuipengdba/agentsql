package businessdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	postgresBinderABI        = "agentsql-binder-4.2"
	postgresBinderMaxRetries = 1
	postgresCatalogTimeout   = time.Second
)

type PostgresBinderCapability struct {
	ABI              string               `json:"abi"`
	ServerMajor      int                  `json:"server_major"`
	ExtensionVersion string               `json:"extension_version"`
	BuildHash        string               `json:"build_hash,omitempty"`
	ExtensionHash    string               `json:"extension_hash"`
	NodeManifestHash string               `json:"node_manifest_hash"`
	AllowlistHash    string               `json:"allowlist_hash"`
	Matview          bool                 `json:"matview"`
	Allowlist        PostgresOIDAllowlist `json:"allowlist"`
}

// ProbePostgresBinderCapability performs the exact ABI/major/hash validation
// used by execution without preparing caller SQL or retaining a transaction.
func (executor *PostgresExecutor) ProbePostgresBinderCapability(ctx context.Context, budget PostgresCatalogBudget) (PostgresBinderCapability, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil {
		return PostgresBinderCapability{}, catalogAuthError("AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return PostgresBinderCapability{}, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL binder probe connection", err)
	}
	defer connection.Release()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PostgresBinderCapability{}, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL binder probe", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return PostgresBinderCapability{}, err
	}
	capability, err := readPostgresBinderCapability(ctx, tx, budget)
	if err != nil {
		return PostgresBinderCapability{}, err
	}
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return PostgresBinderCapability{}, postgresDatabaseError(ctx, DBStageRollback, "rollback PostgreSQL binder probe", err)
	}
	return capability, nil
}

type PostgresPreparedManifest struct {
	StatementName    string
	BackendPID       uint32
	TransactionID    string
	RoleOID          uint32
	RoleName         string
	SearchPath       string
	AnalyzedDigest   string
	DependencyDigest string
	PlanGeneration   uint64
	ReplanCount      uint64
	Invalidated      bool
	CommandType      string
	HasRecursive     bool
	HasModifyingCTE  bool
	NodeCount        int
	EdgeCount        int
	WorkUnits        int
	Relations        []PostgresBoundRelation
	Columns          []PostgresColumnUse
	Objects          []PostgresObjectUse
	Capability       PostgresBinderCapability
}

type PostgresBoundRelation struct {
	OID           uint32
	Kind          byte
	Inherits      bool
	RequiredPerms uint32
	CheckAsUser   uint32
	ViewDepth     int
	Path          string
}

type PostgresColumnUse struct {
	Site                string
	RelationOID         uint32
	Attnum              int16
	TypeOID             uint32
	CollationOID        uint32
	Usage               string
	QueryDepth          int
	ViewDepth           int
	ContributorGroup    int
	ContributorComplete bool
	WholeRow            bool
	ResultTypeOID       uint32
	ResultComposite     bool
}

type PostgresEnrollment struct {
	Manifest          PostgresPreparedManifest
	Catalog           PostgresCatalogFrame
	BinderFingerprint string
	Fingerprint       string
}

// PostgresPreparedSelect is a request-owned, same-backend prepared portal.
// Execute never accepts SQL and VerifyPost re-reads the extension seal and the
// catalog before the transaction may be completed by the S4 orchestrator.
type PostgresPreparedSelect struct {
	mu           sync.Mutex
	executor     *PostgresExecutor
	connection   *pgxpool.Conn
	tx           pgx.Tx
	name         string
	manifest     PostgresPreparedManifest
	fpre         PostgresCatalogFrame
	closed       bool
	executed     bool
	discard      bool
	businessRank *lockrank.Lease
}

func (executor *PostgresExecutor) EnrollPostgresSelect(ctx context.Context, rawSQL string, budget PostgresCatalogBudget) (PostgresEnrollment, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil {
		return PostgresEnrollment{}, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
	}
	manifest, frame, err := executor.discoverPostgresSelect(ctx, rawSQL, budget)
	if err != nil {
		return PostgresEnrollment{}, err
	}
	return enrollmentFrom(manifest, frame), nil
}

func (executor *PostgresExecutor) PrepareBoundPostgresSelect(ctx context.Context, rawSQL string, enrollment *PostgresEnrollment, budget PostgresCatalogBudget) (*PostgresPreparedSelect, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil {
		return nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
	}
	candidate, _, err := executor.discoverPostgresSelect(ctx, rawSQL, budget)
	if err != nil {
		return nil, err
	}
	for retry := 0; retry <= postgresBinderMaxRetries; retry++ {
		prepared, bindErr := executor.prepareLockedPostgresSelect(ctx, rawSQL, candidate, enrollment, budget)
		if bindErr == nil {
			return prepared, nil
		}
		var race *postgresCatalogRaceError
		if !errors.As(bindErr, &race) || retry == postgresBinderMaxRetries {
			return nil, bindErr
		}
		candidate, _, err = executor.discoverPostgresSelect(ctx, rawSQL, budget)
		if err != nil {
			return nil, err
		}
	}
	return nil, catalogAuthError("AUTH_CATALOG_RACE")
}

func (executor *PostgresExecutor) discoverPostgresSelect(ctx context.Context, rawSQL string, budget PostgresCatalogBudget) (PostgresPreparedManifest, PostgresCatalogFrame, error) {
	businessRank, err := lockrank.Acquire(ctx, lockrank.Business)
	if err != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, err
	}
	defer businessRank.Release()
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL binder connection", err)
	}
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		connection.Release()
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL candidate binder transaction", err)
	}
	name := ""
	cleaned := false
	defer func() {
		if !cleaned {
			_ = cleanupPostgresPreparedConnection(executor, connection, tx, name)
		}
	}()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, err
	}
	if err := setPostgresBinderSearchPath(ctx, tx); err != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, err
	}
	name, err = randomPreparedName()
	if err != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, catalogAuthError("AUTH_DATABASE_ERROR")
	}
	manifest, err := prepareAndReadPostgresManifest(ctx, tx, name, rawSQL, budget)
	if err != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, err
	}
	frame, err := scanPostgresCatalog(ctx, tx, manifestRelationOIDs(manifest), manifestViewDepths(manifest), manifest.Objects, manifest.Capability.Allowlist, budget)
	if err != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, err
	}
	cleanupErr := cleanupPostgresPreparedConnection(executor, connection, tx, name)
	cleaned = true
	if cleanupErr != nil {
		return PostgresPreparedManifest{}, PostgresCatalogFrame{}, postgresDatabaseError(ctx, DBStageRollback, "rollback PostgreSQL candidate binder transaction", cleanupErr)
	}
	return manifest, frame, nil
}

func (executor *PostgresExecutor) prepareLockedPostgresSelect(ctx context.Context, rawSQL string, candidate PostgresPreparedManifest, enrollment *PostgresEnrollment, budget PostgresCatalogBudget) (*PostgresPreparedSelect, error) {
	businessRank, err := lockrank.Acquire(ctx, lockrank.Business)
	if err != nil {
		return nil, err
	}
	rankTransferred := false
	defer func() {
		if !rankTransferred {
			businessRank.Release()
		}
	}()
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return nil, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL execution binder connection", err)
	}
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		connection.Release()
		return nil, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL execution binder transaction", err)
	}
	name := ""
	defer func() {
		if !rankTransferred {
			_ = cleanupPostgresPreparedConnection(executor, connection, tx, name)
		}
	}()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return nil, err
	}
	if err := setPostgresBinderSearchPath(ctx, tx); err != nil {
		return nil, err
	}
	if err := lockPostgresRelations(ctx, tx, candidate.Relations, budget); err != nil {
		return nil, err
	}
	name, err = randomPreparedName()
	if err != nil {
		return nil, catalogAuthError("AUTH_DATABASE_ERROR")
	}
	manifest, err := prepareAndReadPostgresManifest(ctx, tx, name, rawSQL, budget)
	if err != nil {
		return nil, err
	}
	actual, err := readPostgresUserLocks(ctx, tx, budget)
	if err != nil {
		return nil, err
	}
	if !samePostgresClosure(candidate.Relations, manifest.Relations) || !sameOIDSet(actual, manifestRelationOIDs(manifest)) {
		return nil, &postgresCatalogRaceError{reason: "AUTH_BIND_CLOSURE_MISMATCH"}
	}
	fpre, err := scanPostgresCatalog(ctx, tx, manifestRelationOIDs(manifest), manifestViewDepths(manifest), manifest.Objects, manifest.Capability.Allowlist, budget)
	if err != nil {
		return nil, err
	}
	current := enrollmentFrom(manifest, fpre)
	if enrollment != nil && enrollment.Fingerprint != current.Fingerprint {
		return nil, &postgresCatalogRaceError{reason: "AUTH_CATALOG_RACE"}
	}
	// Planning is deliberately delayed until after the exact-OID implicit
	// object gate. seal_prepared builds the one parameterless generic plan and
	// freezes its generation; every later invalidation/replan is rejected by
	// the extension's ProcessUtility hook before EXECUTE can run.
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT agentsql_catalog.seal_prepared($1)`, name); err != nil {
		return nil, postgresDatabaseError(ctx, DBStageMetadata, "seal PostgreSQL prepared statement", err)
	}
	sealed, err := readPostgresManifest(ctx, tx, name, manifest.Capability, budget)
	if err != nil {
		return nil, err
	}
	if manifest.AnalyzedDigest != sealed.AnalyzedDigest || manifest.DependencyDigest != sealed.DependencyDigest || !samePostgresClosure(manifest.Relations, sealed.Relations) || sealed.PlanGeneration == 0 {
		return nil, &postgresCatalogRaceError{reason: "AUTH_PREPARED_INVALIDATED"}
	}
	manifest = sealed
	prepared := &PostgresPreparedSelect{executor: executor, connection: connection, tx: tx, name: name,
		manifest: manifest, fpre: fpre, businessRank: businessRank}
	if err := executor.registerResource(prepared); err != nil {
		return nil, err
	}
	rankTransferred = true
	return prepared, nil
}

func prepareAndReadPostgresManifest(ctx context.Context, tx pgx.Tx, name, rawSQL string, budget PostgresCatalogBudget) (PostgresPreparedManifest, error) {
	if strings.TrimSpace(rawSQL) == "" {
		return PostgresPreparedManifest{}, catalogAuthError("AUTH_EXPRESSION_IDENTITY_UNSUPPORTED")
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresPreparedManifest{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT agentsql_catalog.prepare($1,$2)`, name, rawSQL); err != nil {
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "prepare PostgreSQL analyzed statement", err)
	}
	capability, err := readPostgresBinderCapability(ctx, tx, budget)
	if err != nil {
		return PostgresPreparedManifest{}, err
	}
	manifest, err := readPostgresManifest(ctx, tx, name, capability, budget)
	if err != nil {
		return PostgresPreparedManifest{}, err
	}
	if err := validatePostgresManifest(manifest, budget); err != nil {
		return PostgresPreparedManifest{}, err
	}
	if err := validatePostgresObjectAllowlist(manifest.Objects, capability.Allowlist); err != nil {
		return PostgresPreparedManifest{}, err
	}
	return manifest, nil
}

func setPostgresBinderSearchPath(ctx context.Context, tx pgx.Tx) error {
	if ctx == nil || tx == nil {
		return NewIdentityDriftFailure()
	}
	// Session hardening is transaction setup, not a catalog read. Charging it
	// against the catalog round-trip budget would make the same manifest cost
	// differently solely because fixed-path resolution is enabled.
	var path string
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.set_config('search_path','pg_catalog',true)`).Scan(&path); err != nil || path != "pg_catalog" {
		return NewIdentityDriftFailure()
	}
	return nil
}

func readPostgresBinderCapability(ctx context.Context, tx pgx.Tx, budget PostgresCatalogBudget) (PostgresBinderCapability, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresBinderCapability{}, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT agentsql_catalog.capabilities()`).Scan(&raw); err != nil {
		return PostgresBinderCapability{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL binder capability", err)
	}
	if err := budget.ChargeBinderBytes(len(raw)); err != nil {
		return PostgresBinderCapability{}, err
	}
	var capability PostgresBinderCapability
	if err := json.Unmarshal(raw, &capability); err != nil {
		return PostgresBinderCapability{}, catalogAuthError("AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	expected, ok := PostgresBinderNativeExpectation(capability.ServerMajor)
	if !ok || !capability.Matview || !nativeCapabilityMatches(capability, expected) {
		return PostgresBinderCapability{}, catalogAuthError("AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	return capability, nil
}

func readPostgresManifest(ctx context.Context, tx pgx.Tx, name string, capability PostgresBinderCapability, budget PostgresCatalogBudget) (PostgresPreparedManifest, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresPreparedManifest{}, err
	}
	manifest := PostgresPreparedManifest{Capability: capability}
	const sql = `SELECT statement_name,backend_pid,transaction_id,role_oid,role_name,search_path,
 analyzed_digest,dependency_digest,plan_generation,replan_count,invalidated,command_type,
 has_recursive,has_modifying_cte,node_count,edge_count,work_units
FROM agentsql_catalog.prepared_manifest($1)`
	if err := tx.QueryRow(ctx, sql, name).Scan(&manifest.StatementName, &manifest.BackendPID, &manifest.TransactionID, &manifest.RoleOID, &manifest.RoleName, &manifest.SearchPath, &manifest.AnalyzedDigest, &manifest.DependencyDigest, &manifest.PlanGeneration, &manifest.ReplanCount, &manifest.Invalidated, &manifest.CommandType, &manifest.HasRecursive, &manifest.HasModifyingCTE, &manifest.NodeCount, &manifest.EdgeCount, &manifest.WorkUnits); err != nil {
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL prepared manifest", err)
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresPreparedManifest{}, err
	}
	rows, err := tx.Query(ctx, `SELECT relation_oid,relkind::text,inh,required_perms,check_as_user,view_depth,node_path FROM agentsql_catalog.prepared_relations($1) ORDER BY relation_oid,node_path`, name)
	if err != nil {
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL prepared relations", err)
	}
	for rows.Next() {
		var relation PostgresBoundRelation
		var kind string
		if err := rows.Scan(&relation.OID, &kind, &relation.Inherits, &relation.RequiredPerms, &relation.CheckAsUser, &relation.ViewDepth, &relation.Path); err != nil {
			rows.Close()
			return PostgresPreparedManifest{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
		if len(kind) != 1 {
			rows.Close()
			return PostgresPreparedManifest{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
		relation.Kind = kind[0]
		manifest.Relations = append(manifest.Relations, relation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL prepared relations", err)
	}
	rows.Close()
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresPreparedManifest{}, err
	}
	rows, err = tx.Query(ctx, `SELECT site,relation_oid,attnum,type_oid,collation_oid,usage,query_depth,view_depth,contributor_group,contributor_complete,is_whole_row,result_type_oid,result_is_composite FROM agentsql_catalog.prepared_vars($1) ORDER BY site,contributor_group,relation_oid,attnum`, name)
	if err != nil {
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL prepared variables", err)
	}
	for rows.Next() {
		var use PostgresColumnUse
		if err := rows.Scan(&use.Site, &use.RelationOID, &use.Attnum, &use.TypeOID, &use.CollationOID, &use.Usage, &use.QueryDepth, &use.ViewDepth, &use.ContributorGroup, &use.ContributorComplete, &use.WholeRow, &use.ResultTypeOID, &use.ResultComposite); err != nil {
			rows.Close()
			return PostgresPreparedManifest{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
		manifest.Columns = append(manifest.Columns, use)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL prepared variables", err)
	}
	rows.Close()
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresPreparedManifest{}, err
	}
	rows, err = tx.Query(ctx, `SELECT object_kind,object_oid FROM agentsql_catalog.prepared_objects($1) ORDER BY object_kind,object_oid`, name)
	if err != nil {
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL prepared objects", err)
	}
	for rows.Next() {
		var object PostgresObjectUse
		if err := rows.Scan(&object.Kind, &object.OID); err != nil {
			rows.Close()
			return PostgresPreparedManifest{}, catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
		manifest.Objects = append(manifest.Objects, object)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PostgresPreparedManifest{}, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL prepared objects", err)
	}
	rows.Close()
	return manifest, nil
}

func validatePostgresManifest(manifest PostgresPreparedManifest, budget PostgresCatalogBudget) error {
	if manifest.StatementName == "" || manifest.BackendPID == 0 || manifest.RoleOID == 0 || manifest.AnalyzedDigest == "" || manifest.DependencyDigest == "" || manifest.Invalidated || manifest.ReplanCount != 0 || manifest.CommandType != "SELECT" || manifest.HasRecursive || manifest.HasModifyingCTE {
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
	seen := make(map[uint32]struct{})
	for _, relation := range manifest.Relations {
		if relation.OID == 0 || relation.Kind != 'r' && relation.Kind != 'v' && relation.Kind != 'm' {
			return catalogAuthError("AUTH_RELATION_SHAPE_UNSUPPORTED")
		}
		if err := budget.CheckViewDepth(relation.ViewDepth); err != nil {
			return err
		}
		seen[relation.OID] = struct{}{}
	}
	if err := budget.ChargePaths(len(manifest.Relations) + len(manifest.Columns)); err != nil {
		return err
	}
	for _, use := range manifest.Columns {
		if use.WholeRow || use.Attnum <= 0 || use.ResultComposite || !use.ContributorComplete {
			return catalogAuthError("AUTH_COLUMN_SHAPE_UNSUPPORTED")
		}
		if _, ok := seen[use.RelationOID]; !ok {
			return catalogAuthError("AUTH_BINDER_INCOMPLETE")
		}
	}
	return nil
}

func lockPostgresRelations(ctx context.Context, tx pgx.Tx, relations []PostgresBoundRelation, budget PostgresCatalogBudget) error {
	ordered := append([]PostgresBoundRelation(nil), relations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].OID < ordered[j].OID })
	seen := make(map[uint32]struct{})
	for _, relation := range ordered {
		if _, ok := seen[relation.OID]; ok {
			continue
		}
		seen[relation.OID] = struct{}{}
		if relation.Kind != 'r' && relation.Kind != 'v' && relation.Kind != 'm' {
			return catalogAuthError("AUTH_RELATION_SHAPE_UNSUPPORTED")
		}
		if err := budget.ChargeCatalogRoundTrips(1); err != nil {
			return err
		}
		var qualified string
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.format('%I.%I',n.nspname,c.relname) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1 AND c.relkind=$2`, relation.OID, string(relation.Kind)).Scan(&qualified); err != nil {
			return &postgresCatalogRaceError{reason: "AUTH_CATALOG_RACE"}
		}
		lockSQL := "LOCK TABLE " + qualified + " IN ACCESS SHARE MODE"
		if relation.Kind == 'm' {
			// PostgreSQL rejects LOCK TABLE for materialized views. Planning a
			// zero-row SELECT obtains the same transaction-scoped AccessShareLock
			// without executing the relation, so WITH NO DATA also remains bindable.
			lockSQL = "EXPLAIN SELECT 1 FROM " + qualified + " LIMIT 0"
		}
		if _, err := tx.Exec(ctx, lockSQL); err != nil {
			return postgresDatabaseError(ctx, DBStageMetadata, "lock PostgreSQL relation", err)
		}
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_locks WHERE pid=pg_backend_pid() AND locktype='relation' AND relation=$1 AND mode='AccessShareLock' AND granted)`, relation.OID).Scan(&locked); err != nil || !locked {
			return &postgresCatalogRaceError{reason: "AUTH_CATALOG_RACE"}
		}
	}
	return nil
}

func readPostgresUserLocks(ctx context.Context, tx pgx.Tx, budget PostgresCatalogBudget) ([]uint32, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT l.relation FROM pg_catalog.pg_locks l JOIN pg_catalog.pg_class c ON c.oid=l.relation JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE l.pid=pg_backend_pid() AND l.locktype='relation' AND l.granted AND c.relkind IN ('r','v','m') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' ORDER BY l.relation`)
	if err != nil {
		return nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL held relation locks", err)
	}
	defer rows.Close()
	var result []uint32
	for rows.Next() {
		var oid uint32
		if err := rows.Scan(&oid); err != nil {
			return nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		result = append(result, oid)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL held relation locks", err)
	}
	return result, nil
}

func (prepared *PostgresPreparedSelect) Manifest() PostgresPreparedManifest {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.manifest
}
func (prepared *PostgresPreparedSelect) Fpre() PostgresCatalogFrame {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.fpre
}

func (prepared *PostgresPreparedSelect) Execute(ctx context.Context, rowLimit int) (model.QueryResult, error) {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed || prepared.executed || prepared.tx == nil || rowLimit <= 0 {
		return model.QueryResult{}, catalogAuthError("AUTH_PREPARED_STATE_INVALID")
	}
	result, err := prepared.executor.queryWithRunner(ctx, prepared.tx, `EXECUTE `+quoteInternalPreparedName(prepared.name), rowLimit)
	if err != nil {
		prepared.discard = true
		return model.QueryResult{}, err
	}
	prepared.executed = true
	return result, nil
}

func (prepared *PostgresPreparedSelect) VerifyPost(ctx context.Context, budget PostgresCatalogBudget) (PostgresCatalogFrame, error) {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed || !prepared.executed || prepared.tx == nil {
		return PostgresCatalogFrame{}, catalogAuthError("AUTH_PREPARED_STATE_INVALID")
	}
	manifest, err := readPostgresManifest(ctx, prepared.tx, prepared.name, prepared.manifest.Capability, budget)
	if err != nil {
		prepared.discard = true
		return PostgresCatalogFrame{}, err
	}
	if !samePreparedSeal(prepared.manifest, manifest) {
		prepared.discard = true
		return PostgresCatalogFrame{}, catalogAuthError("AUTH_PREPARED_INVALIDATED")
	}
	actual, err := readPostgresUserLocks(ctx, prepared.tx, budget)
	if err != nil {
		prepared.discard = true
		return PostgresCatalogFrame{}, err
	}
	if !sameOIDSet(actual, manifestRelationOIDs(manifest)) {
		prepared.discard = true
		return PostgresCatalogFrame{}, catalogAuthError("AUTH_BIND_CLOSURE_MISMATCH")
	}
	fpost, err := scanPostgresCatalog(ctx, prepared.tx, manifestRelationOIDs(manifest), manifestViewDepths(manifest), manifest.Objects, manifest.Capability.Allowlist, budget)
	if err != nil {
		prepared.discard = true
		return PostgresCatalogFrame{}, err
	}
	if fpost.Fingerprint != prepared.fpre.Fingerprint {
		prepared.discard = true
		return PostgresCatalogFrame{}, catalogAuthError("AUTH_CATALOG_RACE")
	}
	return fpost, nil
}

func (prepared *PostgresPreparedSelect) Close(ctx context.Context, commit bool) error {
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
	var err error
	if commit && !prepared.discard {
		_ = deallocatePostgresPrepared(ctx, prepared.tx, prepared.name)
		err = prepared.tx.Commit(ctx)
	} else {
		err = cleanupPostgresPreparedConnection(prepared.executor, prepared.connection, prepared.tx, prepared.name)
	}
	prepared.tx = nil
	if !commit || prepared.discard {
		prepared.connection = nil
		return err
	}
	if prepared.discard || err != nil {
		physical := prepared.connection.Hijack()
		prepared.connection = nil
		closeContext, cancel := context.WithTimeout(context.Background(), postgresCloseTimeout)
		closeErr := physical.Close(closeContext)
		cancel()
		if err != nil {
			return errors.Join(err, closeErr)
		}
		return closeErr
	}
	prepared.connection.Release()
	prepared.connection = nil
	return nil
}

func (prepared *PostgresPreparedSelect) closeForExecutor(context.Context) error {
	return prepared.Close(context.Background(), false)
}

func cleanupPostgresPreparedConnection(executor *PostgresExecutor, connection *pgxpool.Conn, tx pgx.Tx, name string) error {
	if connection == nil {
		return nil
	}
	timeout := postgresCloseTimeout
	if executor != nil && executor.timeout > 0 && executor.timeout < timeout {
		timeout = executor.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	deallocateErr := error(nil)
	if tx != nil && name != "" {
		deallocateErr = deallocatePostgresPrepared(ctx, tx, name)
	}
	rollbackErr := error(nil)
	if tx != nil {
		rollbackErr = tx.Rollback(ctx)
		if errors.Is(rollbackErr, pgx.ErrTxClosed) {
			rollbackErr = nil
		}
	}
	if deallocateErr != nil && rollbackErr == nil && name != "" {
		_, deallocateErr = connection.Exec(ctx, "DEALLOCATE "+quoteInternalPreparedName(name))
	}
	if deallocateErr != nil || rollbackErr != nil {
		physical := connection.Hijack()
		closeContext, closeCancel := context.WithTimeout(context.Background(), timeout)
		closeErr := physical.Close(closeContext)
		closeCancel()
		return errors.Join(rollbackErr, closeErr)
	}
	connection.Release()
	return nil
}

func setPostgresCatalogTimeout(ctx context.Context, tx pgx.Tx, executorTimeout time.Duration) error {
	timeout := postgresCatalogTimeout
	if executorTimeout > 0 && executorTimeout < timeout {
		timeout = executorTimeout
	}
	milliseconds := timeout.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	_, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('lock_timeout',$1,true),pg_catalog.set_config('statement_timeout',$1,true)`, fmt.Sprintf("%dms", milliseconds))
	if err != nil {
		return postgresDatabaseError(ctx, DBStageMetadata, "set PostgreSQL catalog timeout", err)
	}
	return nil
}
func deallocatePostgresPrepared(ctx context.Context, tx pgx.Tx, name string) error {
	_, err := tx.Exec(ctx, "DEALLOCATE "+quoteInternalPreparedName(name))
	return err
}
func quoteInternalPreparedName(name string) string { return `"` + name + `"` }
func randomPreparedName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "agentsql_" + hex.EncodeToString(value[:]), nil
}
func manifestRelationOIDs(manifest PostgresPreparedManifest) []uint32 {
	values := make([]uint32, 0, len(manifest.Relations))
	for _, r := range manifest.Relations {
		values = append(values, r.OID)
	}
	return uniqueSortedOIDs(values)
}
func manifestViewDepths(manifest PostgresPreparedManifest) map[uint32]int {
	result := make(map[uint32]int)
	for _, r := range manifest.Relations {
		if r.ViewDepth > result[r.OID] {
			result[r.OID] = r.ViewDepth
		}
	}
	return result
}
func samePostgresClosure(left, right []PostgresBoundRelation) bool {
	if !sameOIDSet(boundRelationOIDs(left), boundRelationOIDs(right)) {
		return false
	}
	kinds := make(map[uint32]byte, len(left))
	for _, relation := range left {
		if prior, exists := kinds[relation.OID]; exists && prior != relation.Kind {
			return false
		}
		kinds[relation.OID] = relation.Kind
	}
	for _, relation := range right {
		if kind, exists := kinds[relation.OID]; !exists || kind != relation.Kind {
			return false
		}
	}
	return true
}
func boundRelationOIDs(values []PostgresBoundRelation) []uint32 {
	result := make([]uint32, 0, len(values))
	for _, v := range values {
		result = append(result, v.OID)
	}
	return result
}
func sameOIDSet(left, right []uint32) bool {
	a, b := uniqueSortedOIDs(left), uniqueSortedOIDs(right)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func samePreparedSeal(left, right PostgresPreparedManifest) bool {
	return left.StatementName == right.StatementName && left.BackendPID == right.BackendPID && left.TransactionID == right.TransactionID && left.RoleOID == right.RoleOID && left.RoleName == right.RoleName && left.SearchPath == right.SearchPath && left.AnalyzedDigest == right.AnalyzedDigest && left.DependencyDigest == right.DependencyDigest && left.PlanGeneration == right.PlanGeneration && right.ReplanCount == 0 && !right.Invalidated && samePostgresClosure(left.Relations, right.Relations)
}
func enrollmentFrom(manifest PostgresPreparedManifest, frame PostgresCatalogFrame) PostgresEnrollment {
	h := sha256.New()
	writeCanonicalString(h, manifest.Capability.ExtensionHash)
	writeCanonicalString(h, manifest.Capability.NodeManifestHash)
	writeCanonicalString(h, manifest.Capability.AllowlistHash)
	writeCanonicalString(h, manifest.AnalyzedDigest)
	writeCanonicalString(h, manifest.DependencyDigest)
	binder := hex.EncodeToString(h.Sum(nil))
	h = sha256.New()
	writeCanonicalString(h, binder)
	writeCanonicalString(h, frame.Fingerprint)
	return PostgresEnrollment{Manifest: manifest, Catalog: frame, BinderFingerprint: binder, Fingerprint: "agentsql-pg-enrollment-v1:" + hex.EncodeToString(h.Sum(nil))}
}

// PostgresEnrollmentFromLocked derives the immutable enrollment digests from
// the exact locked manifest/catalog pair consumed by S4. It does not expose a
// database handle or permit execution.
func PostgresEnrollmentFromLocked(manifest PostgresPreparedManifest, frame PostgresCatalogFrame) PostgresEnrollment {
	return enrollmentFrom(manifest, frame)
}

type postgresCatalogRaceError struct{ reason string }

func (err *postgresCatalogRaceError) Error() string               { return err.reason }
func (err *postgresCatalogRaceError) AuthorizationReason() string { return err.reason }

var _ interface{ AuthorizationReason() string } = (*postgresCatalogRaceError)(nil)
