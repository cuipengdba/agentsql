package controlledread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/rules"
)

const (
	discoverDeadline    = 12 * time.Second
	statementTimeout    = 2 * time.Second
	discoveryQPS        = 1.0
	discoveryConcurrent = 1
	capacityOnlyQPS     = 1_000_000_000.0
)

type datasourceReader interface {
	Get(ctx context.Context, id string) (model.Datasource, error)
}

type executorPool interface {
	GetOrOpen(model.Datasource, []byte) (executor.Executor, error)
	CloseAll() error
}

// Service is the server-internal, fail-closed discovery reader. It implements
// discovery's typed ports and never accepts caller-provided SQL.
type Service struct {
	datasources datasourceReader
	manager     executorPool
	secret      []byte
	limiter     rules.RateLimiter
}

type runState struct {
	datasource model.Datasource
	executor   executor.Executor
	allowed    map[discovery.ColumnRef]struct{}
}

type runStateKey struct{}

func NewService(datasources datasourceReader, manager executorPool, secret []byte, limiter rules.RateLimiter) (*Service, error) {
	if datasources == nil || manager == nil || len(secret) != 32 || limiter == nil {
		return nil, fmt.Errorf("create controlled reader: invalid dependency")
	}
	return &Service{datasources: datasources, manager: manager, secret: append([]byte(nil), secret...), limiter: limiter}, nil
}

// Close releases the read-only pools owned by the service.
func (service *Service) Close() error {
	if service == nil || service.manager == nil {
		return nil
	}
	return service.manager.CloseAll()
}

// Discover applies request-level admission and a fixed overall deadline, then
// delegates pure classification and sampling orchestration to discovery.Scanner.
func (service *Service) Discover(ctx context.Context, username, datasourceID string, request discovery.ScanRequest) (discovery.ScanResult, error) {
	if service == nil || ctx == nil || strings.TrimSpace(username) == "" || username != strings.TrimSpace(username) {
		return discovery.ScanResult{}, discovery.NewPortError(discovery.CodeInvalidRequest)
	}
	keys := []struct {
		key        string
		qps        float64
		concurrent int
	}{
		{key: "admin:" + username + ":" + datasourceID, qps: discoveryQPS, concurrent: discoveryConcurrent},
		{key: "admin:" + username, qps: discoveryQPS, concurrent: discovery.MaxTables},
		{key: "datasource:" + datasourceID, qps: capacityOnlyQPS, concurrent: 1},
		{key: "discovery:global", qps: capacityOnlyQPS, concurrent: 2},
	}
	reserved := make([]string, 0, len(keys))
	for _, candidate := range keys {
		admission, err := service.limiter.Allow(candidate.key, candidate.qps, candidate.concurrent)
		if err != nil {
			releaseReservations(service.limiter, reserved)
			return discovery.ScanResult{}, discovery.NewPortError(discovery.CodeInternal)
		}
		if !admission.Allowed {
			releaseReservations(service.limiter, reserved)
			return discovery.ScanResult{}, discovery.NewPortError(discovery.CodeRateLimited)
		}
		reserved = append(reserved, candidate.key)
	}
	defer releaseReservations(service.limiter, reserved)

	datasource, err := service.datasources.Get(ctx, datasourceID)
	if err != nil {
		return discovery.ScanResult{}, err
	}
	datasource.StmtTimeoutMS = effectiveStatementTimeout(datasource.StmtTimeoutMS)
	reader, err := service.manager.GetOrOpen(datasource, append([]byte(nil), service.secret...))
	if err != nil {
		return discovery.ScanResult{}, safeDiscoveryError(err)
	}
	timed, cancel := context.WithTimeout(ctx, discoverDeadline)
	defer cancel()
	state := &runState{datasource: datasource, executor: reader, allowed: make(map[discovery.ColumnRef]struct{})}
	timed = context.WithValue(timed, runStateKey{}, state)
	result, err := discovery.NewScanner(service, service).Scan(timed, datasourceID, request)
	if err != nil && errors.Is(timed.Err(), context.DeadlineExceeded) {
		return discovery.ScanResult{}, discovery.NewPortError(discovery.CodeTimeout)
	}
	return result, err
}

func effectiveStatementTimeout(configured int) int {
	maximum := int(statementTimeout / time.Millisecond)
	if configured > 0 && configured < maximum {
		return configured
	}
	return maximum
}

func releaseReservations(limiter rules.RateLimiter, keys []string) {
	for index := len(keys) - 1; index >= 0; index-- {
		_ = limiter.Release(keys[index])
	}
}

func metadataColumnsMatch(columns []string) bool {
	expected := []string{"table_schema", "table_name", "column_name", "data_type"}
	if len(columns) != 6 {
		return false
	}
	for index, name := range expected {
		if !strings.EqualFold(columns[index], name) {
			return false
		}
	}
	return (strings.EqualFold(columns[4], "udt_name") || strings.EqualFold(columns[4], "column_type")) &&
		strings.EqualFold(columns[5], "ordinal_position")
}

func stateFromContext(ctx context.Context, datasourceID string) (*runState, error) {
	if ctx == nil {
		return nil, discovery.NewPortError(discovery.CodeInternal)
	}
	state, ok := ctx.Value(runStateKey{}).(*runState)
	if !ok || state == nil || state.datasource.ID != datasourceID || state.executor == nil {
		return nil, discovery.NewPortError(discovery.CodeInternal)
	}
	return state, nil
}

func (service *Service) ListColumns(ctx context.Context, datasourceID string, tables []discovery.TableRef) ([]discovery.ColumnMeta, error) {
	state, err := stateFromContext(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	dialect := model.DBDialect(state.datasource.DBType)
	sqlText, err := buildMetadataSQL(dialect, state.datasource.Database, tables)
	if err != nil {
		return nil, discovery.NewPortError(discovery.CodeInvalidRequest)
	}
	if err := assertMetadataSQL(dialect, sqlText); err != nil {
		return nil, discovery.NewPortError(discovery.CodeInternal)
	}
	result, err := state.executor.Query(ctx, sqlText, metadataRowLimit)
	if err != nil {
		return nil, safeDiscoveryError(err)
	}
	if result.Truncated || len(result.Rows) > discovery.MaxMetadataColumns || !metadataColumnsMatch(result.Columns) {
		return nil, discovery.NewPortError(discovery.CodeScopeLimit)
	}
	metadata := make([]discovery.ColumnMeta, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) != 6 {
			return nil, discovery.NewPortError(discovery.CodeInternal)
		}
		ordinal, parseErr := parsePositiveInt(row[5])
		if parseErr != nil {
			return nil, discovery.NewPortError(discovery.CodeInternal)
		}
		dataType := row[3]
		if row[4] != "" && row[4] != row[3] {
			dataType += "/" + row[4]
		}
		meta := discovery.ColumnMeta{Schema: row[0], Table: row[1], Column: row[2], DataType: dataType, Ordinal: ordinal}
		metadata = append(metadata, meta)
		state.allowed[discovery.ColumnRef{Schema: meta.Schema, Table: meta.Table, Column: meta.Column}] = struct{}{}
	}
	return metadata, nil
}

func (service *Service) QueryColumns(ctx context.Context, datasourceID string, table discovery.TableRef, columns []discovery.ColumnRef, limit int) (discovery.SampleBatch, error) {
	state, err := stateFromContext(ctx, datasourceID)
	if err != nil {
		return discovery.SampleBatch{}, err
	}
	for _, column := range columns {
		if _, confirmed := state.allowed[column]; !confirmed {
			return discovery.SampleBatch{}, discovery.NewPortError(discovery.CodeInternal)
		}
	}
	dialect := model.DBDialect(state.datasource.DBType)
	sqlText, err := buildSampleSQL(dialect, table, columns, limit)
	if err != nil {
		return discovery.SampleBatch{}, discovery.NewPortError(discovery.CodeInvalidRequest)
	}
	if err := assertSampleSQL(dialect, sqlText, table, columns); err != nil {
		return discovery.SampleBatch{}, discovery.NewPortError(discovery.CodeInternal)
	}
	result, err := state.executor.Query(ctx, sqlText, limit)
	if err != nil {
		return discovery.SampleBatch{}, safeDiscoveryError(err)
	}
	if result.Truncated || len(result.Rows) > limit || len(result.Columns) != len(columns) {
		return discovery.SampleBatch{}, discovery.NewPortError(discovery.CodeInternal)
	}
	for index, name := range result.Columns {
		if !sameIdentifier(dialect, name, columns[index].Column) {
			return discovery.SampleBatch{}, discovery.NewPortError(discovery.CodeInternal)
		}
	}
	rows := make([][]*string, len(result.Rows))
	for rowIndex, row := range result.Rows {
		if len(row) != len(columns) {
			return discovery.SampleBatch{}, discovery.NewPortError(discovery.CodeInternal)
		}
		rows[rowIndex] = make([]*string, len(row))
		for columnIndex, value := range row {
			if value == "" {
				continue
			}
			copyValue := value
			rows[rowIndex][columnIndex] = &copyValue
		}
	}
	return discovery.NewSampleBatch(columns, rows), nil
}

func parsePositiveInt(value string) (int, error) {
	var result int
	if _, err := fmt.Sscanf(value, "%d", &result); err != nil || result < 1 || fmt.Sprintf("%d", result) != value {
		return 0, fmt.Errorf("invalid positive integer")
	}
	return result, nil
}

func assertMetadataSQL(dialect model.DBDialect, sqlText string) error {
	approved, err := parser.NewParser(dialect)
	if err != nil {
		return err
	}
	ast, err := approved.Parse(sqlText)
	if err != nil || ast == nil || ast.IsMulti || !strings.EqualFold(string(ast.StmtType), "SELECT") || len(ast.Tables) != 2 {
		return fmt.Errorf("metadata SQL invariant failed")
	}
	return nil
}

func assertSampleSQL(dialect model.DBDialect, sqlText string, table discovery.TableRef, columns []discovery.ColumnRef) error {
	approved, err := parser.NewParser(dialect)
	if err != nil {
		return err
	}
	ast, err := approved.Parse(sqlText)
	if err != nil || ast == nil || ast.IsMulti || !strings.EqualFold(string(ast.StmtType), "SELECT") || !ast.HasLimit || ast.HasWhere || ast.HasGroupBy || len(ast.Functions) != 0 || len(ast.Tables) != 1 || len(ast.DirectProjections) != len(columns) {
		return fmt.Errorf("sample SQL invariant failed")
	}
	if !sameIdentifier(dialect, ast.Tables[0].Schema, table.Schema) || !sameIdentifier(dialect, ast.Tables[0].Table, table.Table) {
		return fmt.Errorf("sample table invariant failed")
	}
	for index, projection := range ast.DirectProjections {
		if projection.Offset != index || projection.FromEnd || !sameIdentifier(dialect, projection.Column, columns[index].Column) {
			return fmt.Errorf("sample projection invariant failed")
		}
	}
	return nil
}

func sameIdentifier(dialect model.DBDialect, left, right string) bool {
	if dialect == "mysql" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

var _ discovery.SchemaLister = (*Service)(nil)
var _ discovery.LimitedQuerier = (*Service)(nil)
