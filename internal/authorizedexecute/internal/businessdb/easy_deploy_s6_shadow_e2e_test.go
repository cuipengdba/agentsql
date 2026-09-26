package businessdb

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestEasyDeployS6ShadowMatrix is deliberately opt-in: one cell builds and
// installs the real C extension, while the other proves the zero-install TLS
// path with an official PostgreSQL image.
func TestEasyDeployS6ShadowMatrix(t *testing.T) {
	if os.Getenv("AGENTSQL_EASYDEPLOY_S6_MATRIX") != "1" {
		t.Skip("set AGENTSQL_EASYDEPLOY_S6_MATRIX=1 to run the PG14/18 S6 shadow matrix")
	}
	for _, cell := range []struct {
		major           int
		tls             bool
		nativeInstalled bool
	}{
		{major: 14, nativeInstalled: true},
		{major: 18, tls: true, nativeInstalled: false},
	} {
		cell := cell
		name := "pg" + strconv.Itoa(cell.major) + "-closed-only"
		if cell.nativeInstalled {
			name = "pg" + strconv.Itoa(cell.major) + "-native-c"
		}
		if cell.tls {
			name += "-tls"
		}
		t.Run(name, func(t *testing.T) { runEasyDeployS6ShadowCell(t, cell.major, cell.tls, cell.nativeInstalled) })
	}
}

func runEasyDeployS6ShadowCell(t *testing.T, major int, tls, nativeInstalled bool) {
	t.Helper()
	unavailable, err := probeDockerAvailable()
	if unavailable {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	t.Cleanup(cancel)

	majorString := strconv.Itoa(major)
	request := testcontainers.ContainerRequest{
		Env:          map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
		ExposedPorts: []string{"5432/tcp"},
		Labels: map[string]string{
			"agentsql.easy-deploy.s6": "true", "agentsql.pg-major": majorString,
			"agentsql.native-installed": strconv.FormatBool(nativeInstalled),
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		).WithDeadline(2 * time.Minute),
	}
	if nativeInstalled {
		root, rootErr := filepath.Abs(filepath.Join("..", "..", "..", "..", "dbext", "postgres", "agentsql_binder"))
		require.NoError(t, rootErr)
		request.FromDockerfile = testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test",
			Repo: "agentsql-easy-deploy-s6", Tag: "pg" + majorString, BuildArgs: map[string]*string{"PG_MAJOR": &majorString}, KeepImage: true}
	} else {
		request.Image = "postgres:" + majorString
	}
	if tls {
		request.Entrypoint = []string{"/bin/bash", "-c"}
		request.Cmd = []string{`openssl req -new -x509 -nodes -days 1 -subj '/CN=localhost' -out /tmp/agentsql-server.crt -keyout /tmp/agentsql-server.key >/dev/null 2>&1 && chown postgres:postgres /tmp/agentsql-server.crt /tmp/agentsql-server.key && chmod 600 /tmp/agentsql-server.key && exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/agentsql-server.crt -c ssl_key_file=/tmp/agentsql-server.key`}
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	sslmode := "disable"
	if tls {
		sslmode = "require"
	}
	executor := newPostgresDMLMatrixExecutor(t, ctx, host, port.Int(), sslmode)
	if tls {
		var encrypted bool
		require.NoError(t, executor.pool.QueryRow(ctx,
			`SELECT ssl FROM pg_catalog.pg_stat_ssl WHERE pid=pg_backend_pid()`).Scan(&encrypted))
		require.True(t, encrypted, "matrix TLS cell used an unencrypted backend")
	}

	if nativeInstalled {
		_, err = executor.Execute(ctx, `CREATE SCHEMA agentsql_catalog`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`)
		require.NoError(t, err)
	}
	setupEasyDeployS6CorpusFixture(t, ctx, executor)
	expected, ok := PostgresBinderNativeExpectation(major)
	require.True(t, ok)
	handshake, err := executor.ProbeEasyDeployBinderCapabilities(ctx, expected, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.True(t, handshake.Closed.Available)
	require.Equal(t, nativeInstalled, handshake.NativeFilesAvailable)
	require.Equal(t, nativeInstalled, handshake.NativeInstalled)
	if nativeInstalled {
		require.Equal(t, BinderModeNativeCV1, handshake.SelectedMode)
		require.Equal(t, BinderNativeHealthHealthy, handshake.NativeHealth)
		runEasyDeployS6CorpusReplay(t, ctx, executor, handshake, major)
		runEasyDeployS6RealShadow(t, ctx, executor, handshake, major)
	} else {
		require.Equal(t, BinderModeCatalogClosedV1, handshake.SelectedMode)
		require.Equal(t, BinderCodeModeRequired, handshake.NativeHealth)
		runEasyDeployS6ClosedOnly(t, ctx, executor, handshake, major)
	}
	runEasyDeployS6NegativeEscapeGate(t, ctx, executor, handshake, major)
}

func runEasyDeployS6CorpusReplay(t *testing.T, ctx context.Context, executor *PostgresExecutor,
	handshake BinderCapabilityHandshake, major int) {
	t.Helper()
	datasource := "s6-corpus-pg" + strconv.Itoa(major)
	selector := NewBinderModeSelector(NewNativeHealthRegistry())
	closedCovered, nativeCovered := 0, 0
	for _, entry := range loadEasyDeployS6Corpus(t).Entries {
		request := BindRequest{RawSQL: entry.SQL, Identity: SemanticIdentity{DatasourceIdentity: datasource}}
		class, _, _ := ClassifyEasyDeployClosedSyntax(entry.SQL)
		selection, selectionErr := selector.Select(BinderSelectionRequest{DatasourceIdentity: datasource,
			RequestDigest: EasyDeployRequestDigest(entry.SQL), StatementClass: class, ClosedDisposition: entry.Expectation,
			Provider: postgresProviderSelfManaged, Handshake: handshake})
		if entry.Expectation == ClosedRequestMustReject {
			require.Error(t, selectionErr, "final corpus gate %s", entry.ID)
			continue
		}
		require.NoError(t, selectionErr, "final corpus gate %s", entry.ID)
		if entry.Expectation == ClosedRequestProven {
			require.Equal(t, BinderModeCatalogClosedV1, selection.Mode, entry.ID)
			_, err := realClosedShadowBind(executor)(ctx, request)
			require.NoError(t, err, "closed corpus replay %s", entry.ID)
			closedCovered++
		} else {
			require.Equal(t, BinderModeNativeCV1, selection.Mode, entry.ID)
		}
		if !entry.NativeSupported {
			continue
		}
		var err error
		switch class {
		case BinderStatementSelect:
			_, err = realNativeSelectShadowBind(executor, nil)(ctx, request)
		case BinderStatementInsert, BinderStatementUpdate, BinderStatementDelete:
			_, err = realNativeDMLShadowBind(executor)(ctx, request)
		default:
			err = NewCapabilityFailure(BinderCodeModeUnsupported)
		}
		if err != nil {
			logPostgresDBError(t, err)
		}
		require.NoError(t, err, "native corpus replay %s", entry.ID)
		nativeCovered++
	}
	require.Equal(t, 32, closedCovered)
	require.Equal(t, 46, nativeCovered)
}

func setupEasyDeployS6CorpusFixture(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	for _, statement := range []string{
		`CREATE SCHEMA s6c`,
		`CREATE TABLE s6c.accounts(id integer, name text, active boolean, balance integer)`,
		`CREATE TABLE s6c.events(id integer, account_id integer, amount integer)`,
		`INSERT INTO s6c.accounts VALUES (1,'alpha',true,100),(2,'beta',false,200)`,
		`INSERT INTO s6c.events VALUES (1,1,10),(2,2,20)`,
		`CREATE VIEW s6c.account_view AS SELECT id,name,active,balance FROM s6c.accounts`,
		`CREATE VIEW s6c.account_view_2 AS SELECT id,name FROM s6c.account_view`,
		`CREATE MATERIALIZED VIEW s6c.account_rollup AS SELECT id FROM s6c.accounts WITH NO DATA`,
		`CREATE TABLE s6c.has_trigger(id integer, value integer)`,
		`CREATE FUNCTION s6c.trigger_fn() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'`,
		`CREATE TRIGGER s6c_customer_trigger BEFORE UPDATE ON s6c.has_trigger FOR EACH ROW EXECUTE FUNCTION s6c.trigger_fn()`,
		`CREATE TABLE s6c.has_default(id integer, value integer DEFAULT 7)`,
		`CREATE TABLE s6c.has_rls(id integer)`,
		`ALTER TABLE s6c.has_rls ENABLE ROW LEVEL SECURITY`,
	} {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
}

func runEasyDeployS6RealShadow(t *testing.T, ctx context.Context, executor *PostgresExecutor,
	handshake BinderCapabilityHandshake, major int) {
	t.Helper()
	health := NewNativeHealthRegistry()
	selector := NewBinderModeSelector(health)
	nativeRequired := BinderSelectionRequest{DatasourceIdentity: "s6-shadow-pg" + strconv.Itoa(major),
		RequestDigest: EasyDeployRequestDigest("native-required-before-divergence"), StatementClass: BinderStatementSelect,
		ClosedDisposition: ClosedRequestNativeRequired, Provider: postgresProviderSelfManaged, Handshake: handshake}
	decision, err := selector.Select(nativeRequired)
	require.NoError(t, err)
	require.Equal(t, BinderModeNativeCV1, decision.Mode)

	metrics := NewInMemoryShadowMetrics()
	events := make([]ShadowDifferentialEvent, 0, 5)
	runner := NewShadowDifferentialRunner(health, ShadowDifferentialSinkFunc(func(_ context.Context, event ShadowDifferentialEvent) error {
		events = append(events, event)
		return nil
	}), metrics)
	for _, sql := range []string{
		`SELECT a.id,a.name FROM s6c.accounts a WHERE a.id>0`,
		`SELECT a.id,e.amount FROM s6c.accounts a INNER JOIN s6c.events e ON a.id=e.account_id WHERE e.amount>0`,
		`SELECT a.id FROM s6c.accounts a WHERE EXISTS (SELECT e.id FROM s6c.events e WHERE e.account_id=a.id)`,
		`SELECT a.balance+1 FROM s6c.accounts a WHERE a.id=1`,
	} {
		class, disposition, _ := ClassifyEasyDeployClosedSyntax(sql)
		require.Equal(t, ClosedRequestProven, disposition)
		input := ShadowDifferentialInput{DatasourceIdentity: nativeRequired.DatasourceIdentity,
			RequestDigest: EasyDeployRequestDigest(sql), StatementClass: class, ClosedDisposition: disposition,
			NativeCapabilityDigest: handshake.Native.Digest}
		result, runErr := runner.Run(ctx, input, BindRequest{RawSQL: sql,
			Identity: SemanticIdentity{DatasourceIdentity: nativeRequired.DatasourceIdentity}},
			realClosedShadowBind(executor), realNativeSelectShadowBind(executor, nil))
		require.NoError(t, runErr, "%s result=%+v", sql, result)
		require.Equal(t, ShadowConsistent, result.Status, sql)
		require.Empty(t, result.DifferingFields, sql)
	}
	matviewSQL := `SELECT m.id FROM s6c.account_rollup m`
	matviewResult, matviewErr := runner.Run(ctx, ShadowDifferentialInput{DatasourceIdentity: nativeRequired.DatasourceIdentity,
		RequestDigest: EasyDeployRequestDigest(matviewSQL), StatementClass: BinderStatementSelect,
		ClosedDisposition: ClosedRequestNativeRequired, NativeCapabilityDigest: handshake.Native.Digest},
		BindRequest{RawSQL: matviewSQL, Identity: SemanticIdentity{DatasourceIdentity: nativeRequired.DatasourceIdentity}},
		realClosedShadowBind(executor), realNativeSelectShadowBind(executor, nil))
	require.NoError(t, matviewErr, "%s result=%+v", matviewSQL, matviewResult)
	require.Equal(t, ShadowNativeOnly, matviewResult.Status)
	require.Equal(t, "AUTH_RELATION_SHAPE_UNSUPPORTED", matviewResult.ClosedReason)
	for _, sql := range []string{
		`INSERT INTO s6c.events(id,account_id,amount) VALUES (30,1,10)`,
		`UPDATE s6c.events e SET amount=e.amount+1 WHERE e.id=1`,
		`DELETE FROM s6c.events e WHERE e.id=2`,
	} {
		class, disposition, _ := ClassifyEasyDeployClosedSyntax(sql)
		require.Equal(t, ClosedRequestProven, disposition)
		input := ShadowDifferentialInput{DatasourceIdentity: nativeRequired.DatasourceIdentity,
			RequestDigest: EasyDeployRequestDigest(sql), StatementClass: class, ClosedDisposition: disposition,
			NativeCapabilityDigest: handshake.Native.Digest}
		result, runErr := runner.Run(ctx, input, BindRequest{RawSQL: sql,
			Identity: SemanticIdentity{DatasourceIdentity: nativeRequired.DatasourceIdentity}},
			realClosedShadowBind(executor), realNativeDMLShadowBind(executor))
		require.NoError(t, runErr, "%s result=%+v", sql, result)
		require.Equal(t, ShadowConsistent, result.Status, sql)
		require.Empty(t, result.DifferingFields, sql)
	}

	divergentSQL := `SELECT a.id FROM s6c.accounts a WHERE a.id=1`
	result, err := runner.Run(ctx, ShadowDifferentialInput{DatasourceIdentity: nativeRequired.DatasourceIdentity,
		RequestDigest: EasyDeployRequestDigest(divergentSQL), StatementClass: BinderStatementSelect,
		ClosedDisposition: ClosedRequestProven, NativeCapabilityDigest: handshake.Native.Digest},
		BindRequest{RawSQL: divergentSQL, Identity: SemanticIdentity{DatasourceIdentity: nativeRequired.DatasourceIdentity}},
		realClosedShadowBind(executor), realNativeSelectShadowBind(executor, func(facts *SemanticFacts) {
			require.NotEmpty(t, facts.ColumnUses)
			facts.ColumnUses[0].Name += "_injected_divergence"
		}))
	require.Error(t, err)
	require.True(t, isBinderDivergence(err))
	require.Equal(t, ShadowDivergent, result.Status)
	require.True(t, result.NativeMarkedUnhealthy)
	require.NotEmpty(t, result.DifferingFields)
	require.Equal(t, uint64(1), metrics.Snapshot().Divergence)
	require.Len(t, events, 9)

	for index := 0; index < 2; index++ {
		fresh := nativeRequired
		fresh.RequestDigest = EasyDeployRequestDigest("post-divergence-" + strconv.Itoa(index))
		decision, err = selector.Select(fresh)
		require.NoError(t, err)
		require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
		require.Equal(t, ModeSelectionNativeUnhealthy, decision.Reason)
	}
}

func runEasyDeployS6ClosedOnly(t *testing.T, ctx context.Context, executor *PostgresExecutor,
	handshake BinderCapabilityHandshake, major int) {
	t.Helper()
	selector := NewBinderModeSelector(NewNativeHealthRegistry())
	request := BinderSelectionRequest{DatasourceIdentity: "s6-closed-pg" + strconv.Itoa(major),
		RequestDigest: EasyDeployRequestDigest("closed-only-native-shape"), StatementClass: BinderStatementSelect,
		ClosedDisposition: ClosedRequestNativeRequired, Provider: postgresProviderSelfManaged, Handshake: handshake}
	decision, err := selector.Select(request)
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
	require.Equal(t, ModeSelectionNativeAbsent, decision.Reason)

	sql := `SELECT a.id,a.name FROM s6c.accounts a WHERE a.id=1`
	program, err := realClosedShadowBind(executor)(ctx, BindRequest{RawSQL: sql,
		Identity: SemanticIdentity{DatasourceIdentity: request.DatasourceIdentity}})
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, program.Mode)
	require.Equal(t, BinderStatementSelect, program.Facts.StatementClass)

	matview := request
	matview.RequestDigest = EasyDeployRequestDigest("closed-only-matview")
	matview.RequiresMatview = true
	decision, err = selector.Select(matview)
	requireAuthorizationReason(t, err, "AUTH_RELATION_SHAPE_UNSUPPORTED")
	require.True(t, decision.Rejected)
}

func runEasyDeployS6NegativeEscapeGate(t *testing.T, ctx context.Context, executor *PostgresExecutor,
	handshake BinderCapabilityHandshake, major int) {
	t.Helper()
	selector := NewBinderModeSelector(NewNativeHealthRegistry())
	for _, entry := range loadEasyDeployS6Corpus(t).Entries {
		if entry.Tier != "negative_escape" {
			continue
		}
		t.Run(entry.ID, func(t *testing.T) {
			class, _, _ := ClassifyEasyDeployClosedSyntax(entry.SQL)
			_, err := selector.Select(BinderSelectionRequest{DatasourceIdentity: "s6-escape-pg" + strconv.Itoa(major),
				RequestDigest: EasyDeployRequestDigest(entry.SQL), StatementClass: class,
				ClosedDisposition: ClosedRequestMustReject, Provider: postgresProviderSelfManaged, Handshake: handshake})
			require.Error(t, err, "negative escape reached a binder mode: %s", entry.ID)

			request := BindRequest{RawSQL: entry.SQL, Identity: SemanticIdentity{DatasourceIdentity: "s6-escape-pg" + strconv.Itoa(major)}}
			var prepared *PostgresClosedPrepared
			switch class {
			case BinderStatementSelect:
				prepared, err = executor.BindClosedSelect(ctx, request, &unlimitedPostgresBudget{})
			case BinderStatementInsert, BinderStatementUpdate, BinderStatementDelete:
				prepared, err = executor.BindClosedDML(ctx, request, &unlimitedPostgresBudget{})
			default:
				_, disposition, _ := ClassifyEasyDeployClosedSyntax(entry.SQL)
				require.Equal(t, ClosedRequestMustReject, disposition)
				return
			}
			if prepared != nil {
				_ = prepared.Close(ctx)
			}
			require.Error(t, err, "negative escape passed the closed binder: %s", entry.ID)
		})
	}
}

func realClosedShadowBind(executor *PostgresExecutor) ShadowBindFunc {
	return func(ctx context.Context, request BindRequest) (BoundProgram, error) {
		class, _, _ := ClassifyEasyDeployClosedSyntax(request.RawSQL)
		var prepared *PostgresClosedPrepared
		var err error
		switch class {
		case BinderStatementSelect:
			prepared, err = executor.BindClosedSelect(ctx, request, &unlimitedPostgresBudget{})
		case BinderStatementInsert, BinderStatementUpdate, BinderStatementDelete:
			prepared, err = executor.BindClosedDML(ctx, request, &unlimitedPostgresBudget{})
		default:
			return BoundProgram{}, NewCapabilityFailure(BinderCodeModeUnsupported)
		}
		if err != nil {
			return BoundProgram{}, err
		}
		program := prepared.Program()
		if _, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{}); err != nil {
			_ = prepared.Close(context.Background())
			return BoundProgram{}, err
		}
		if err = prepared.Close(ctx); err != nil {
			return BoundProgram{}, err
		}
		return program, nil
	}
}

func realNativeSelectShadowBind(executor *PostgresExecutor, mutate func(*SemanticFacts)) ShadowBindFunc {
	return func(ctx context.Context, request BindRequest) (BoundProgram, error) {
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, request.RawSQL, nil, &unlimitedPostgresBudget{})
		if err != nil {
			return BoundProgram{}, err
		}
		manifest, frame := prepared.Manifest(), prepared.Fpre()
		facts, err := NativeSelectSemanticFacts(request.Identity.DatasourceIdentity, request.Identity, manifest, frame)
		if err == nil && mutate != nil {
			mutate(&facts)
		}
		var program BoundProgram
		if err == nil {
			program, err = NativeBoundProgram(facts, manifest, frame)
		}
		closeErr := prepared.Close(ctx, false)
		if err != nil {
			return BoundProgram{}, err
		}
		if closeErr != nil {
			return BoundProgram{}, closeErr
		}
		return program, nil
	}
}

func realNativeDMLShadowBind(executor *PostgresExecutor) ShadowBindFunc {
	return func(ctx context.Context, request BindRequest) (program BoundProgram, resultErr error) {
		enrollment, err := executor.EnrollPostgresDML(ctx, request.RawSQL, request.Identity.DatasourceIdentity, &unlimitedPostgresBudget{})
		if err != nil {
			return BoundProgram{}, err
		}
		tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return BoundProgram{}, err
		}
		defer func() {
			if rollbackErr := tx.Rollback(context.Background()); resultErr == nil && rollbackErr != nil && rollbackErr != pgx.ErrTxClosed {
				resultErr = rollbackErr
			}
		}()
		owner := b5terminal.NewTerminalOwner(1, 1)
		machine := b5dml.NewBeginMachine(1, 1, owner)
		if !machine.PinConnection() || !machine.ObserveBegin(b5dml.BeginAttemptEvidence{
			Write: b5terminal.WriteEvidence{Phase: b5terminal.WriteFullFrame, FrameBytes: 5, BytesWritten: 5},
			Reply: b5dml.BeginReplyPositiveACK, Correlation: b5terminal.CorrelationStrongCurrentOperation,
			ServerStatus: b5terminal.ServerReadyIdleInTransaction,
		}) {
			return BoundProgram{}, NewCapabilityFailure("AUTH_DML_NATIVE_BEGIN_REQUIRED")
		}
		nativeTx, err := AttachPostgresDMLNativeTx(ctx, tx, machine)
		if err != nil {
			return BoundProgram{}, err
		}
		authorization := postgresDMLMatrixAuthorization(enrollment)
		authorization.DatasourceID = request.Identity.DatasourceIdentity
		for index := range authorization.Policies {
			authorization.Policies[index].DatasourceID = request.Identity.DatasourceIdentity
		}
		prepared, decision, err := executor.PrepareBoundPostgresDML(ctx, nativeTx, request.RawSQL, &enrollment,
			authorization, &unlimitedPostgresBudget{})
		if err != nil {
			return BoundProgram{}, err
		}
		defer func() {
			if closeErr := prepared.Close(context.Background()); resultErr == nil && closeErr != nil {
				resultErr = closeErr
			}
		}()
		if !decision.Allowed() {
			return BoundProgram{}, NewCapabilityFailure("AUTH_DML_AUTHORIZATION_DENY")
		}
		enrollment.Manifest = prepared.Manifest()
		facts, err := NativeDMLSemanticFacts(request.Identity.DatasourceIdentity, request.Identity, enrollment)
		if err != nil {
			return BoundProgram{}, err
		}
		digest, err := facts.Digest()
		if err != nil {
			return BoundProgram{}, err
		}
		// The production DML binder has its own B5 attestation envelope. Shadow
		// comparison consumes only the shared semantic facts, so do not route the
		// DML manifest through the SELECT-specific NativeBoundProgram attestation.
		return BoundProgram{Mode: BinderModeNativeCV1, Facts: facts, SemanticFactsDigest: digest}, nil
	}
}
