package businessdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/jackc/pgx/v5"
)

// OpenGaussExecutor contains the verified 7.0.0 LTS read-only path. PG wire
// compatibility does not grant PostgreSQL-only authorization capabilities.
type OpenGaussExecutor struct{ *PostgresExecutor }

var _ Executor = (*OpenGaussExecutor)(nil)

// Exact build from the checksum-pinned official openEuler 24.03 x86_64 image.
const verifiedOpenGaussRelease = "openGauss 7.0.0 build 2397a86f"

func NewOpenGaussExecutor(ctx context.Context, datasource model.Datasource, password string) (*OpenGaussExecutor, error) {
	if datasource.DBType != string(model.DialectOpenGauss) {
		return nil, fmt.Errorf("open openGauss datasource: incorrect database type")
	}
	if datasource.TLSMode != "verify-full" || datasource.TLSCAFile == "" || datasource.TrustServerCertificate {
		return nil, fmt.Errorf("open openGauss datasource: verified TLS is required")
	}
	postgres, err := newPostgresExecutor(ctx, datasource, password, true, verifyOpenGaussConnection)
	if err != nil {
		return nil, err
	}
	return &OpenGaussExecutor{PostgresExecutor: postgres}, nil
}

// AfterConnect runs on every fresh physical connection, including reconnects.
func verifyOpenGaussConnection(ctx context.Context, connection *pgx.Conn) error {
	var version, readOnlyState string
	if err := connection.QueryRow(ctx, "SELECT pg_catalog.version()").Scan(&version); err != nil || !openGaussIdentityAllowed(version) {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageConnect, "", nil)
	}
	if err := connection.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&readOnlyState); err != nil || readOnlyState != "on" {
		return newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageConnect, "", ErrReadOnlyViolated)
	}
	return nil
}

func openGaussIdentityAllowed(version string) bool {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "(")
	return strings.HasPrefix(version, verifiedOpenGaussRelease+") compiled at ")
}

func (*OpenGaussExecutor) Dialect() string { return string(model.DialectOpenGauss) }

func (*OpenGaussExecutor) OpenSession(context.Context, string) (Session, error) {
	return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageAcquire, "", ErrReadOnlyViolated)
}

func (*OpenGaussExecutor) BeginWriteTx(context.Context) (WriteTx, error) {
	return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageBeginTx, "", ErrReadOnlyViolated)
}

func (*OpenGaussExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{}, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageExecute, "", ErrReadOnlyViolated)
}

func (executor *OpenGaussExecutor) Query(ctx context.Context, sql string, rowLimit int) (model.QueryResult, error) {
	ast, err := parseOpenGaussSQL(sql)
	if err != nil {
		return model.QueryResult{}, err
	}
	if executor == nil || executor.PostgresExecutor == nil {
		return model.QueryResult{}, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageQuery, "", nil)
	}
	if err := executor.verifyPhysicalTable(ctx, ast.Tables[0]); err != nil {
		return model.QueryResult{}, err
	}
	return executor.PostgresExecutor.Query(ctx, sql, rowLimit)
}

func (executor *OpenGaussExecutor) Explain(ctx context.Context, sql string) (model.ExplainInfo, error) {
	ast, err := parseOpenGaussSQL(sql)
	if err != nil {
		return model.ExplainInfo{}, err
	}
	if executor == nil || executor.PostgresExecutor == nil {
		return model.ExplainInfo{}, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageExplain, "", nil)
	}
	if err := executor.verifyPhysicalTable(ctx, ast.Tables[0]); err != nil {
		return model.ExplainInfo{}, err
	}
	return executor.PostgresExecutor.Explain(ctx, sql)
}

func parseOpenGaussSQL(sql string) (*model.AST, error) {
	approved, err := parser.NewParser(model.DialectOpenGauss)
	if err != nil {
		return nil, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageParse, "", nil)
	}
	ast, err := approved.Parse(sql)
	if err != nil || ast == nil || len(ast.Tables) != 1 {
		return nil, newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, DBStageParse, "", nil)
	}
	return ast, nil
}

func (executor *OpenGaussExecutor) verifyPhysicalTable(ctx context.Context, table model.ObjectRef) error {
	if ctx == nil || executor == nil || executor.PostgresExecutor == nil || executor.pool == nil {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageMetadata, "", nil)
	}
	metadataContext, cancel := executor.timeoutContext(ctx)
	defer cancel()
	var relationKind string
	err := executor.pool.QueryRow(metadataContext, `
SELECT c.relkind::text FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, table.Schema, table.Table).Scan(&relationKind)
	if err != nil || relationKind != "r" {
		return newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, "", nil)
	}
	return nil
}
