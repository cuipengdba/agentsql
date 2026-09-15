# T25 网关只读路径开销基线（主控端实测存档）

> 本文件由主控端在合入后实测生成，Codex 交付不预填任何性能数字。测量口径为**内存 fake executor**：跑完整 AgentSQL 八阶段流水线（鉴权、数据源/策略装载、SQL parse、静态/动态规则、受控 Query、脱敏、审计映射），但剥离真实数据库的网络与执行耗时，因此数值代表**网关自身的纯额外开销**，不含数据库执行时间。

## 1. 测试环境

| 项 | 值 |
|---|---|
| 操作系统 | Microsoft Windows NT 10.0.26200.0（amd64） |
| CPU | 13th Gen Intel Core i9-13900H，20 逻辑核 |
| Go 工具链 | go1.27.0 windows/amd64（CGO 开启，pg_query 走 cgo） |
| 模块版本 | AgentSQL v0.1，T25 合入后工作树 |
| 并发度 | GOMAXPROCS=20，`b.RunParallel` / 20 worker |

## 2. 标准并行 benchmark（5 次）

命令：

```bash
go test ./internal/pipeline -run '^$' -bench 'BenchmarkPipelineReadOnlyParallel$' -benchtime=2s -count=5
```

| 次数 | 迭代数 | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| 1 | 104362 | 23433 | 34774 | 428 |
| 2 | 90679 | 33042 | 34793 | 428 |
| 3 | 74780 | 35800 | 34813 | 428 |
| 4 | 64891 | 34059 | 34840 | 428 |
| 5 | 77144 | 29751 | 34615 | 427 |

5 次平均 **31217 ns/op ≈ 0.031 ms/请求**，每次决策约 34.8 KB 堆分配、428 次分配。

## 3. 并发延迟百分位（20 万样本）

标准 benchmark 只给均值，另用主控补充的 `internal/pipeline/t25_latency_percentile_test.go` 在 20 worker 下各跑 10000 次、共 200000 次完整只读决策，逐次计时后排序取分位。

命令：

```bash
go test ./internal/pipeline -run TestT25LatencyPercentile -v -count=1
```

结果：

| 指标 | 值 |
|---|---|
| 样本数 / 墙钟 | 200000 / 2.843 s |
| 聚合吞吐 | 70343 ops/s |
| P50 | < 1 µs（亚微秒） |
| P95 | 1.1052 ms |
| **P99** | **1.8078 ms** |
| P99.9 | 3.2926 ms |
| Max | 6.7632 ms（20 万次中 1 次，Go 调度/GC 尾抖） |

## 4. 验收结论

- 验收口径：只读路径网关额外开销 **P99 < 5 ms/请求**。
- 实测 **P99 = 1.81 ms，为预算的 1/2.8，达标**；P99.9 = 3.29 ms 亦在 5 ms 内。
- 该数值来自 20 核满载竞争的偏严苛口径；真实部署中网关单核处理、数据库执行才是主要耗时，网关占比更低。
- 唯一一次 Max 6.76 ms 为运行时偶发尾抖（非稳定分位），后续可通过对象池（降低 428 allocs/op）进一步压缩，不影响 v0.1 验收。

## 5. 复现实验

```bash
# 标准 benchmark
go test ./internal/pipeline -run '^$' -bench BenchmarkPipelineReadOnlyParallel -benchtime=2s -count=5
# 百分位采样
go test ./internal/pipeline -run TestT25LatencyPercentile -v -count=1
# 可选：真实 PostgreSQL 对照（默认 Skip，设置 AGENTSQL_BENCH_DSN 后启用，含真实 DB 时间，非纯网关开销）
```
