package businessdb

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
)

// NativeExecutor exposes connection-level native operations. SQL lineage,
// masking, and SQL audit rules do not apply to these operations.
type NativeExecutor interface {
	Category() model.DatasourceCategory
	Dialect() model.DBDialect
	Ping(context.Context) error
	ServerVersion(context.Context) (string, error)
	Discover(context.Context) (NativeCatalog, error)
	NativeQuery(context.Context, NativeQueryRequest) (NativeQueryResult, error)
	Close() error
}

type NativeCatalog struct {
	Category      model.DatasourceCategory
	ServerVersion string
	Namespaces    []NativeNamespace
}
type NativeNamespace struct {
	Name      string
	Kind      string
	ItemCount int64
	Metadata  map[string]string
}
type NativeQueryRequest struct {
	Namespace string
	Command   string
	Args      []string
}
type NativeQueryResult struct {
	Columns []string
	Rows    [][]string
	Info    map[string]string
	Raw     string
}

const nativeMaxRows = 1000
const nativeMaxValueBytes = 4096
const nativeMaxArgs = 256

func boundedNativeString(value string) string {
	if len(value) <= nativeMaxValueBytes {
		return value
	}
	value = value[:nativeMaxValueBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func normalizedNativeCommand(request NativeQueryRequest) (string, []string, error) {
	command := strings.ToUpper(strings.TrimSpace(request.Command))
	if command == "" || len(request.Args) > nativeMaxArgs {
		return "", nil, fmt.Errorf("native command or argument count is invalid")
	}
	parts := strings.Fields(command)
	if len(parts) > 2 {
		return "", nil, fmt.Errorf("native command is not allowed")
	}
	for _, part := range parts {
		for _, r := range part {
			if r < 'A' || r > 'Z' && r != '_' {
				return "", nil, fmt.Errorf("native command is invalid")
			}
		}
	}
	for _, arg := range request.Args {
		if len(arg) > nativeMaxValueBytes {
			return "", nil, fmt.Errorf("native argument exceeds size limit")
		}
	}
	return strings.Join(parts, " "), request.Args, nil
}

func boundedNativeRows(columns []string, rows [][]string) NativeQueryResult {
	result := NativeQueryResult{Columns: columns, Rows: make([][]string, 0, min(len(rows), nativeMaxRows))}
	for i, row := range rows {
		if i >= nativeMaxRows {
			result.Info = map[string]string{"truncated": "true"}
			break
		}
		bounded := make([]string, len(row))
		for j, cell := range row {
			bounded[j] = boundedNativeString(cell)
		}
		result.Rows = append(result.Rows, bounded)
	}
	return result
}

// auditNativeQuery records command decisions without logging keys, arguments,
// values, or credentials. The host logger controls retention and forwarding.
func auditNativeQuery(dbType model.DBDialect, datasourceID, command string, readOnly bool, err error) {
	status := "allowed"
	if err != nil {
		status = "error"
	}
	slog.Info("native query", "datasource_id", datasourceID, "db_type", dbType, "command", command, "read_only", readOnly, "status", status)
}
