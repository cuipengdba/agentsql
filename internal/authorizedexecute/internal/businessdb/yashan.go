package businessdb

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/model"
)

// YashanExecutor is the native YashanDB connection and typed-metadata
// dialect. General SQL, transactions and EXPLAIN remain unavailable until
// their authorization and decoding contracts are implemented and verified.
type YashanExecutor struct{ *limitedSQLExecutor }

// yashanSession preserves physical-session ownership while keeping caller SQL
// outside the native driver until a YashanDB authorization contract exists.
type yashanSession struct{ Session }

// NewYashanExecutor opens and verifies a native YashanDB datasource. The
// official driver is registered only by builds using "-tags yashan" with CGO
// enabled; ordinary builds fail closed without acquiring a connection.
func NewYashanExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*YashanExecutor, error) {
	dsn, err := buildYashanDSN(datasource, password)
	if err != nil {
		return nil, err
	}
	if !sqlDriverRegistered("yasdb") {
		return nil, newDBError(
			DBErrorKindConnection,
			DBErrorCodeConnection,
			DBStageConnect,
			"",
			ErrDatasourceUnreachable,
		)
	}
	executor, err := newLimitedSQLExecutor(ctx, datasource, "yasdb", dsn, "yashan", password, readOnly)
	if err != nil {
		return nil, err
	}
	if err := executor.probeCurrentSchema(ctx, "SELECT USER FROM DUAL"); err != nil {
		_ = executor.Close()
		return nil, err
	}
	return &YashanExecutor{limitedSQLExecutor: executor}, nil
}

// OpenSession acquires the physical connection needed by the session lifecycle,
// but wraps it so the common limited-dialect SELECT path is not promoted into
// the deliberately metadata-only YashanDB capability.
func (executor *YashanExecutor) OpenSession(ctx context.Context, sessionID string) (Session, error) {
	if executor == nil || executor.limitedSQLExecutor == nil {
		return nil, newDBError(
			DBErrorKindConnection,
			DBErrorCodeConnection,
			DBStageAcquire,
			"",
			ErrDatasourceUnreachable,
		)
	}
	session, err := executor.limitedSQLExecutor.OpenSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &yashanSession{Session: session}, nil
}

// Query fails closed before validation or driver submission. YashanDB metadata
// discovery uses its typed capability and does not depend on general Query.
func (*YashanExecutor) Query(context.Context, string, int) (model.QueryResult, error) {
	return model.QueryResult{}, yashanUnsupported(DBStageQuery)
}

// Query fails closed on physical sessions as well as on the pooled executor.
func (*yashanSession) Query(context.Context, string, int) (model.QueryResult, error) {
	return model.QueryResult{}, yashanUnsupported(DBStageQuery)
}

func yashanUnsupported(stage DBStage) error {
	return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, "", nil)
}

func buildYashanDSN(datasource model.Datasource, password string) (string, error) {
	if err := validateDatasource(datasource); err != nil {
		return "", fmt.Errorf("build YashanDB connection configuration: %w", err)
	}
	if password == "" {
		return "", fmt.Errorf("build YashanDB connection configuration: password is required")
	}
	username, err := escapeYashanDSNComponent(datasource.Username)
	if err != nil {
		return "", err
	}
	escapedPassword, err := escapeYashanDSNComponent(password)
	if err != nil {
		return "", err
	}
	return username + "/" + escapedPassword + "@" +
		net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port)) +
		"?compat_vector=yashan", nil
}

func escapeYashanDSNComponent(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("build YashanDB connection configuration: empty credential component")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("build YashanDB connection configuration: unsupported credential character")
		}
	}
	replacer := strings.NewReplacer(`\`, `\\`, `/`, `\/`, `@`, `\@`)
	return replacer.Replace(value), nil
}

func sqlDriverRegistered(name string) bool {
	for _, registered := range sql.Drivers() {
		if registered == name {
			return true
		}
	}
	return false
}

var _ Executor = (*YashanExecutor)(nil)
var _ Session = (*yashanSession)(nil)
