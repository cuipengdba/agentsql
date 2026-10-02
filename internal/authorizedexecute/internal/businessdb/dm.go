package businessdb

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/model"
	_ "github.com/godoes/gorm-dameng/dm8"
)

// DMExecutor is the DM8 connection and typed-metadata dialect. General SQL,
// transactions and EXPLAIN remain unavailable until their authorization and
// plan-decoding contracts are implemented and verified.
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
	executor, err := newLimitedSQLExecutor(ctx, datasource, "dm", dsn, "dm", password, readOnly)
	if err != nil {
		return nil, err
	}
	if err := executor.probeCurrentSchema(ctx, "SELECT USER"); err != nil {
		_ = executor.Close()
		return nil, err
	}
	return &DMExecutor{limitedSQLExecutor: executor}, nil
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
