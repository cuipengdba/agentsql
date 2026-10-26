package authorizedexecute

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This test uses an isolated PostgreSQL 16 instance with the attested native
// binder. It is opt-in because most developer machines have stock PostgreSQL.
func TestNativePostgresPolicyBindingHoldsAndReleasesCatalogLease(t *testing.T) {
	if os.Getenv("AGENTSQL_NATIVE_POLICY_BIND_E2E") != "1" {
		t.Skip("set AGENTSQL_NATIVE_POLICY_BIND_E2E=1 with an isolated binder database")
	}
	host, user, password, database := os.Getenv("AGENTSQL_NATIVE_PG_HOST"), os.Getenv("AGENTSQL_NATIVE_PG_USER"), os.Getenv("AGENTSQL_NATIVE_PG_PASSWORD"), os.Getenv("AGENTSQL_NATIVE_PG_DATABASE")
	if host == "" || user == "" || password == "" || database == "" {
		t.Fatal("native policy binding test database configuration is incomplete")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable", user, password, host, database)
	connection, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer connection.Close(context.Background())
	_, err = connection.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS v420_policy_probe;
CREATE TABLE IF NOT EXISTS v420_policy_probe.sales(region text, amount integer);
TRUNCATE v420_policy_probe.sales;
INSERT INTO v420_policy_probe.sales VALUES ('east',10),('west',20)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cleanupErr := pgx.Connect(context.Background(), dsn)
		if cleanupErr == nil {
			_, _ = cleanup.Exec(context.Background(), `DROP SCHEMA IF EXISTS v420_policy_probe CASCADE`)
			_ = cleanup.Close(context.Background())
		}
	})
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := store.NewPasswordCipher(secret)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(password)
	require.NoError(t, err)
	datasource := model.Datasource{ID: "v420-native-policy-probe", DBType: "postgres", Host: host,
		Port: 5432, Database: database, Username: user, PasswordEnc: encrypted,
		ConnLimit: 4, StmtTimeoutMS: 5000, RowLimit: 100}
	gateway := NewGateway(false)
	defer gateway.CloseAll()
	const sqlText = `SELECT region, pg_catalog.sum(amount) FROM v420_policy_probe.sales GROUP BY region`
	discovered, err := gateway.EnrollPostgresPolicySelect(ctx, datasource, secret, sqlText, DefaultLimits)
	require.NoError(t, err)
	require.Equal(t, "NATIVE_C_V1", discovered.Mode)
	require.Len(t, discovered.Relations, 1)
	lease, err := gateway.BindPostgresPolicySelect(ctx, datasource, secret, sqlText, DefaultLimits)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(discovered, lease.Enrollment), "discovery and locked native identities must match")
	require.NoError(t, lease.Close())
	require.NoError(t, lease.Close())
	var rows int
	require.NoError(t, connection.QueryRow(ctx, `SELECT count(*) FROM v420_policy_probe.sales`).Scan(&rows))
	require.Equal(t, 2, rows, "policy binding must never execute the SELECT or alter business rows")
}
