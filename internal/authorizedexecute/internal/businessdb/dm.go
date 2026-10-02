package businessdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/model"
	dm8 "github.com/godoes/gorm-dameng/dm8"
)

// DMExecutor supports connection lifecycle, typed metadata and narrowly
// validated read-only SELECTs. Transactions, writes and EXPLAIN remain
// unavailable until their authorization and plan-decoding contracts are
// implemented and verified.
type DMExecutor struct{ *limitedSQLExecutor }

// NewDMExecutor opens and verifies a native DM8 datasource.
func NewDMExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*DMExecutor, error) {
	dsn, err := buildDMDSN(datasource, password)
	if err != nil {
		return nil, err
	}
	executor, err := newLimitedSQLExecutorWithDialect(
		ctx, datasource, "dm", dsn, "dm", password, readOnly, classifyDMError, nil,
	)
	if err != nil {
		return nil, err
	}
	if err := executor.probeCurrentSchema(ctx, "SELECT USER"); err != nil {
		_ = executor.Close()
		return nil, err
	}
	return &DMExecutor{limitedSQLExecutor: executor}, nil
}

func classifyDMError(ctx context.Context, stage DBStage, cause error) (error, bool) {
	if classified, ok := classifyContextError(ctx, stage, cause); ok {
		return classified, true
	}
	var driverError *dm8.DmError
	if errors.As(cause, &driverError) {
		driverCode := strconv.FormatInt(int64(driverError.ErrCode), 10)
		switch code := driverError.ErrCode; {
		case code == -2501:
			return newDBError(DBErrorKindAuthentication, DBErrorCodeAuthentication, stage, driverCode, nil), true
		case code == -2007:
			return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, stage, driverCode, nil), true
		case code <= -5501 && code > -6000:
			return newDBError(DBErrorKindPermission, DBErrorCodePermission, stage, driverCode, ErrPermissionDenied), true
		case code == 6001 || code == 6060 || code == 9007 || code == 9008 || code == 20001:
			return newDBError(DBErrorKindConnection, DBErrorCodeConnection, stage, driverCode, ErrDatasourceUnreachable), true
		default:
			return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, driverCode, nil), true
		}
	}
	if isNetworkConnectionError(cause) {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, stage, "", ErrDatasourceUnreachable), true
	}
	return nil, false
}

func buildDMDSN(datasource model.Datasource, password string) (string, error) {
	// dm8 v8.1.4.80 splits this DSN itself and does not URL-decode userinfo.
	// An '@' in the password is safe because the driver uses LastIndex("@"),
	// while '?' and control characters cannot be represented unambiguously.
	if strings.Contains(datasource.Username, ":") ||
		strings.ContainsAny(datasource.Username, "?") ||
		strings.ContainsAny(password, "?") ||
		strings.ContainsAny(datasource.Database, "?&=") ||
		containsControl(datasource.Username) || containsControl(password) || containsControl(datasource.Database) {
		return "", fmt.Errorf("build DM connection configuration: unsupported credential or schema character")
	}
	query := "schema=" + datasource.Database
	if datasource.StmtTimeoutMS > 0 {
		query += "&connectTimeout=" + strconv.Itoa(datasource.StmtTimeoutMS)
	}
	return "dm://" + datasource.Username + ":" + password + "@" +
		net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port)) + "?" + query, nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

var _ Executor = (*DMExecutor)(nil)
