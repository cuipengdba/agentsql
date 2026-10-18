package businessdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/jackc/pgx/v5"
)

// HighGoExecutor isolates the verified HighGo PG-mode path from
// PostgreSQL-only capabilities. Its underlying pool is always read-only.
type HighGoExecutor struct{ *PostgresExecutor }

var _ Executor = (*HighGoExecutor)(nil)

const verifiedHighGoVersion = "瀚高数据库管理系统V9.0-企业版-9.0.10"

func NewHighGoExecutor(ctx context.Context, datasource model.Datasource, password string) (*HighGoExecutor, error) {
	if datasource.DBType != string(model.DialectHighGo) {
		return nil, fmt.Errorf("open HighGo datasource: incorrect database type")
	}
	postgres, err := newPostgresExecutor(ctx, datasource, password, true, verifyHighGoConnection)
	if err != nil {
		return nil, err
	}
	return &HighGoExecutor{PostgresExecutor: postgres}, nil
}

// Every newly opened pool connection must prove the exact server and mode.
// A recovered pool cannot silently reconnect to an unverified PostgreSQL host.
func verifyHighGoConnection(ctx context.Context, connection *pgx.Conn) error {
	var version, compatibility, readOnlyState string
	// HighGo reports a PostgreSQL compatibility string from version(). Probe the
	// catalog-qualified vendor function so an ordinary search_path object
	// cannot claim to identify the server.
	if err := connection.QueryRow(ctx, "SELECT pg_catalog.highgo_version()").Scan(&version); err != nil {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageConnect, "", nil)
	}
	if err := connection.QueryRow(ctx, "SHOW ivorysql.database_mode").Scan(&compatibility); err != nil ||
		!highGoIdentityAllowed(version, compatibility) {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageConnect, "", nil)
	}
	if err := connection.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&readOnlyState); err != nil ||
		readOnlyState != "on" {
		return newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageConnect, "", ErrReadOnlyViolated)
	}
	return nil
}

func highGoIdentityAllowed(version, mode string) bool {
	return strings.TrimSpace(version) == verifiedHighGoVersion && strings.EqualFold(strings.TrimSpace(mode), "pg")
}

func (*HighGoExecutor) Dialect() string { return string(model.DialectHighGo) }

func (*HighGoExecutor) OpenSession(context.Context, string) (Session, error) {
	return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageAcquire, "", ErrReadOnlyViolated)
}

func (*HighGoExecutor) BeginWriteTx(context.Context) (WriteTx, error) {
	return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageBeginTx, "", ErrReadOnlyViolated)
}

func (*HighGoExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{}, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageExecute, "", ErrReadOnlyViolated)
}

func (executor *HighGoExecutor) Query(ctx context.Context, sql string, rowLimit int) (model.QueryResult, error) {
	if err := validateHighGoSQL(sql); err != nil {
		return model.QueryResult{}, err
	}
	if executor == nil || executor.PostgresExecutor == nil {
		return model.QueryResult{}, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageQuery, "", nil)
	}
	return executor.PostgresExecutor.Query(ctx, sql, rowLimit)
}

func (executor *HighGoExecutor) Explain(ctx context.Context, sql string) (model.ExplainInfo, error) {
	if err := validateHighGoSQL(sql); err != nil {
		return model.ExplainInfo{}, err
	}
	if executor == nil || executor.PostgresExecutor == nil {
		return model.ExplainInfo{}, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageExplain, "", nil)
	}
	return executor.PostgresExecutor.Explain(ctx, sql)
}

func validateHighGoSQL(sql string) error {
	approved, err := parser.NewParser(model.DialectHighGo)
	if err != nil {
		return newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageParse, "", nil)
	}
	if _, err := approved.Parse(sql); err != nil {
		return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, DBStageParse, "", nil)
	}
	return nil
}
