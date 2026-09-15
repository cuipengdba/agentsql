package pipeline

// 主控端补充：并发只读决策的延迟百分位采样（复用 t25_bench_test.go 的内存 fake）。
// 目的：在剥离真实数据库耗时的口径下，量出网关八阶段流水线的 P50/P95/P99/P999，
// 验收口径为 P99 < 5ms/请求。运行：
//
//	go test ./internal/pipeline -run TestT25LatencyPercentile -v -count=1
import (
	"context"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
)

// newT25LatencyPipeline 与 t25_bench_test.go 的构造等价，仅形参用 *testing.T。
func newT25LatencyPipeline(t *testing.T, datasource model.Datasource) *Pipeline {
	t.Helper()
	redactor, err := mask.NewRedactor(nil)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := New(
		Ports{
			Authenticator: t25BenchmarkAuthenticator{},
			Datasources:   t25BenchmarkDatasourceReader{datasource: datasource},
			Policies:      t25BenchmarkPolicyLoader{},
			Executors:     t25BenchmarkExecutorProvider{executor: &t25BenchmarkExecutor{}},
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
		t.Fatal(err)
	}
	return flow
}

func TestT25LatencyPercentile(t *testing.T) {
	datasource := t25BenchmarkDatasource()
	flow := newT25LatencyPipeline(t, datasource)
	request := t25BenchmarkRequest(datasource.ID)

	workers := runtime.GOMAXPROCS(0)
	perWorker := 10000
	results := make([][]time.Duration, workers)

	start := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			local := make([]time.Duration, 0, perWorker)
			ctx := context.Background()
			for index := 0; index < perWorker; index++ {
				begin := time.Now()
				resp, err := flow.Process(ctx, request)
				elapsed := time.Since(begin)
				if err != nil || resp.Decision != model.DecisionAllow {
					t.Errorf("unexpected decision=%q err=%v", resp.Decision, err)
					return
				}
				local = append(local, elapsed)
			}
			results[id] = local
		}(worker)
	}
	wg.Wait()
	wall := time.Since(start)

	total := 0
	for _, local := range results {
		total += len(local)
	}
	all := make([]time.Duration, 0, total)
	for _, local := range results {
		all = append(all, local...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	percentile := func(percent float64) time.Duration {
		index := int(float64(len(all)-1) * percent)
		return all[index]
	}
	p50 := percentile(0.50)
	p95 := percentile(0.95)
	p99 := percentile(0.99)
	p999 := percentile(0.999)
	maxLatency := all[len(all)-1]
	throughput := float64(len(all)) / wall.Seconds()

	t.Logf("workers=%d samples=%d wall=%s throughput=%.0f ops/s", workers, len(all), wall, throughput)
	t.Logf("latency P50=%s P95=%s P99=%s P99.9=%s Max=%s", p50, p95, p99, p999, maxLatency)

	const budget = 5 * time.Millisecond
	if p99 >= budget {
		t.Fatalf("P99 %s exceeds budget %s", p99, budget)
	}
	t.Logf("PASS: P99 %s is %.1fx below the 5ms budget", p99, float64(budget)/float64(p99))
}
