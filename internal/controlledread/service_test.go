package controlledread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
)

type fakeDatasourceReader struct{ datasource model.Datasource }

func (reader fakeDatasourceReader) Get(context.Context, string) (model.Datasource, error) {
	return reader.datasource, nil
}

type fakeExecutorPool struct {
	executor interface {
		Query(context.Context, string, int) (model.QueryResult, error)
	}
	opened model.Datasource
}

type limiterCall struct {
	key        string
	qps        float64
	concurrent int
}

type recordingLimiter struct {
	calls    []limiterCall
	releases []string
}

func (limiter *recordingLimiter) Allow(key string, qps float64, concurrent int) (rules.RateLimitResult, error) {
	limiter.calls = append(limiter.calls, limiterCall{key: key, qps: qps, concurrent: concurrent})
	return rules.RateLimitResult{Allowed: true}, nil
}

func (limiter *recordingLimiter) Release(key string) error {
	limiter.releases = append(limiter.releases, key)
	return nil
}

func (pool *fakeExecutorPool) ListSchema(ctx context.Context, datasource model.Datasource, _ []byte, tables []executor.TableRef) ([]executor.SchemaColumn, error) {
	pool.opened = datasource
	requested := make([]discovery.TableRef, len(tables))
	for index, table := range tables {
		requested[index] = discovery.TableRef{Schema: table.Schema, Table: table.Table}
	}
	query, err := buildMetadataSQL(model.DBDialect(datasource.DBType), datasource.Database, requested)
	if err != nil {
		return nil, err
	}
	result, err := pool.executor.Query(ctx, query, metadataRowLimit)
	if err != nil {
		return nil, err
	}
	if !metadataColumnsMatch(result.Columns) {
		return nil, errors.New("invalid metadata shape")
	}
	columns := make([]executor.SchemaColumn, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) != 6 {
			return nil, errors.New("invalid metadata row")
		}
		ordinal, err := parsePositiveInt(row[5])
		if err != nil {
			return nil, err
		}
		dataType := row[3]
		if row[4] != "" && row[4] != row[3] {
			dataType += "/" + row[4]
		}
		columns = append(columns, executor.SchemaColumn{Schema: row[0], Table: row[1], Column: row[2], DataType: dataType, Ordinal: ordinal})
	}
	return columns, nil
}

func (pool *fakeExecutorPool) Sample(ctx context.Context, datasource model.Datasource, _ []byte, table executor.TableRef, columns []executor.ColumnRef, limit int) (model.QueryResult, error) {
	pool.opened = datasource
	requested := make([]discovery.ColumnRef, len(columns))
	for index, column := range columns {
		requested[index] = discovery.ColumnRef{Schema: column.Schema, Table: column.Table, Column: column.Column}
	}
	query, err := buildSampleSQL(model.DBDialect(datasource.DBType), discovery.TableRef{Schema: table.Schema, Table: table.Table}, requested, limit)
	if err != nil {
		return model.QueryResult{}, err
	}
	return pool.executor.Query(ctx, query, limit)
}
func (*fakeExecutorPool) CloseAll() error { return nil }

type capturedQuery struct {
	text     string
	rowLimit int
}

type fakeReadExecutor struct {
	mu             sync.Mutex
	queries        []capturedQuery
	metadataResult model.QueryResult
	sampleResult   model.QueryResult
	err            error
}

func (reader *fakeReadExecutor) Query(_ context.Context, sqlText string, rowLimit int) (model.QueryResult, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.queries = append(reader.queries, capturedQuery{text: sqlText, rowLimit: rowLimit})
	if reader.err != nil {
		return model.QueryResult{}, reader.err
	}
	if strings.Contains(sqlText, "information_schema.columns") {
		return reader.metadataResult, nil
	}
	return reader.sampleResult, nil
}

func TestServiceCandidateOnlySamplingAndLimitDoubleBarrier(t *testing.T) {
	reader := &fakeReadExecutor{
		metadataResult: model.QueryResult{Columns: []string{"table_schema", "table_name", "column_name", "data_type", "udt_name", "ordinal_position"}, Rows: [][]string{
			{"public", "customers", "phone", "text", "text", "1"},
			{"public", "customers", "description", "text", "text", "2"},
		}},
		sampleResult: model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13800138000"}, {"13900139000"}, {"13700137000"}, {"13600136000"}, {"T38_RAW_SAMPLE_SENTINEL_7b19"}}},
	}
	service, pool := newFakeService(t, reader)
	result, err := service.Discover(context.Background(), "admin", "ds-1", discovery.ScanRequest{Tables: []discovery.TableRef{{Schema: "public", Table: "customers"}}, SampleRows: 5})
	require.NoError(t, err)
	require.Equal(t, 2_000, pool.opened.StmtTimeoutMS)
	require.Equal(t, 2, result.Stats.ColumnsSeen)
	require.Equal(t, 2, result.Stats.CandidateColumns)
	require.Equal(t, 1, result.Stats.SampledColumns)
	require.Len(t, result.Findings, 2)
	require.Equal(t, discovery.CategoryGeneric, result.Findings[1].Category)
	require.False(t, result.Findings[1].Applicable)
	require.Len(t, reader.queries, 2)
	require.Equal(t, metadataRowLimit, reader.queries[0].rowLimit)
	require.Equal(t, 5, reader.queries[1].rowLimit)
	require.Contains(t, reader.queries[1].text, `SELECT "phone" FROM "public"."customers" LIMIT 5`)
	require.NotContains(t, reader.queries[1].text, "description")
	require.NotContains(t, reader.queries[1].text, "WHERE")
	require.NotContains(t, fmt.Sprintf("%v", result), "T38_RAW_SAMPLE_SENTINEL_7b19")
}

func TestServiceSamplingFalseExecutesMetadataOnly(t *testing.T) {
	reader := &fakeReadExecutor{metadataResult: model.QueryResult{Columns: []string{"table_schema", "table_name", "column_name", "data_type", "udt_name", "ordinal_position"}, Rows: [][]string{{"public", "customers", "phone", "text", "text", "1"}}}}
	service, _ := newFakeService(t, reader)
	no := false
	result, err := service.Discover(context.Background(), "admin", "ds-1", discovery.ScanRequest{Tables: []discovery.TableRef{{Schema: "public", Table: "customers"}}, Sampling: &no})
	require.NoError(t, err)
	require.Len(t, reader.queries, 1)
	require.Zero(t, result.Stats.SampledColumns)
	require.Zero(t, result.Stats.SampledValuesCount)
}

func TestServiceFailsClosedOnPermissionAndMalformedMetadata(t *testing.T) {
	t.Run("permission", func(t *testing.T) {
		reader := &fakeReadExecutor{err: executor.ErrPermissionDenied}
		service, _ := newFakeService(t, reader)
		_, err := service.Discover(context.Background(), "admin", "ds-1", discovery.ScanRequest{Tables: []discovery.TableRef{{Schema: "public", Table: "customers"}}})
		require.ErrorIs(t, err, discovery.ErrPermissionDenied)
	})
	t.Run("metadata width", func(t *testing.T) {
		reader := &fakeReadExecutor{metadataResult: model.QueryResult{Columns: []string{"unexpected"}, Rows: [][]string{{"sentinel"}}}}
		service, _ := newFakeService(t, reader)
		_, err := service.Discover(context.Background(), "admin", "ds-1", discovery.ScanRequest{Tables: []discovery.TableRef{{Schema: "public", Table: "customers"}}})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "sentinel")
	})
}

func TestServiceUsesFixedRequestDatasourceAndGlobalAdmission(t *testing.T) {
	reader := &fakeReadExecutor{metadataResult: model.QueryResult{Columns: []string{"table_schema", "table_name", "column_name", "data_type", "udt_name", "ordinal_position"}, Rows: [][]string{{"public", "customers", "description", "text", "text", "1"}}}}
	datasource := model.Datasource{ID: "ds-1", DBType: "postgres", Database: "app", StmtTimeoutMS: 5_000}
	pool := &fakeExecutorPool{executor: reader}
	limiter := &recordingLimiter{}
	service, err := NewService(fakeDatasourceReader{datasource: datasource}, pool, []byte("0123456789abcdef0123456789abcdef"), limiter)
	require.NoError(t, err)
	_, err = service.Discover(context.Background(), "alice", "ds-1", discovery.ScanRequest{Tables: []discovery.TableRef{{Schema: "public", Table: "customers"}}})
	require.NoError(t, err)
	require.Equal(t, []limiterCall{
		{key: "admin:alice:ds-1", qps: 1, concurrent: 1},
		{key: "admin:alice", qps: 1, concurrent: discovery.MaxTables},
		{key: "datasource:ds-1", qps: capacityOnlyQPS, concurrent: 1},
		{key: "discovery:global", qps: capacityOnlyQPS, concurrent: 2},
	}, limiter.calls)
	require.Equal(t, []string{"discovery:global", "datasource:ds-1", "admin:alice", "admin:alice:ds-1"}, limiter.releases)
}

func newFakeService(t *testing.T, reader *fakeReadExecutor) (*Service, *fakeExecutorPool) {
	t.Helper()
	datasource := model.Datasource{ID: "ds-1", DBType: "postgres", Database: "app", StmtTimeoutMS: 5_000}
	pool := &fakeExecutorPool{executor: reader}
	service, err := NewService(fakeDatasourceReader{datasource: datasource}, pool, []byte("0123456789abcdef0123456789abcdef"), rules.NewDefaultTokenBucketLimiter())
	require.NoError(t, err)
	return service, pool
}
