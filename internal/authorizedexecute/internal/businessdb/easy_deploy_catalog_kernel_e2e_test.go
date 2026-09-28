package businessdb

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestEasyDeployCatalogKernelPG14Through18Roles(t *testing.T) {
	for _, major := range []string{"14", "15", "16", "17", "18"} {
		major := major
		t.Run("pg"+major, func(t *testing.T) { runEasyDeployCatalogKernelMatrix(t, major) })
	}
}

func TestEasyDeployNativeCapabilityProbePG16(t *testing.T) {
	ctx := dockerTestContext(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	major := "16"
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-easy-deploy-probe", Tag: "pg16", BuildArgs: map[string]*string{"PG_MAJOR": &major}, KeepImage: true},
		Env:            map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"}, ExposedPorts: []string{"5432/tcp"}, Labels: map[string]string{"agentsql.easy-deploy.s2": "true"},
		WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second)}, Started: true})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	super := newEasyDeployTestExecutor(t, ctx, "native-probe-super", host, port.Int(), "agentsql", "agentsql-password")
	for _, statement := range []string{`CREATE ROLE probe_regular LOGIN PASSWORD 'regular-password' NOSUPERUSER NOCREATEDB NOCREATEROLE`} {
		_, err = super.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
	var audit []BinderCapabilityAuditRecord
	handshake, err := super.BootstrapEasyDeployBinder(ctx, EasyDeployBinderBootstrapOptions{CreateIfAvailable: true, RuntimeRole: "probe_regular",
		Audit: BinderCapabilityAuditFunc(func(_ context.Context, record BinderCapabilityAuditRecord) error {
			audit = append(audit, record)
			return nil
		})}, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.Equal(t, BinderModeNativeCV1, handshake.SelectedMode)
	require.GreaterOrEqual(t, len(audit), 4)
	require.Equal(t, "bootstrap_inventory", audit[0].Stage)
	require.Equal(t, "create", audit[1].Stage)
	require.Equal(t, "created", audit[1].Outcome)
	audit = nil
	handshake, err = super.BootstrapEasyDeployBinder(ctx, EasyDeployBinderBootstrapOptions{CreateIfAvailable: true, RuntimeRole: "probe_regular",
		Audit: BinderCapabilityAuditFunc(func(_ context.Context, record BinderCapabilityAuditRecord) error {
			audit = append(audit, record)
			return nil
		})}, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.Equal(t, BinderModeNativeCV1, handshake.SelectedMode)
	require.GreaterOrEqual(t, len(audit), 4)
	require.Equal(t, "create", audit[1].Stage)
	require.Equal(t, "configured", audit[1].Outcome)
	regular := newEasyDeployTestExecutor(t, ctx, "native-probe-regular", host, port.Int(), "probe_regular", "regular-password")
	expected, ok := PostgresBinderNativeExpectation(16)
	require.True(t, ok)
	for _, executor := range []*PostgresExecutor{super, regular} {
		handshake, err := executor.ProbeEasyDeployBinderCapabilities(ctx, expected, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.Equal(t, BinderModeNativeCV1, handshake.SelectedMode)
		require.True(t, handshake.Native.Available)
		require.Equal(t, "healthy", handshake.NativeHealth)
		mismatch := expected
		mismatch.AllowlistHash = "tampered"
		handshake, err = executor.ProbeEasyDeployBinderCapabilities(ctx, mismatch, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.Equal(t, BinderModeCatalogClosedV1, handshake.SelectedMode)
		require.False(t, handshake.Native.Available)
		require.Equal(t, "AUTH_BINDER_CAPABILITY_MISMATCH", handshake.NativeHealth)
	}
	for _, tamper := range []struct {
		name, version, buildHash string
	}{
		{name: "binary-attestation-hash", version: expected.ExtensionVersion, buildHash: "tampered-so-build"},
		{name: "extension-version", version: "0.3", buildHash: expected.BuildHash},
	} {
		t.Run("reject-"+tamper.name, func(t *testing.T) {
			statement := fmt.Sprintf(`CREATE OR REPLACE FUNCTION agentsql_catalog.capabilities() RETURNS jsonb
LANGUAGE SQL STABLE PARALLEL RESTRICTED AS $stub$
SELECT pg_catalog.jsonb_build_object(
  'abi', '%s', 'server_major', %d, 'extension_version', '%s',
  'build_hash', '%s', 'extension_hash', '%s',
  'node_manifest_hash', '%s', 'allowlist_hash', '%s')
$stub$`, expected.ABI, expected.ServerMajor, tamper.version, tamper.buildHash,
				expected.ExtensionHash, expected.NodeManifestHash, expected.AllowlistHash)
			_, err := super.Execute(ctx, statement)
			require.NoError(t, err)
			handshake, err := super.ProbeEasyDeployBinderCapabilities(ctx, expected, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			require.Equal(t, BinderModeCatalogClosedV1, handshake.SelectedMode)
			require.False(t, handshake.Native.Available)
			require.Equal(t, "AUTH_BINDER_CAPABILITY_MISMATCH", handshake.NativeHealth)
		})
	}
	_, err = super.Execute(ctx, `CREATE OR REPLACE FUNCTION agentsql_catalog.capabilities() RETURNS jsonb
AS '$libdir/agentsql_binder', 'agentsql_binder_capabilities'
LANGUAGE C STABLE PARALLEL RESTRICTED`)
	require.NoError(t, err)
	exitCode, _, err := container.Exec(ctx, []string{"sh", "-c", `library="$(pg_config --pkglibdir)/agentsql_binder.so"; printf 'tampered shared object\n' > "${library}.tampered"; chmod 0755 "${library}.tampered"; mv "${library}.tampered" "$library"`})
	require.NoError(t, err)
	require.Zero(t, exitCode)
	tampered := newEasyDeployTestExecutor(t, ctx, "native-probe-tampered-so", host, port.Int(), "agentsql", "agentsql-password")
	handshake, err = tampered.ProbeEasyDeployBinderCapabilities(ctx, expected, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, handshake.SelectedMode)
	require.False(t, handshake.Native.Available)
	require.Equal(t, "AUTH_BINDER_CAPABILITY_MISMATCH", handshake.NativeHealth)
}

func runEasyDeployCatalogKernelMatrix(t *testing.T, major string) {
	t.Helper()
	ctx := dockerTestContext(t)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:" + major,
			Env:          map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
			ExposedPorts: []string{"5432/tcp"},
			Labels:       map[string]string{"agentsql.easy-deploy.s2": "true", "agentsql.pg-major": major},
			WaitingFor:   wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second),
		}, Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	super := newEasyDeployTestExecutor(t, ctx, "s2-super-"+major, host, port.Int(), "agentsql", "agentsql-password")
	setupEasyDeployFixture(t, ctx, super)
	regular := newEasyDeployTestExecutor(t, ctx, "s2-regular-"+major, host, port.Int(), "edb_regular", "regular-password")

	for _, role := range []struct {
		name     string
		executor *PostgresExecutor
	}{{"super", super}, {"regular", regular}} {
		role := role
		t.Run(role.name, func(t *testing.T) { runEasyDeployRoleScenarios(t, ctx, major, role.name, role.executor, super) })
	}
}

func newEasyDeployTestExecutor(t *testing.T, ctx context.Context, id, host string, port int, username, password string) *PostgresExecutor {
	t.Helper()
	executor, err := NewPostgresExecutor(ctx, model.Datasource{ID: id, DBType: "postgres", Host: host, Port: port, Database: "agentsql", Username: username, ConnLimit: 8, StmtTimeoutMS: 5_000}, password, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	return executor
}

func setupEasyDeployFixture(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	statements := []string{
		`CREATE ROLE edb_regular LOGIN PASSWORD 'regular-password' NOSUPERUSER NOCREATEDB NOCREATEROLE`,
		`CREATE SCHEMA edb`,
		`CREATE TABLE edb.base(id integer, note text)`,
		`CREATE TABLE edb.hidden(id integer)`,
		`CREATE TABLE edb.aba(id integer)`,
		`CREATE TABLE edb.aba_server(id integer)`,
		`CREATE VIEW edb.base_view AS SELECT id FROM edb.base`,
		`CREATE TABLE edb.audit(id integer)`,
		`CREATE TABLE edb.trigger_target(id integer)`,
		`CREATE FUNCTION edb.audit_trigger() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN INSERT INTO edb.audit VALUES (NEW.id); RETURN NEW; END'`,
		`CREATE TRIGGER audit_trigger AFTER INSERT ON edb.trigger_target FOR EACH ROW EXECUTE FUNCTION edb.audit_trigger()`,
		`CREATE TABLE edb.rule_target(id integer)`,
		`CREATE RULE audit_rule AS ON INSERT TO edb.rule_target DO ALSO INSERT INTO edb.audit VALUES (NEW.id)`,
		`CREATE TABLE edb.rls_target(id integer)`,
		`ALTER TABLE edb.rls_target ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY rls_policy ON edb.rls_target USING (true)`,
		`CREATE TABLE edb.parent_target(id integer)`,
		`CREATE TABLE edb.child_target(note text) INHERITS (edb.parent_target)`,
		`CREATE TABLE edb.partition_target(id integer) PARTITION BY RANGE(id)`,
		`CREATE TABLE edb.partition_child PARTITION OF edb.partition_target FOR VALUES FROM (0) TO (100)`,
		`CREATE TABLE edb.fk_parent(id integer PRIMARY KEY)`,
		`CREATE TABLE edb.fk_child(parent_id integer REFERENCES edb.fk_parent(id))`,
		`CREATE TABLE edb.check_target(value integer CHECK(value > 0))`,
		`CREATE TABLE edb.default_target(value integer DEFAULT 1)`,
		`CREATE TABLE edb.expression_index_target(value integer)`,
		`CREATE INDEX expression_index ON edb.expression_index_target ((value + 1))`,
		`GRANT USAGE ON SCHEMA edb TO edb_regular`,
		`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA edb TO edb_regular`,
	}
	for _, statement := range statements {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
}

func runEasyDeployRoleScenarios(t *testing.T, ctx context.Context, major, role string, executor, super *PostgresExecutor) {
	t.Helper()
	started := time.Now()
	handshake, err := executor.ProbeEasyDeployBinderCapabilities(ctx, NativeCapabilityExpectation{}, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, handshake.SelectedMode)
	require.True(t, handshake.Closed.Available)
	require.False(t, handshake.Native.Available)
	require.Equal(t, BinderCodeModeRequired, handshake.NativeHealth)
	require.Equal(t, major, fmt.Sprint(handshake.Closed.ServerMajor))

	candidate, err := executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "base"}}, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.Len(t, candidate.Frame.Relations, 1)
	require.Len(t, candidate.Frame.Columns, 2)
	require.NotEmpty(t, candidate.Frame.Dependencies)
	relationOID := candidate.Frame.Relations[0].OID
	id := closedCatalogColumn(t, candidate.Frame, "id")
	facts := SemanticFacts{StatementClass: BinderStatementSelect, ColumnUses: []SemanticColumnUse{{RelationOID: relationOID, Attnum: id.Attnum, Usage: SemanticUsageOutput, Site: "target", OutputIndex: 0}}}
	prepared, err := executor.PrepareClosedCatalog(ctx, `SELECT id FROM edb.base WHERE id > 0`, candidate, facts, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	program := prepared.Program()
	require.Equal(t, BinderModeCatalogClosedV1, program.Mode)
	require.NotEmpty(t, program.SemanticFactsDigest)
	require.Len(t, program.LockExpectation, 1)
	_, err = prepared.Execute(ctx, 10)
	require.NoError(t, err)
	proof, err := prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.NotEmpty(t, proof.AttestationDigest)
	require.NoError(t, prepared.Close(ctx))

	t.Run("ordered-lock-blocks-ddl", func(t *testing.T) {
		candidate, err := executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "hidden"}, {Schema: "edb", Name: "base"}}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		facts := SemanticFacts{StatementClass: BinderStatementSelect}
		prepared, err := executor.PrepareClosedCatalog(ctx, `SELECT b.id FROM edb.base b JOIN edb.hidden h ON h.id=b.id`, candidate, facts, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		for i := 1; i < len(prepared.Program().CatalogRoots); i++ {
			require.Less(t, prepared.Program().CatalogRoots[i-1], prepared.Program().CatalogRoots[i])
		}
		conn, err := super.pool.Acquire(ctx)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `SET lock_timeout='250ms'`)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `ALTER TABLE edb.base ADD COLUMN ddl_block_probe integer`)
		require.Error(t, err)
		conn.Release()
		require.NoError(t, prepared.Close(ctx))
		_, err = super.Execute(ctx, `ALTER TABLE edb.base ADD COLUMN ddl_block_probe integer`)
		require.NoError(t, err)
		_, err = super.Execute(ctx, `ALTER TABLE edb.base DROP COLUMN ddl_block_probe`)
		require.NoError(t, err)
	})

	t.Run("prepare-lock-crosscheck", func(t *testing.T) {
		candidate, err := executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "base"}}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = executor.PrepareClosedCatalog(ctx, `SELECT b.id FROM edb.base b JOIN edb.hidden h ON h.id=b.id`, candidate, SemanticFacts{StatementClass: BinderStatementSelect}, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, "AUTH_BIND_CLOSURE_MISMATCH")
	})

	t.Run("aba-and-old-prepared", func(t *testing.T) {
		candidate, err := executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "aba"}}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		oldOID := candidate.Frame.Relations[0].OID
		_, err = super.Execute(ctx, `DROP TABLE edb.aba`)
		require.NoError(t, err)
		_, err = super.Execute(ctx, `CREATE TABLE edb.aba(id integer)`)
		require.NoError(t, err)
		_, err = super.Execute(ctx, `GRANT SELECT,INSERT,UPDATE,DELETE ON edb.aba TO edb_regular`)
		require.NoError(t, err)
		fresh, err := executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "aba"}}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NotEqual(t, oldOID, fresh.Frame.Relations[0].OID)
		_, err = executor.PrepareClosedCatalog(ctx, `SELECT id FROM edb.aba`, candidate, SemanticFacts{StatementClass: BinderStatementSelect}, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, "AUTH_CATALOG_RACE")

		conn, err := executor.pool.Acquire(ctx)
		require.NoError(t, err)
		defer conn.Release()
		_, err = conn.Exec(ctx, `PREPARE agentsql_aba_observation AS SELECT id FROM edb.aba_server`)
		require.NoError(t, err)
		_, err = super.Execute(ctx, `DROP TABLE edb.aba_server`)
		require.NoError(t, err)
		_, err = super.Execute(ctx, `CREATE TABLE edb.aba_server(id integer)`)
		require.NoError(t, err)
		_, err = super.Execute(ctx, `GRANT SELECT ON edb.aba_server TO edb_regular`)
		require.NoError(t, err)
		rows, err := conn.Query(ctx, `EXECUTE agentsql_aba_observation`)
		require.NoError(t, err)
		rows.Close()
		_, err = conn.Exec(ctx, `DEALLOCATE agentsql_aba_observation`)
		require.NoError(t, err)
	})

	t.Run("implicit-presence-denied", func(t *testing.T) {
		for _, name := range []string{"trigger_target", "rule_target", "rls_target", "parent_target", "child_target", "partition_target", "fk_parent", "fk_child", "check_target", "default_target", "expression_index_target"} {
			_, err := executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: name}}, &unlimitedPostgresBudget{})
			var typed *BinderError
			require.Error(t, err, name)
			require.True(t, errors.As(err, &typed), "%s: %#v", name, err)
			require.Contains(t, []string{"AUTH_IMPLICIT_OBJECT_UNSUPPORTED", "AUTH_RELATION_SHAPE_UNSUPPORTED"}, typed.Code, name)
		}
	})

	t.Run("resolution-gaps-fail-closed", func(t *testing.T) {
		_, err := executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Name: "base"}}, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, BinderCodeModeRequired)
		_, err = executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "base_view"}}, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, BinderCodeModeRequired)
		_, err = executor.DiscoverClosedCatalog(ctx, nil, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, "AUTH_CATALOG_INCOMPLETE")
		_, err = executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "missing_relation"}}, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, "AUTH_CATALOG_INCOMPLETE")
	})
	fullP95, cachedP95 := measureEasyDeployPaths(t, ctx, executor)
	t.Logf("PG%s/%s catalog-kernel scenarios=%s full_p95=%s cached_revalidate_p95=%s", major, role, time.Since(started), fullP95, cachedP95)
}

func measureEasyDeployPaths(t *testing.T, ctx context.Context, executor *PostgresExecutor) (time.Duration, time.Duration) {
	t.Helper()
	const samples = 20
	full := make([]time.Duration, 0, samples)
	cached := make([]time.Duration, 0, samples)
	var candidate ClosedCatalogCandidate
	for i := 0; i < samples; i++ {
		started := time.Now()
		var err error
		candidate, err = executor.DiscoverClosedCatalog(ctx, []ClosedRelationRef{{Schema: "edb", Name: "base"}}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		prepared, err := executor.PrepareClosedCatalog(ctx, `SELECT id FROM edb.base`, candidate, SemanticFacts{StatementClass: BinderStatementSelect}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx))
		full = append(full, time.Since(started))
	}
	for i := 0; i < samples; i++ {
		started := time.Now()
		prepared, err := executor.PrepareClosedCatalog(ctx, `SELECT id FROM edb.base`, candidate, SemanticFacts{StatementClass: BinderStatementSelect}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx))
		cached = append(cached, time.Since(started))
	}
	return nearestRankP95(full), nearestRankP95(cached)
}

func nearestRankP95(values []time.Duration) time.Duration {
	values = append([]time.Duration(nil), values...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (95*len(values)+99)/100 - 1
	return values[index]
}

func closedCatalogColumn(t *testing.T, frame PostgresCatalogFrame, name string) PostgresColumnIdentity {
	t.Helper()
	for _, column := range frame.Columns {
		if column.Name == name {
			return column
		}
	}
	t.Fatalf("column %s not found", name)
	return PostgresColumnIdentity{}
}

func requireEasyDeployReason(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err)
	var reasoned interface{ AuthorizationReason() string }
	require.True(t, errors.As(err, &reasoned), "%#v", err)
	require.Equal(t, want, reasoned.AuthorizationReason())
}
