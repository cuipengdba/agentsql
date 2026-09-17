# T25 网关开销基线

本目录只定义可重复的测量方法，不记录未经实测复现的性能数字。

## 内存执行器基线

以下 benchmark 使用内存 fake executor 跑完整 AgentSQL 八阶段流程，包含鉴权、数据源与策略装载、SQL parse、静态/动态规则、受控 Query、结果脱敏与审计，但剥离真实数据库网络和执行耗时：

```bash
go test ./internal/pipeline -run '^$' -bench BenchmarkPipelineReadOnlyParallel -benchtime=2s -count=5
```

benchmark 使用 `b.RunParallel` 并开启 `ReportAllocs`。报告应记录每次运行的 Go 版本、CPU、操作系统、并发度、`ns/op`、`B/op` 与 `allocs/op`。验收口径是只读路径的网关额外开销 P99 小于 5ms/请求，最终数字以实测环境为准。

## 可选真实 PostgreSQL 对照

真实库 benchmark 默认跳过。显式提供独立测试库 URL 后运行：

```bash
export AGENTSQL_BENCH_DSN='postgres://user:password@127.0.0.1:5432/agentsql_bench?sslmode=disable'
go test ./internal/pipeline -run '^$' -bench BenchmarkPipelineReadOnlyRealDatabase -benchtime=2s -count=5
```

该结果包含真实数据库连接、EXPLAIN 与查询耗时，只用于对照，不等同于网关纯额外开销。

## 结果归档

实测完成后在 `docs/perf/t25_bench_result.md` 记录环境、原始命令、五次样本和 P99 结论；未在真实环境跑通前不得预填性能数字。
