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
	writerPassword := "writer-password"
	adminPassword, _ := u.User.Password()
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
	operation, err := NewManagedFileOperationID()
	require.NoError(t, err)
	name, err := ManagedFileName(operation, 0)
	require.NoError(t, err)
	reader := "agentsql_reader_" + name[len("agentsql_file_"):len("agentsql_file_")+12]
	writer := "agentsql_writer_" + name[len("agentsql_file_"):len("agentsql_file_")+12]
	rolesCreated := false
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if rolesCreated {
			_, _ = connection.Exec(cleanup, `REVOKE `+pgx.Identifier{writer}.Sanitize()+` FROM `+pgx.Identifier{reader}.Sanitize())
			_, _ = connection.Exec(cleanup, `ALTER DEFAULT PRIVILEGES FOR ROLE `+pgx.Identifier{writer}.Sanitize()+` IN SCHEMA agentsql_files REVOKE SELECT ON TABLES FROM `+pgx.Identifier{reader}.Sanitize())
			_, _ = connection.Exec(cleanup, `REVOKE USAGE ON SCHEMA agentsql_files FROM `+pgx.Identifier{reader}.Sanitize())
		}
		_, _ = connection.Exec(cleanup, `DROP SCHEMA agentsql_files CASCADE`)
		if rolesCreated {
			_, _ = connection.Exec(cleanup, `DROP ROLE `+pgx.Identifier{writer}.Sanitize())
			_, _ = connection.Exec(cleanup, `DROP ROLE `+pgx.Identifier{reader}.Sanitize())
		}
	}()
	_, err = connection.Exec(ctx, `CREATE ROLE `+pgx.Identifier{reader}.Sanitize()+` NOLOGIN`)
	require.NoError(t, err)
	_, err = connection.Exec(ctx, `CREATE ROLE `+pgx.Identifier{writer}.Sanitize()+` LOGIN PASSWORD 'writer-password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`)
	require.NoError(t, err)
	rolesCreated = true
	_, err = connection.Exec(ctx, `CREATE SCHEMA agentsql_files AUTHORIZATION `+pgx.Identifier{writer}.Sanitize())
	require.NoError(t, err)
	_, err = connection.Exec(ctx, `GRANT USAGE ON SCHEMA agentsql_files TO `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	datasource := model.Datasource{ID: "managed-file-test", DBType: "postgres", Host: u.Hostname(), Port: port,
		Database: u.Path[1:], Username: writer, StmtTimeoutMS: 10000}
	sheets := []ManagedFileSheet{{Columns: []ManagedFileColumn{{Name: "region", Type: "string"}, {Name: "amount", Type: "int"}},
		Rows: [][]any{{"east", int64(10)}, {"west", int64(20)}}},
		{Columns: []ManagedFileColumn{{Name: "sold_at", Type: "datetime"}}, Rows: [][]any{{time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}}}}
	privileged := datasource
	privileged.Username = u.User.Username()
	_, err = StageManagedFiles(ctx, privileged, adminPassword, reader, operation, sheets)
	require.ErrorIs(t, err, ErrManagedFileInvalid, "a superuser must not provision managed files")
	tables, err := StageManagedFiles(ctx, datasource, writerPassword, reader, operation, sheets)
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
	require.NoError(t, GrantManagedFileRead(ctx, datasource, writerPassword, reader, operation, len(sheets)))
	require.NoError(t, GrantManagedFileRead(ctx, datasource, writerPassword, reader, operation, len(sheets)), "grant retry is idempotent")
	_, err = connection.Exec(ctx, `SET ROLE `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	var importedRows int
	readErr := connection.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{ManagedFileSchema, tables[0].Name}.Sanitize()).Scan(&importedRows)
	_, resetErr := connection.Exec(ctx, `RESET ROLE`)
	require.NoError(t, resetErr)
	require.NoError(t, readErr)
	require.Equal(t, 2, importedRows)
	// PostgreSQL default privileges can silently grant a new table to a
	// reader. The staging transaction must detect and roll back that table.
	_, err = connection.Exec(ctx, `ALTER DEFAULT PRIVILEGES FOR ROLE `+pgx.Identifier{writer}.Sanitize()+` IN SCHEMA agentsql_files GRANT SELECT ON TABLES TO `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	otherOperation, err := NewManagedFileOperationID()
	require.NoError(t, err)
	_, err = StageManagedFiles(ctx, datasource, writerPassword, reader, otherOperation, sheets[:1])
	require.ErrorIs(t, err, ErrManagedFileInvalid)
	_, err = connection.Exec(ctx, `ALTER DEFAULT PRIVILEGES FOR ROLE `+pgx.Identifier{writer}.Sanitize()+` IN SCHEMA agentsql_files REVOKE SELECT ON TABLES FROM `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	_, err = connection.Exec(ctx, `GRANT `+pgx.Identifier{writer}.Sanitize()+` TO `+pgx.Identifier{reader}.Sanitize())
	require.NoError(t, err)
	otherOperation, err = NewManagedFileOperationID()
	require.NoError(t, err)
	_, err = StageManagedFiles(ctx, datasource, writerPassword, reader, otherOperation, sheets[:1])
	require.ErrorIs(t, err, ErrManagedFileInvalid, "inherited owner access must roll back the new table")
	var count int
	require.NoError(t, connection.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r'`, ManagedFileSchema).Scan(&count))
	require.Equal(t, 2, count, "rejected default grant must not leave a table")
	require.NoError(t, DropManagedFiles(ctx, datasource, writerPassword, operation, len(sheets)))
	require.NoError(t, DropManagedFiles(ctx, datasource, writerPassword, operation, len(sheets)), "cleanup retry is idempotent")
	require.NoError(t, connection.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r'`, ManagedFileSchema).Scan(&count))
	require.Zero(t, count)
}
