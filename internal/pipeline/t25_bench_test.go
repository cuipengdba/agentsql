package pipeline

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
)

var t25BenchmarkSecret = []byte("0123456789abcdef0123456789abcdef")

func BenchmarkPipelineReadOnlyParallel(b *testing.B) {
	datasource := t25BenchmarkDatasource()
	flow := newT25BenchmarkPipeline(b, datasource, &t25BenchmarkExecutor{})
	request := t25BenchmarkRequest(datasource.ID)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			response, err := flow.Process(context.Background(), request)
			if err != nil || response.Decision != model.DecisionAllow {
				b.Errorf("benchmark pipeline result decision=%q err=%v", response.Decision, err)
				return
			}
		}
	})
}

func BenchmarkPipelineReadOnlyRealDatabase(b *testing.B) {
	rawDSN := strings.TrimSpace(os.Getenv("AGENTSQL_BENCH_DSN"))
	if rawDSN == "" {
		b.Skip("AGENTSQL_BENCH_DSN is not set")
	}
	datasource, password, err := t25PostgresDatasourceFromURL(rawDSN)
	if err != nil {
		b.Fatal(err)
	}
	databaseExecutor, err := executor.NewPostgresExecutor(
		context.Background(),
		datasource,
		password,
		true,
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := databaseExecutor.Close(); err != nil {
			b.Error(err)
		}
	})
	flow := newT25BenchmarkPipeline(b, datasource, databaseExecutor)
	request := t25BenchmarkRequest(datasource.ID)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		response, err := flow.Process(context.Background(), request)
		if err != nil || response.Decision != model.DecisionAllow {
			b.Fatalf("real benchmark pipeline result decision=%q err=%v", response.Decision, err)
		}
	}
}

func newT25BenchmarkPipeline(
	b *testing.B,
	datasource model.Datasource,
	databaseExecutor executor.Executor,
) *Pipeline {
	b.Helper()
	redactor, err := mask.NewRedactor(nil)
	if err != nil {
		b.Fatal(err)
	}
	flow, err := New(
		Ports{
			Authenticator: t25BenchmarkAuthenticator{},
			Datasources:   t25BenchmarkDatasourceReader{datasource: datasource},
			Policies:      t25BenchmarkPolicyLoader{},
			Executors:     t25BenchmarkExecutorProvider{executor: databaseExecutor},
			Approvals:     t25BenchmarkApprovalWriter{},
			Audit:         t25BenchmarkAuditRecorder{},
			Redactors:     t25BenchmarkRedactorBuilder{redactor: redactor},
		},
		t25BenchmarkSecret,
		WithRuleLayers(engine.RuleLayers{Global: engine.RuleLayer{
			"R008": {Thresholds: map[string]float64{
				rules.ThresholdQPS:           1_000_000_000,
				rules.ThresholdMaxConcurrent: 1_000_000,
			}},
		}}),
	)
	if err != nil {
		b.Fatal(err)
	}
	return flow
}

func t25BenchmarkDatasource() model.Datasource {
	return model.Datasource{
		ID:            "t25-benchmark-datasource",
		DBType:        "postgres",
		RowLimit:      1,
		StmtTimeoutMS: 5_000,
	}
}

func t25BenchmarkRequest(datasourceID string) Request {
	return Request{
		APIKey:       "asql_t25_benchmark",
		DatasourceID: datasourceID,
		SQL:          "SELECT 1 LIMIT 1",
		MCPTool:      "query",
	}
}

func t25PostgresDatasourceFromURL(rawDSN string) (model.Datasource, string, error) {
	parsed, err := url.Parse(rawDSN)
	if err != nil {
		return model.Datasource{}, "", fmt.Errorf("parse AGENTSQL_BENCH_DSN: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return model.Datasource{}, "", fmt.Errorf("AGENTSQL_BENCH_DSN must be a PostgreSQL URL")
	}
	if parsed.User == nil || parsed.User.Username() == "" || parsed.Hostname() == "" {
		return model.Datasource{}, "", fmt.Errorf("AGENTSQL_BENCH_DSN must include username and host")
	}
	port := 5432
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil {
			return model.Datasource{}, "", fmt.Errorf("parse AGENTSQL_BENCH_DSN port: %w", err)
		}
	}
	database := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if unescaped, unescapeErr := url.PathUnescape(database); unescapeErr == nil {
		database = unescaped
	} else {
		return model.Datasource{}, "", fmt.Errorf("parse AGENTSQL_BENCH_DSN database: %w", unescapeErr)
	}
	if database == "" {
		return model.Datasource{}, "", fmt.Errorf("AGENTSQL_BENCH_DSN must include database name")
	}
	password, _ := parsed.User.Password()
	host := parsed.Hostname()
	if net.ParseIP(host) == nil {
		host = strings.TrimSpace(host)
	}
	return model.Datasource{
		ID:            "t25-real-benchmark-datasource",
		DBType:        "postgres",
		Host:          host,
		Port:          port,
		Database:      database,
		Username:      parsed.User.Username(),
		ConnLimit:     5,
		StmtTimeoutMS: 5_000,
		RowLimit:      1,
	}, password, nil
}

type t25BenchmarkAuthenticator struct{}

func (t25BenchmarkAuthenticator) Authenticate(context.Context, string) (model.Agent, error) {
	return model.Agent{ID: "t25-benchmark-agent", Status: "active", Level: "readonly"}, nil
}

type t25BenchmarkDatasourceReader struct {
	datasource model.Datasource
}

func (reader t25BenchmarkDatasourceReader) Get(context.Context, string) (model.Datasource, error) {
	return reader.datasource, nil
}

type t25BenchmarkPolicyLoader struct{}

func (t25BenchmarkPolicyLoader) ListByAgentAndDatasource(context.Context, string, string) ([]model.Policy, error) {
	return []model.Policy{}, nil
}

type t25BenchmarkExecutorProvider struct {
	executor executor.Executor
}

func (provider t25BenchmarkExecutorProvider) GetOrOpen(model.Datasource, []byte) (executor.Executor, error) {
	return provider.executor, nil
}

type t25BenchmarkApprovalWriter struct{}

func (t25BenchmarkApprovalWriter) Create(_ context.Context, approval model.Approval) (model.Approval, error) {
	return approval, nil
}

type t25BenchmarkAuditRecorder struct{}

func (t25BenchmarkAuditRecorder) Record(_ context.Context, log model.AuditLog) (model.AuditLog, error) {
	log.ID = 1
	return log, nil
}

type t25BenchmarkRedactorBuilder struct {
	redactor mask.Redactor
}

func (builder t25BenchmarkRedactorBuilder) RedactorFor(context.Context, string) (mask.Redactor, error) {
	return builder.redactor, nil
}

type t25BenchmarkExecutor struct{}

func (*t25BenchmarkExecutor) Dialect() string            { return "postgres" }
func (*t25BenchmarkExecutor) Ping(context.Context) error { return nil }
func (*t25BenchmarkExecutor) OpenSession(context.Context, string) (executor.Session, error) {
	return nil, fmt.Errorf("benchmark does not use sessions")
}
func (*t25BenchmarkExecutor) Explain(context.Context, string) (model.ExplainInfo, error) {
	return model.ExplainInfo{EstScanRows: 1, UsesIndex: true}, nil
}
func (*t25BenchmarkExecutor) Query(context.Context, string, int) (model.QueryResult, error) {
	return model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{"1"}}, RowCount: 1}, nil
}
func (*t25BenchmarkExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{}, fmt.Errorf("benchmark executor is read-only")
}
func (*t25BenchmarkExecutor) Close() error                                { return nil }
func (*t25BenchmarkExecutor) TableHasIndex(string, string) (bool, error)  { return true, nil }
func (*t25BenchmarkExecutor) TableRowCount(string, string) (int64, error) { return 1, nil }
func (*t25BenchmarkExecutor) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, nil
}

var _ executor.Executor = (*t25BenchmarkExecutor)(nil)
var _ rules.TransactionMetadataProvider = (*t25BenchmarkExecutor)(nil)
