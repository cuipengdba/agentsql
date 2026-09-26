package authorizedexecute

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestAuthorizedSelectS4PostgresMatrix(t *testing.T) {
	for _, major := range []string{"14", "15", "16", "17", "18"} {
		t.Run("pg"+major, func(t *testing.T) { runAuthorizedSelectS4Postgres(t, major) })
	}
}

func runAuthorizedSelectS4Postgres(t *testing.T, major string) {
	t.Helper()
	ctx := s4DockerTestContext(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-s4-test", Tag: "pg" + major, BuildArgs: map[string]*string{"PG_MAJOR": &major}, KeepImage: true},
			Env:            map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
			ExposedPorts:   []string{"5432/tcp"},
			WaitingFor:     wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second),
		}, Started: true,
	})
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := store.NewPasswordCipher(secret)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt("agentsql-password")
	require.NoError(t, err)
	datasource := model.Datasource{ID: "s4-pg-" + major, DBType: "postgres", Host: host, Port: port.Int(), Database: "agentsql", Username: "agentsql", PasswordEnc: encrypted, ConnLimit: 8, StmtTimeoutMS: 5_000, RowLimit: 100}
	gateway := NewGateway(false)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	for _, statement := range []string{
		`CREATE SCHEMA agentsql_catalog`,
		`CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`,
		`CREATE SCHEMA s4`,
		`CREATE TABLE s4.a(id integer, phone text)`,
		`CREATE TABLE s4.b(id integer, note text)`,
		`CREATE VIEW s4.v AS SELECT id,phone FROM s4.a`,
		`CREATE VIEW s4.v2 AS SELECT id,phone FROM s4.v`,
		`INSERT INTO s4.a VALUES (1,'13812345678'),(2,'13987654321')`,
		`INSERT INTO s4.b VALUES (1,'x'),(2,'y')`,
		`CREATE MATERIALIZED VIEW s4.mv AS SELECT id, phone AS phone_alias, length(phone) AS phone_len FROM s4.v WITH DATA`,
	} {
		statementCapability, err := gateway.AuthorizedExecute(ctx, datasource, secret, statement, "")
		require.NoError(t, err, statement)
		_, err = statementCapability.Execute(ctx)
		require.NoError(t, err, statement)
		require.NoError(t, statementCapability.Close())
	}

	t.Run("protocol3-activation-gate", func(t *testing.T) {
		control, err := store.OpenWithSecret(ctx, filepath.Join(t.TempDir(), "control.db"), secret)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, control.Close()) })
		stored := datasource
		stored.PasswordEnc = ""
		stored, err = control.Datasources().Create(ctx, stored, "agentsql-password")
		require.NoError(t, err)
		capability, err := gateway.ProbePostgresB2Capability(ctx, stored, secret)
		require.NoError(t, err)
		wantMajor, err := strconv.Atoi(major)
		require.NoError(t, err)
		require.Equal(t, wantMajor, capability.ServerMajor)
		require.Equal(t, "agentsql-binder-4.2", capability.ABI)
		require.True(t, capability.Matview)
		require.NoError(t, gateway.ProbeReservation(stored.ID))
		metadataReadiness, err := control.Fence().Protocol3Readiness(ctx)
		require.NoError(t, err)
		require.True(t, metadataReadiness.Ready())
		now := time.Now().UTC()
		instance, err := control.Fence().ActivateProtocol3(ctx, store.Protocol3Activation{
			InstanceID: "s5-pg-" + major, ArtifactDigest: capability.ExtensionHash,
			BinderReady: true, CatalogReady: true, ReservationReady: true,
			Now: now, Lease: time.Minute,
		})
		require.NoError(t, err)
		instance, err = control.Fence().Heartbeat(ctx, instance, now.Add(time.Second), time.Minute)
		require.NoError(t, err)
		snapshot, err := control.Fence().BeginRead(ctx, 3, instance.InstanceID, now.Add(time.Second))
		require.NoError(t, err)
		require.NoError(t, snapshot.FinalCheck(ctx, now.Add(2*time.Second)))
		require.NoError(t, snapshot.Close())
		require.NoError(t, control.Fence().DrainRuntime(ctx, instance.InstanceID))
		_, err = control.Fence().BeginRead(ctx, 3, instance.InstanceID, now.Add(2*time.Second))
		require.ErrorIs(t, err, store.ErrFenceLost, "drained protocol-3 runtime must be inactive")

		expiring, err := control.Fence().ActivateProtocol3(ctx, store.Protocol3Activation{
			InstanceID: "s5-expiring-pg-" + major, ArtifactDigest: capability.ExtensionHash,
			BinderReady: true, CatalogReady: true, ReservationReady: true,
			Now: now.Add(2 * time.Second), Lease: time.Second,
		})
		require.NoError(t, err)
		_, err = control.Fence().BeginRead(ctx, 3, expiring.InstanceID, now.Add(4*time.Second))
		require.ErrorIs(t, err, store.ErrFenceLost, "expired protocol-3 runtime must fail closed")
		_, err = control.Fence().Heartbeat(ctx, expiring, now.Add(4*time.Second), time.Minute)
		require.ErrorIs(t, err, store.ErrFenceLost, "expired protocol-3 runtime cannot resurrect")
	})

	joinSQL := `SELECT a.phone FROM s4.a a JOIN s4.b b ON a.id=b.id WHERE b.note='x' ORDER BY a.id`
	enrollment, err := gateway.EnrollPostgresSelect(ctx, datasource, secret, joinSQL, DefaultLimits)
	require.NoError(t, err)
	policies := policiesForEnrollment(datasource.ID, enrollment)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "s4", Table: "a", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)

	t.Run("join-reference-mask-p0e", func(t *testing.T) {
		tracked := lockrank.WithTracker(ctx)
		control, err := lockrank.Acquire(tracked, lockrank.Control)
		require.NoError(t, err)
		defer control.Release()
		var phases []SelectPhase
		var recorded ColumnAuthorizationAudit
		selected, err := executeColumnAuthorized(tracked, gateway, datasource, secret, joinSQL, ColumnAuthorizationRequest{
			Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Policies: policies,
			Redactor: redactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "revision",
			Observe: func(phase SelectPhase) { phases = append(phases, phase) },
			DurableAudit: func(_ context.Context, audit ColumnAuthorizationAudit, candidate *model.QueryResult, _ mask.RedactReport) error {
				require.NotNil(t, candidate)
				recorded = audit
				return nil
			},
			FinalFence: func(context.Context) error { return nil },
		})
		require.NoError(t, err)
		require.True(t, selected.Allowed)
		require.Equal(t, "138****5678", selected.Result.Rows[0][0])
		require.Equal(t, 1, selected.Redact.MaskedCells)
		require.True(t, selected.Seal.Verify(selected.Encoded))
		require.Equal(t, ColumnAuditVersion, recorded.Version)
		require.Equal(t, []SelectPhase{PhaseBusinessBegin, PhaseExecute, PhaseMask, PhaseEncode, PhaseFpost, PhaseBusinessEnd, PhaseAudit, PhaseFinalFence, PhaseSeal}, phases)
	})

	t.Run("where-on-order-reference-needs-grant", func(t *testing.T) {
		missing := removeUsage(policies, "reference")
		var audited ColumnAuthorizationAudit
		selected, err := executeColumnAuthorized(ctx, gateway, datasource, secret, joinSQL, ColumnAuthorizationRequest{
			Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Policies: missing,
			Redactor: redactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "revision",
			DurableAudit: func(_ context.Context, audit ColumnAuthorizationAudit, candidate *model.QueryResult, _ mask.RedactReport) error {
				require.Nil(t, candidate)
				audited = audit
				return nil
			}, FinalFence: func(context.Context) error { return nil },
		})
		require.NoError(t, err)
		require.False(t, selected.Allowed)
		require.Equal(t, ReasonColumnGrantMissing, selected.Reason)
		require.Equal(t, "deny", audited.Decision)
	})

	for name, sqlText := range map[string]string{
		"matview-allow":  `SELECT phone_alias FROM s4.mv WHERE id=1`,
		"self-join":      `SELECT left_a.phone FROM s4.a left_a JOIN s4.a right_a ON left_a.id=right_a.id WHERE right_a.id=1`,
		"two-level-view": `SELECT phone FROM s4.v2 WHERE id=1`,
	} {
		t.Run(name, func(t *testing.T) {
			enrolled, err := gateway.EnrollPostgresSelect(ctx, datasource, secret, sqlText, DefaultLimits)
			require.NoError(t, err)
			selected, err := executeColumnAuthorized(ctx, gateway, datasource, secret, sqlText, ColumnAuthorizationRequest{
				Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Policies: policiesForEnrollment(datasource.ID, enrolled),
				Redactor: redactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "revision",
				DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
					return nil
				},
				FinalFence: func(context.Context) error { return nil },
			})
			require.NoError(t, err)
			require.True(t, selected.Allowed, "reason=%s audit=%+v", selected.Reason, selected.Audit)
			require.NotEmpty(t, selected.Result.Rows)
		})
	}

	t.Run("matview-deny-without-output-grant", func(t *testing.T) {
		const sqlText = `SELECT phone_alias FROM s4.mv WHERE id=1`
		enrolled, err := gateway.EnrollPostgresSelect(ctx, datasource, secret, sqlText, DefaultLimits)
		require.NoError(t, err)
		selected, err := executeColumnAuthorized(ctx, gateway, datasource, secret, sqlText, ColumnAuthorizationRequest{
			Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Policies: removeUsage(policiesForEnrollment(datasource.ID, enrolled), "output"),
			Redactor: redactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "revision",
			DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
				return nil
			},
			FinalFence: func(context.Context) error { return nil },
		})
		require.NoError(t, err)
		require.False(t, selected.Allowed)
		require.Equal(t, ReasonColumnGrantMissing, selected.Reason)
	})

	t.Run("post-audit-fence-failure-delivers-nothing", func(t *testing.T) {
		selected, err := executeColumnAuthorized(ctx, gateway, datasource, secret, joinSQL, ColumnAuthorizationRequest{
			Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Policies: policies,
			Redactor: redactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "revision",
			DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
				return nil
			},
			FinalFence: func(context.Context) error { return errors.New("fence unavailable") },
		})
		require.Error(t, err)
		require.Empty(t, selected.Encoded)
		require.Empty(t, selected.Result.Rows)
	})

	t.Run("audit-barrier-failure-delivers-nothing", func(t *testing.T) {
		selected, err := executeColumnAuthorized(ctx, gateway, datasource, secret, joinSQL, ColumnAuthorizationRequest{
			Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Policies: policies,
			Redactor: redactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "revision",
			DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
				return errors.New("audit unavailable")
			},
			FinalFence: func(context.Context) error { return nil },
		})
		require.Error(t, err)
		require.Empty(t, selected.Encoded)
		require.Empty(t, selected.Result.Rows)
	})

	t.Run("source-free-proof", func(t *testing.T) {
		emptyRedactor, err := mask.NewRedactor(nil)
		require.NoError(t, err)
		selected, err := executeColumnAuthorized(ctx, gateway, datasource, secret, `SELECT 1 AS one`, ColumnAuthorizationRequest{
			Agent:    model.Agent{ID: "agent", Status: "active", Level: "readonly"},
			Redactor: emptyRedactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "revision",
			DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
				return nil
			},
			FinalFence: func(context.Context) error { return nil },
		})
		require.NoError(t, err)
		require.True(t, selected.Allowed)
		require.Equal(t, [][]string{{"1"}}, selected.Result.Rows)
	})
}

func s4DockerTestContext(t *testing.T) context.Context {
	t.Helper()
	available := false
	defer func() {
		if recovered := recover(); recovered != nil && !available {
			t.Skipf("docker unavailable: %v", recovered)
		}
	}()
	client, err := testcontainers.NewDockerClient()
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	defer client.Close()
	probe, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Ping(probe); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	available = true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func executeColumnAuthorized(ctx context.Context, gateway *Gateway, datasource model.Datasource, secret []byte, sqlText string, request ColumnAuthorizationRequest) (AuthorizedSelectResult, error) {
	statement, err := gateway.AuthorizedExecute(WithColumnAuthorization(ctx, request), datasource, secret, sqlText, "")
	if err != nil {
		return AuthorizedSelectResult{}, err
	}
	column, ok := statement.(ColumnAuthorizedStatement)
	if !ok {
		_ = statement.Close()
		return AuthorizedSelectResult{}, fmt.Errorf("AuthorizedExecute did not route SELECT through column authorization")
	}
	_, executeErr := column.Execute(ctx)
	result, resultOK := column.ColumnAuthorizationResult()
	closeErr := column.Close()
	if executeErr != nil || closeErr != nil {
		return AuthorizedSelectResult{}, errors.Join(executeErr, closeErr)
	}
	if !resultOK {
		return AuthorizedSelectResult{}, fmt.Errorf("column authorization outcome unavailable")
	}
	return result, nil
}

func policiesForEnrollment(datasourceID string, enrollment PostgresRelationEnrollment) []model.Policy {
	policies := []model.Policy{{ID: "broad", AgentID: "agent", DatasourceID: datasourceID, ObjectType: "schema", ObjectName: "s4.*", Action: "allow", Revision: 1}}
	columns := make(map[uint32][]PostgresEnrolledColumn)
	for _, column := range enrollment.Columns {
		columns[column.RelationOID] = append(columns[column.RelationOID], column)
	}
	for index, relation := range enrollment.Relations {
		id := fmt.Sprintf("binding-%d", index)
		stable, catalog := relation.StableObjectID, relation.CatalogFingerprint
		bindingID := id
		policy := model.Policy{ID: id, AgentID: "agent", DatasourceID: datasourceID, ObjectType: "column", ObjectName: relation.Schema + "." + relation.Name, Action: "allow", Revision: 2,
			RelationBindingID: &bindingID,
			RelationBinding:   &model.RelationPolicyBinding{ID: id, PolicyID: id, DatasourceID: datasourceID, SchemaName: relation.Schema, RelationName: relation.Name, StableObjectID: &stable, CatalogFingerprint: &catalog, Status: "healthy", Revision: 1}}
		for _, column := range columns[relation.RelationOID] {
			for _, usage := range []string{"output", "reference"} {
				policy.ColumnPermissions = append(policy.ColumnPermissions, model.PolicyColumnPermission{PolicyID: id, RelationEnrollmentID: id, ColumnOrdinal: int(column.Attnum), ColumnName: column.Name, ColumnTypeDigest: column.TypeDigest, Usage: usage, ParentRevision: 2})
			}
		}
		policies = append(policies, policy)
	}
	return policies
}

func removeUsage(policies []model.Policy, usage string) []model.Policy {
	result := append([]model.Policy(nil), policies...)
	for index := range result {
		result[index].ColumnPermissions = append([]model.PolicyColumnPermission(nil), result[index].ColumnPermissions...)
		filtered := result[index].ColumnPermissions[:0]
		for _, permission := range result[index].ColumnPermissions {
			if permission.Usage != usage {
				filtered = append(filtered, permission)
			}
		}
		result[index].ColumnPermissions = filtered
	}
	return result
}
