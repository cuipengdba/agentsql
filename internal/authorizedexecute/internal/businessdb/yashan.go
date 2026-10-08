package businessdb

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

// YashanExecutor permits the independently checked Yashan SELECT profile.
// Transactions, writes, and EXPLAIN remain unavailable.
type YashanExecutor struct{ *limitedSQLExecutor }

// yashanSession preserves physical-session ownership and the SELECT guard.
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
		return nil, fmt.Errorf("YashanDB Go driver is unavailable: rebuild with CGO_ENABLED=1 and -tags yashan")
	}
	if !yashanClientRuntimeReady() {
		return nil, fmt.Errorf("YashanDB C client runtime is unavailable: install the vendor client and set LD_LIBRARY_PATH to its lib directory before starting AgentSQL")
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

// OpenSession acquires a physical connection and retains the SELECT guard.
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

// Query submits only the offline-qualified Yashan SELECT subset.
func (executor *YashanExecutor) Query(ctx context.Context, sqlText string, rowLimit int) (model.QueryResult, error) {
	if err := validateYashanSelect(sqlText); err != nil {
		return model.QueryResult{}, err
	}
	if executor == nil || executor.limitedSQLExecutor == nil {
		return model.QueryResult{}, yashanUnsupported(DBStageQuery)
	}
	return executor.limitedSQLExecutor.Query(ctx, sqlText, rowLimit)
}

// Query applies the same guard to physical sessions.
func (session *yashanSession) Query(ctx context.Context, sqlText string, rowLimit int) (model.QueryResult, error) {
	if err := validateYashanSelect(sqlText); err != nil {
		return model.QueryResult{}, err
	}
	if session == nil || session.Session == nil {
		return model.QueryResult{}, yashanUnsupported(DBStageQuery)
	}
	return session.Session.Query(ctx, sqlText, rowLimit)
}

func validateYashanSelect(sqlText string) error {
	if _, err := parser.NewYashanParser().Parse(sqlText); err != nil {
		return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, DBStageParse, "", nil)
	}
	if err := validateLimitedSelectForDialect("yashan", sqlText); err != nil {
		return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, DBStageParse, "", nil)
	}
	return nil
}

func yashanClientRuntimeReady() bool {
	for _, directory := range filepath.SplitList(os.Getenv("LD_LIBRARY_PATH")) {
		if directory == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(directory, "libyascli.so")); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
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
