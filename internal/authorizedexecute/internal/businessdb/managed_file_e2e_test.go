package businessdb

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Run only against a disposable PostgreSQL database. Existing managed schemas
// are never touched by this test.
func TestManagedFileStagePostgresIsolation(t *testing.T) {
	dsn := os.Getenv("AGENTSQL_MANAGED_FILE_PG_URL")
	if dsn == "" {
		t.Skip("set AGENTSQL_MANAGED_FILE_PG_URL to a disposable PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	password, _ := u.User.Password()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer connection.Close(context.Background())
	var schemaExists bool
	require.NoError(t, connection.QueryRow(ctx, `SELECT to_regnamespace($1) IS NOT NULL`, ManagedFileSchema).Scan(&schemaExists))
	if schemaExists {
		t.Skip("refusing to touch an existing managed file schema")
	}
	_, err = connection.Exec(ctx, `CREATE SCHEMA agentsql_files`)
	require.NoError(t, err)
	operation, err := NewManagedFileOperationID()
	require.NoError(t, err)
	name, err := ManagedFileName(operation, 0)
	require.NoError(t, err)
	reader := "agentsql_reader_" + name[len("agentsql_file_"):len("agentsql_file_")+12]
	roleCreated := false
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if roleCreated {
			_, _ = connection.Exec(cleanup, `REVOKE postgres FROM `+pgx.Identifier{reader}.Sanitize())
			_, _ = connection.Exec(cleanup, `ALTER DEFAULT PRIVILEGES IN SCHEMA agentsql_files REVOKE SELECT ON TABLES FROM `+pgx.Identifier{reader}.Sanitize())
			_, _ = connection.Exec(cleanup, `REVOKE USAGE ON SCHEMA agentsql_files FROM `+pgx.Identifier{reader}.Sanitize())
		}
		_, _ = connection.Exec(cleanup, `DROP SCHEMA agentsql_files CASCADE`)
		if roleCreated {
			_, _ = connection.Exec(cleanup, `DROP ROLE `+pgx.Identifier{reader}.Sanitize())
		}
	}()
	_, err = connection.Exec(ctx, `CREATE ROLE `+pgx.Identifier{reader}.Sanitize()+` NOLOGIN`)
	require.NoError(t, err)
	roleCreated = true
	_, err = connection.Exec(ctx, `GRANT USAGE ON SCHEMA agentsql_files TO `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	datasource := model.Datasource{ID: "managed-file-test", DBType: "postgres", Host: u.Hostname(), Port: port,
		Database: u.Path[1:], Username: u.User.Username(), StmtTimeoutMS: 10000}
	sheets := []ManagedFileSheet{{Columns: []ManagedFileColumn{{Name: "region", Type: "string"}, {Name: "amount", Type: "int"}},
		Rows: [][]any{{"east", int64(10)}, {"west", int64(20)}}},
		{Columns: []ManagedFileColumn{{Name: "sold_at", Type: "datetime"}}, Rows: [][]any{{time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}}}}
	tables, err := StageManagedFiles(ctx, datasource, password, reader, operation, sheets)
	require.NoError(t, err)
	require.Len(t, tables, 2)
	for index, table := range tables {
		require.Equal(t, ManagedFileSchema, table.Schema)
		require.Regexp(t, managedFileTableName, table.Name)
		require.Equal(t, len(sheets[index].Rows), table.Rows)
		qualified := pgx.Identifier{table.Schema, table.Name}.Sanitize()
		var count int
		require.NoError(t, connection.QueryRow(ctx, `SELECT count(*) FROM `+qualified).Scan(&count))
		require.Equal(t, table.Rows, count)
		_, err = connection.Exec(ctx, `SET ROLE `+pgx.Identifier{reader}.Sanitize())
		require.NoError(t, err)
		_, denied := connection.Exec(ctx, `SELECT * FROM `+qualified)
		_, resetErr := connection.Exec(ctx, `RESET ROLE`)
		require.NoError(t, resetErr)
		var pgError *pgconn.PgError
		require.ErrorAs(t, denied, &pgError)
		require.Equal(t, "42501", pgError.Code)
	}
	// PostgreSQL default privileges can silently grant a new table to a
	// reader. The staging transaction must detect and roll back that table.
	_, err = connection.Exec(ctx, `ALTER DEFAULT PRIVILEGES IN SCHEMA agentsql_files GRANT SELECT ON TABLES TO `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	otherOperation, err := NewManagedFileOperationID()
	require.NoError(t, err)
	_, err = StageManagedFiles(ctx, datasource, password, reader, otherOperation, sheets[:1])
	require.ErrorIs(t, err, ErrManagedFileInvalid)
	_, err = connection.Exec(ctx, `ALTER DEFAULT PRIVILEGES IN SCHEMA agentsql_files REVOKE SELECT ON TABLES FROM `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	_, err = connection.Exec(ctx, `GRANT postgres TO `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	otherOperation, err = NewManagedFileOperationID()
	require.NoError(t, err)
	_, err = StageManagedFiles(ctx, datasource, password, reader, otherOperation, sheets[:1])
	require.ErrorIs(t, err, ErrManagedFileInvalid, "inherited owner access must roll back the new table")
	var count int
	require.NoError(t, connection.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r'`, ManagedFileSchema).Scan(&count))
	require.Equal(t, 2, count, "rejected default grant must not leave a table")
	require.NoError(t, DropManagedFiles(ctx, datasource, password, operation, len(sheets)))
	require.NoError(t, DropManagedFiles(ctx, datasource, password, operation, len(sheets)), "cleanup retry is idempotent")
	require.NoError(t, connection.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r'`, ManagedFileSchema).Scan(&count))
	require.Zero(t, count)
}
