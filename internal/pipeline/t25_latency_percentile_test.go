//go:build !race

package pipeline

// 主控端补充：并发只读决策的延迟百分位采样（复用 t25_bench_test.go 的内存 fake）。
// 性能预算 P99<5ms 仅在独占、非 race、非 -short 口径衡量：`-race` 插桩会使延迟数倍
// 放大（本机实测约 3.8x，P99 从 3.2ms 升至 10ms），故用 //go:build !race 在 race
// 套件中排除；它又是 CPU 抢占型微基准，跨包并行全量套件里会因争用抖动，故在
// `go test -short ./...` 中 testing.Short() 跳过。常规功能/语料全量走 -short，性能
// 门禁独占运行本用例。目的：在剥离真实数据库耗时的口径下，量出网关八阶段流水线的
// P50/P95/P99/P999，验收口径为独占 P99 < 5ms/请求。运行：
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
			Executors:     t25BenchmarkExecutorProvider{statement: &t25BenchmarkExecutor{}},
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
	// 这是 GOMAXPROCS 个 worker、约 20 万样本抢占 CPU 的独占式 P99 微基准。混在
	// `go test -short ./...` 跨包并行全量套件里会与其它重测试（容器 E2E、-race 余热）
	// 争用 CPU，在繁忙/共享/发热机器上 P99 被调度抖动放大而偶发误报（本机实测独占
	// P99≈1.8ms，全量并行时可飙到 7ms+，吞吐同步从 67k 跌到 36k ops/s）。因此常规
	// 全量用 -short 跳过本用例；性能门禁由验收脚本在收尾阶段对本包独占、不带 -short
	// 运行（口径见文件头注释），P99 预算 5ms 仅在该独占口径下判定。
	if testing.Short() {
		t.Skip("skipping exclusive P99 microbenchmark in -short/full suite; run solo: go test -run TestT25LatencyPercentile ./internal/pipeline/")
	}
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
