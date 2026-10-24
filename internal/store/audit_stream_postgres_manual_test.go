package store

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// Set both DSNs to isolated empty PostgreSQL databases. This exercises the
// combined and independent audit migration streams against a real server.
func TestAuditStreamPostgresManual(t *testing.T) {
	for _, testCase := range []struct {
		name, variable string
		migrate        func(context.Context, *sql.DB, Dialect) error
	}{
		{"combined", "AGENTSQL_TEST_AUDIT_STREAM_COMBINED_DSN", Migrate},
		{"audit-only", "AGENTSQL_TEST_AUDIT_STREAM_SEPARATE_DSN", MigrateAudit},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dsn := os.Getenv(testCase.variable)
			if dsn == "" {
				t.Skipf("set %s to run PostgreSQL audit stream acceptance", testCase.variable)
			}
			ctx := t.Context()
			db, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			defer db.Close()
			require.NoError(t, testCase.migrate(ctx, db, DialectPostgres))
			require.NoError(t, testCase.migrate(ctx, db, DialectPostgres))
			if testCase.name == "combined" {
				_, err = db.ExecContext(ctx, "INSERT INTO tenants(id,name,status) VALUES('tenant_other','Other','active') ON CONFLICT(id) DO NOTHING")
				require.NoError(t, err)
			}
			repo := &AuditLogRepository{repositoryBase: repositoryBase{db: db, dialect: DialectPostgres}}
			own, err := WithTenant(ctx, DefaultTenantID)
			require.NoError(t, err)
			other, err := WithTenant(ctx, "tenant_other")
			require.NoError(t, err)
			first, err := repo.Insert(own, model.AuditLog{Decision: "allow"})
			require.NoError(t, err)
			_, err = repo.Insert(other, model.AuditLog{Decision: "deny"})
			require.NoError(t, err)
			second, err := repo.Insert(own, model.AuditLog{Decision: "deny"})
			require.NoError(t, err)
			latest, err := repo.LatestStreamSequence(own)
			require.NoError(t, err)
			require.Equal(t, int64(2), latest)
			items, err := repo.StreamAfter(own, 0, 10)
			require.NoError(t, err)
			require.Len(t, items, 2)
			require.Equal(t, first.ID, items[0].Audit.ID)
			require.Equal(t, second.ID, items[1].Audit.ID)
			require.Equal(t, int64(1), items[0].Sequence)
			require.Equal(t, int64(2), items[1].Sequence)
			items, err = repo.StreamAfter(other, 0, 10)
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.Equal(t, int64(1), items[0].Sequence)
			require.NotEqual(t, first.ID, items[0].Audit.ID)
		})
	}
}
