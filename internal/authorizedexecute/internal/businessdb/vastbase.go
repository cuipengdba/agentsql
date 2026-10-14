package businessdb

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

// VastbaseExecutor isolates the candidate G100 PG compatibility path from
// PostgreSQL-only capabilities. Its underlying pool is always read-only;
// target-server compatibility still requires real G100 acceptance.
type VastbaseExecutor struct{ *PostgresExecutor }

var _ Executor = (*VastbaseExecutor)(nil)

var verifiedG100Version = regexp.MustCompile(`(?i)\bvastbase g100 v3\.0\b.*\bbuild 9\b`)

func NewVastbaseExecutor(ctx context.Context, datasource model.Datasource, password string) (*VastbaseExecutor, error) {
	if datasource.DBType != string(model.DialectVastbase) {
		return nil, fmt.Errorf("open Vastbase datasource: incorrect database type")
	}
	postgres, err := NewPostgresExecutor(ctx, datasource, password, true)
	if err != nil {
		return nil, err
	}
	probeContext, cancel := postgres.timeoutContext(ctx)
	defer cancel()
	var version, compatibility, readOnlyState string
	// G100 reports a PostgreSQL compatibility string from version(). Probe the
	// catalog-qualified vendor function so an ordinary search_path object
	// cannot claim to identify the server.
	if err := postgres.pool.QueryRow(probeContext, "SELECT pg_catalog.vb_version()").Scan(&version); err != nil ||
		!verifiedG100Version.MatchString(version) {
		_ = postgres.Close()
		return nil, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageConnect, "", nil)
	}
	if err := postgres.pool.QueryRow(probeContext, "SHOW sql_compatibility").Scan(&compatibility); err != nil ||
		!strings.EqualFold(compatibility, "PG") {
		_ = postgres.Close()
		return nil, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageConnect, "", nil)
	}
	if err := postgres.pool.QueryRow(probeContext, "SHOW default_transaction_read_only").Scan(&readOnlyState); err != nil ||
		readOnlyState != "on" {
		_ = postgres.Close()
		return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageConnect, "", ErrReadOnlyViolated)
	}
	return &VastbaseExecutor{PostgresExecutor: postgres}, nil
}

func (*VastbaseExecutor) Dialect() string { return string(model.DialectVastbase) }

func (*VastbaseExecutor) OpenSession(context.Context, string) (Session, error) {
	return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageAcquire, "", ErrReadOnlyViolated)
}

func (*VastbaseExecutor) BeginWriteTx(context.Context) (WriteTx, error) {
	return nil, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageBeginTx, "", ErrReadOnlyViolated)
}

func (*VastbaseExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{}, newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageExecute, "", ErrReadOnlyViolated)
}

func (executor *VastbaseExecutor) Query(ctx context.Context, sql string, rowLimit int) (model.QueryResult, error) {
	if err := validateVastbaseSQL(sql); err != nil {
		return model.QueryResult{}, err
	}
	if executor == nil || executor.PostgresExecutor == nil {
		return model.QueryResult{}, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageQuery, "", nil)
	}
	return executor.PostgresExecutor.Query(ctx, sql, rowLimit)
}

func (executor *VastbaseExecutor) Explain(ctx context.Context, sql string) (model.ExplainInfo, error) {
	if err := validateVastbaseSQL(sql); err != nil {
		return model.ExplainInfo{}, err
	}
	if executor == nil || executor.PostgresExecutor == nil {
		return model.ExplainInfo{}, newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageExplain, "", nil)
	}
	return executor.PostgresExecutor.Explain(ctx, sql)
}

func validateVastbaseSQL(sql string) error {
	approved, err := parser.NewParser(model.DialectVastbase)
	if err != nil {
		return newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageParse, "", nil)
	}
	if _, err := approved.Parse(sql); err != nil {
		return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, DBStageParse, "", nil)
	}
	return nil
}
