package controlledread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/discovery"
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
	ListSchema(context.Context, model.Datasource, []byte, []executor.TableRef) ([]executor.SchemaColumn, error)
	Sample(context.Context, model.Datasource, []byte, executor.TableRef, []executor.ColumnRef, int) (model.QueryResult, error)
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
	timed, cancel := context.WithTimeout(ctx, discoverDeadline)
	defer cancel()
	state := &runState{datasource: datasource, allowed: make(map[discovery.ColumnRef]struct{})}
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
	if !ok || state == nil || state.datasource.ID != datasourceID {
		return nil, discovery.NewPortError(discovery.CodeInternal)
	}
	return state, nil
}

func (service *Service) ListColumns(ctx context.Context, datasourceID string, tables []discovery.TableRef) ([]discovery.ColumnMeta, error) {
	state, err := stateFromContext(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	requested := make([]executor.TableRef, len(tables))
	for index, table := range tables {
		requested[index] = executor.TableRef{Schema: table.Schema, Table: table.Table}
	}
	columns, err := service.manager.ListSchema(ctx, state.datasource, append([]byte(nil), service.secret...), requested)
	if err != nil {
		return nil, safeDiscoveryError(err)
	}
	if len(columns) > discovery.MaxMetadataColumns {
		return nil, discovery.NewPortError(discovery.CodeScopeLimit)
	}
	metadata := make([]discovery.ColumnMeta, 0, len(columns))
	for _, column := range columns {
		meta := discovery.ColumnMeta{Schema: column.Schema, Table: column.Table, Column: column.Column, DataType: column.DataType, Ordinal: column.Ordinal}
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
	requested := make([]executor.ColumnRef, len(columns))
	for index, column := range columns {
		requested[index] = executor.ColumnRef{Schema: column.Schema, Table: column.Table, Column: column.Column}
	}
	result, err := service.manager.Sample(ctx, state.datasource, append([]byte(nil), service.secret...), executor.TableRef{Schema: table.Schema, Table: table.Table}, requested, limit)
	if err != nil {
		return discovery.SampleBatch{}, safeDiscoveryError(err)
	}
	if result.Truncated || len(result.Rows) > limit || len(result.Columns) != len(columns) {
		return discovery.SampleBatch{}, discovery.NewPortError(discovery.CodeInternal)
	}
	for index, name := range result.Columns {
		if !sameIdentifier(model.DBDialect(state.datasource.DBType), name, columns[index].Column) {
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
