package adminapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestB5AdminPostgres14And18SessionsTransactionsQuarantineReconciliationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:14/18 B5 admin API integration test")
	}
	for _, image := range []string{"postgres:14", "postgres:18"} {
		image := image
		t.Run(image, func(t *testing.T) {
			ctx := adminDockerTestContext(t)
			container, err := postgrescontainer.Run(ctx, image, postgrescontainer.WithDatabase("agentsql_b5_admin"), postgrescontainer.WithUsername("agentsql"), postgrescontainer.WithPassword("b5-admin-password"), postgrescontainer.BasicWaitStrategies())
			if err != nil {
				if container != nil {
					testcontainers.CleanupContainer(t, container)
				}
				require.NoError(t, err)
			}
			testcontainers.CleanupContainer(t, container)
			host, err := container.Host(ctx)
			require.NoError(t, err)
			port, err := container.MappedPort(ctx, "5432/tcp")
			require.NoError(t, err)
			dsn := fmt.Sprintf("postgres://agentsql:b5-admin-password@%s:%s/agentsql_b5_admin?sslmode=disable", host, port.Port())
			opened, err := store.OpenMetadata(ctx, store.MetadataOptions{Driver: store.DialectPostgres, PostgresDSN: dsn, MaxOpenConns: 8, MaxIdleConns: 4, AutoMigrate: true}, []byte(adminTestSecret))
			require.NoError(t, err)

			agent, err := opened.Agents().Create(ctx, model.Agent{ID: "b5-admin-agent", Name: "B5 Admin", Status: "active", APIKeyHash: strings.Repeat("a", 64), Level: "dml"})
			require.NoError(t, err)
			datasource, err := opened.Datasources().Create(ctx, model.Datasource{ID: "b5-admin-ds", Name: "B5 Admin PG", DBType: "postgres", Host: "db", Port: 5432, Database: "app", Username: "user", ConnLimit: 10, StmtTimeoutMS: 5000, RowLimit: 100}, "password")
			require.NoError(t, err)
			now := time.Now().UTC().Truncate(time.Microsecond)
			_, err = opened.B5Sessions().Create(ctx, store.B5Session{SessionID: "session-e2e", AgentID: agent.ID, TenantID: "tenant", PrincipalID: "principal", OwnerInstanceID: "owner", OwnerEpoch: 2, ContinuationSchemaID: b5.ContinuationProofSchemaID, ContinuationSchemaVersion: b5.ContinuationProofVersion, ContinuationKeyCiphertext: "ciphertext", ContinuationHMACDigest: bytesOfAdmin(32, 1), StickyRoute: "owner", Status: b5.SessionActive, IdleExpiresAt: now.Add(time.Minute), AbsoluteExpiresAt: now.Add(time.Hour)})
			require.NoError(t, err)
			_, err = opened.B5Transactions().Create(ctx, store.B5Transaction{TransactionID: "tx-e2e", SessionID: "session-e2e", DatasourceID: datasource.ID, Status: b5.TransactionActive, Phase: b5.PhaseActive, PlanDigest: bytesOfAdmin(32, 2), OwnerEpoch: 2, IdleDeadline: now.Add(time.Minute), WallDeadline: now.Add(time.Hour)})
			require.NoError(t, err)

			live := &fakeB5Admin{status: B5StatusView{State: "ACTIVE", Reason: "B5_READY", Ready: true}, quarantine: B5QuarantinePage{Total: 1, List: []B5QuarantineView{{LeaseID: "lease-e2e", DatasourceID: datasource.ID, Reason: "DISCARD_UNCONFIRMED", ChargedSlots: 1}}}, reconciliation: B5ReconciliationPage{Total: 1, List: []B5ReconciliationView{{EventUUID: "event-e2e", TransactionID: "tx-e2e", Classification: "COMMITTED_AUDIT_PENDING"}}}, metrics: B5MetricsView{QuarantineCount: 1, QuarantineChargedSlots: 1, HardBudget: 100, QuarantineBudgetRatio: .01}}
			backend, err := NewStoreB5Admin(opened, live)
			require.NoError(t, err)
			runtime := &bootstrap.Runtime{Store: opened, ManagementAudit: audit.NewRecorder(opened.AuditLogs())}
			handler, err := NewHandler(Deps{Runtime: runtime, Config: adminTestConfig(filepath.Join(t.TempDir(), "unused.db")), AdminUsername: "admin", AdminPassword: "password", TokenKey: DeriveTokenKey([]byte(adminTestSecret)), B5Admin: backend}, zerolog.Nop())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Close()) })
			token, _, err := issueAdminToken(DeriveTokenKey([]byte(adminTestSecret)), time.Now(), "b5-e2e")
			require.NoError(t, err)
			call := func(method, path, body string) string {
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				return response.Body.String()
			}
			require.Contains(t, call(http.MethodGet, "/api/v1/b5/sessions?status=ACTIVE", ""), `"id":"session-e2e"`)
			require.Contains(t, call(http.MethodGet, "/api/v1/b5/transactions?datasource_id=b5-admin-ds", ""), `"id":"tx-e2e"`)
			require.Contains(t, call(http.MethodGet, "/api/v1/b5/quarantine", ""), `"lease_id":"lease-e2e"`)
			require.Contains(t, call(http.MethodGet, "/api/v1/b5/reconciliation", ""), `"classification":"COMMITTED_AUDIT_PENDING"`)
			call(http.MethodPost, "/api/v1/b5/quarantine/lease-e2e/confirm-discard", `{"confirm":true}`)
			call(http.MethodPost, "/api/v1/b5/reconciliation", `{"transaction_id":"tx-e2e","confirm":true}`)
			require.Equal(t, 1, live.discardCalls)
			require.Equal(t, 1, live.reconcileCalls)
			audits, err := opened.AuditLogs().Page(ctx, 1, 10)
			require.NoError(t, err)
			require.GreaterOrEqual(t, audits.Total, int64(2))
		})
	}
}

var _ B5RuntimePlane = (*fakeB5Admin)(nil)
