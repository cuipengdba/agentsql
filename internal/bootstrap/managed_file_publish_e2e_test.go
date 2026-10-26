package bootstrap

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type failDropOnceManagedFilePhysical struct {
	ManagedFilePhysical
	failed bool
}

func (physical *failDropOnceManagedFilePhysical) Drop(ctx context.Context, id string, count int) error {
	if !physical.failed {
		physical.failed = true
		return errors.New("injected managed-file drop failure")
	}
	return physical.ManagedFilePhysical.Drop(ctx, id, count)
}

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
	writerPassword := "writer-password"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close(context.Background())
	var existing bool
	require.NoError(t, admin.QueryRow(ctx, `SELECT to_regnamespace('agentsql_files') IS NOT NULL OR EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname IN ('file_reader','file_writer'))`).Scan(&existing))
	if existing {
		t.Skip("refusing to touch an existing managed schema or reader role")
	}
	_, err = admin.Exec(ctx, `CREATE ROLE file_reader LOGIN PASSWORD 'reader-password'`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `CREATE ROLE file_writer LOGIN PASSWORD 'writer-password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `CREATE SCHEMA agentsql_files AUTHORIZATION file_writer`)
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
		_, _ = admin.Exec(cleanup, `DROP ROLE file_writer`)
		_, _ = admin.Exec(cleanup, `DROP ROLE file_reader`)
	}()
	reader, err := runtime.Store.Datasources().Get(ctx, binding.DatasourceID)
	require.NoError(t, err)
	reader.Host, reader.Port, reader.Database = u.Hostname(), port, strings.TrimPrefix(u.Path, "/")
	reader, err = runtime.Store.Datasources().Update(ctx, reader, "reader-password")
	require.NoError(t, err)
	writer := reader
	writer.ID = "managed-provisioner"
	writer.Username = "file_writer"
	physical, err := executor.NewManagedFileProvisioner(reader, writer, writerPassword)
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
	foreign := principal
	foreign.UserID = "another-user"
	require.ErrorIs(t, runtime.DeleteSQLBotFile(ctx, foreign, binding, published.ID, physical), ErrManagedFileStage)
	stale := binding
	stale.Revision++
	require.ErrorIs(t, runtime.DeleteSQLBotFile(ctx, principal, stale, published.ID, physical), ErrManagedFileStage)
	visible, err = runtime.Store.ManagedFileImports().PublishedTableOwner(ctx, binding.ID, binding.DatasourceID,
		binding.AgentID, binding.AgentSQLUserID, tables[0].Name)
	require.NoError(t, err)
	require.True(t, visible)

	// A physical failure after the audited deletion fence must remove query
	// visibility immediately and survive a process restart for retry.
	failing := &failDropOnceManagedFilePhysical{ManagedFilePhysical: physical}
	err = runtime.DeleteSQLBotFile(ctx, principal, binding, published.ID, failing)
	require.ErrorContains(t, err, "injected managed-file drop failure")
	visible, err = runtime.Store.ManagedFileImports().PublishedTableOwner(ctx, binding.ID, binding.DatasourceID,
		binding.AgentID, binding.AgentSQLUserID, tables[0].Name)
	require.NoError(t, err)
	require.False(t, visible)
	cleaned, err := runtime.RecoverUnpublishedSQLBotFiles(ctx, store.DefaultTenantID, physical)
	require.NoError(t, err)
	require.Equal(t, 1, cleaned)
	current, err := runtime.Store.ManagedFileImports().Get(ctx, published.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", current.Status)
	var stillExists bool
	require.NoError(t, admin.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "agentsql_files."+tables[0].Name).Scan(&stillExists))
	require.False(t, stillExists)
}
