package bootstrap

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This test only runs against a disposable PostgreSQL instance selected by the
// caller. It refuses to touch an existing managed schema or reader role.
func TestManagedFilePublishPostgresEndToEnd(t *testing.T) {
	dsn := os.Getenv("AGENTSQL_MANAGED_FILE_PG_URL")
	if dsn == "" {
		t.Skip("set AGENTSQL_MANAGED_FILE_PG_URL to a disposable PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	password, _ := u.User.Password()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close(context.Background())
	var existing bool
	require.NoError(t, admin.QueryRow(ctx, `SELECT to_regnamespace('agentsql_files') IS NOT NULL OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='file_reader')`).Scan(&existing))
	if existing {
		t.Skip("refusing to touch an existing managed schema or reader role")
	}
	_, err = admin.Exec(ctx, `CREATE ROLE file_reader LOGIN PASSWORD 'reader-password'`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `CREATE SCHEMA agentsql_files`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `GRANT USAGE ON SCHEMA agentsql_files TO file_reader`)
	require.NoError(t, err)
	runtime, principal, binding, _ := managedFileStageFixture(t)
	runtime.secret = append([]byte(nil), bootstrapTestSecret...)
	runtime.business = executor.NewGateway(false)
	defer func() {
		_ = runtime.business.CloseAll()
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_, _ = admin.Exec(cleanup, `DROP SCHEMA agentsql_files CASCADE`)
		_, _ = admin.Exec(cleanup, `DROP ROLE file_reader`)
	}()
	reader, err := runtime.Store.Datasources().Get(ctx, binding.DatasourceID)
	require.NoError(t, err)
	reader.Host, reader.Port, reader.Database = u.Hostname(), port, strings.TrimPrefix(u.Path, "/")
	reader, err = runtime.Store.Datasources().Update(ctx, reader, "reader-password")
	require.NoError(t, err)
	writer := reader
	writer.ID = "managed-provisioner"
	writer.Username = u.User.Username()
	physical, err := executor.NewManagedFileProvisioner(reader, writer, password)
	require.NoError(t, err)
	sheets := []executor.ManagedFileSheet{{Columns: []executor.ManagedFileColumn{{Name: "region", Type: "string"}, {Name: "amount", Type: "int"}},
		Rows: [][]any{{"east", int64(10)}, {"west", int64(20)}}}}
	staged, tables, err := runtime.StageSQLBotFile(ctx, principal, binding, strings.Repeat("a", 64), 64, sheets, physical)
	require.NoError(t, err)
	published, err := runtime.PublishSQLBotFile(ctx, principal, binding, staged, tables, sheets, physical)
	require.NoError(t, err)
	require.Equal(t, "published", published.Status)
	visible, err := runtime.Store.ManagedFileImports().PublishedTableOwner(ctx, binding.ID, binding.DatasourceID,
		binding.AgentID, binding.AgentSQLUserID, tables[0].Name)
	require.NoError(t, err)
	require.True(t, visible)
	readerConn, err := pgx.Connect(ctx, "postgres://file_reader:reader-password@"+u.Host+u.Path+"?sslmode=disable")
	require.NoError(t, err)
	defer readerConn.Close(context.Background())
	var count int
	require.NoError(t, readerConn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{"agentsql_files", tables[0].Name}.Sanitize()).Scan(&count))
	require.Equal(t, 2, count)
}
