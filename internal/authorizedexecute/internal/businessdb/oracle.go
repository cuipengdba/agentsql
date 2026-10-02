package businessdb

import (
	"context"
	"strconv"

	"github.com/cuipengdba/agentsql/internal/model"
	goora "github.com/sijms/go-ora/v2"
)

// OracleExecutor is the Oracle connection and typed-metadata dialect. The
// pure-Go driver is used for the Go 1.25 minimum discovery slice; the driver
// qualification boundary is recorded in docs/dm-oracle-dialect.md.
type OracleExecutor struct{ *limitedSQLExecutor }

// NewOracleExecutor opens and verifies an Oracle service datasource.
func NewOracleExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*OracleExecutor, error) {
	dsn := buildOracleDSN(datasource, password)
	executor, err := newLimitedSQLExecutor(ctx, datasource, "oracle", dsn, "oracle", password, readOnly)
	if err != nil {
		return nil, err
	}
	if err := executor.probeCurrentSchema(ctx, "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL"); err != nil {
		_ = executor.Close()
		return nil, err
	}
	return &OracleExecutor{limitedSQLExecutor: executor}, nil
}

func buildOracleDSN(datasource model.Datasource, password string) string {
	options := map[string]string{}
	if datasource.StmtTimeoutMS > 0 {
		seconds := (datasource.StmtTimeoutMS + 999) / 1_000
		options["CONNECTION TIMEOUT"] = strconv.Itoa(seconds)
		options["TIMEOUT"] = strconv.Itoa(seconds)
	}
	return goora.BuildUrl(
		datasource.Host,
		datasource.Port,
		datasource.Database,
		datasource.Username,
		password,
		options,
	)
}

var _ Executor = (*OracleExecutor)(nil)
