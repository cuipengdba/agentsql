# PostgreSQL parser 性能复核（2026-10-05）

## 口径与复现

发布闸门仍是 `TestParseProjectionLineageP99Budget/postgres`：8 个 `UNION ALL` 分支、每分支 8 列，单轮 2,000 次完整 `Parser.Parse`，最近秩 P99 必须 **≤ 5 ms**，连续 5 轮全过。它测量完整解析与血缘，不是数据库执行延迟。标准 Go benchmark 的 `ns/op` 是均值，不能代替 P99。测试源码中的门槛和样本数没有修改。

本次使用 `golang:1.26-bookworm`（Go 1.26.8）、Linux/amd64、WSL2 6.18.40.1、Intel i7-6700（4 核 8 线程）、`GOMAXPROCS=8`。源码以只读方式挂载；性能测量期间未并行执行本任务的其他构建或测试。没有证明宿主机完全没有其他后台负载。

在 Linux 仓库根目录运行：

```sh
sh scripts/parser-bench.sh gate
sh scripts/parser-bench.sh bench
sh scripts/parser-bench.sh profile
```

在 Docker Desktop 上可用同一镜像与只读源码重现：

```sh
docker run --rm --platform linux/amd64 -e GOMAXPROCS=8 -e GOTOOLCHAIN=local \
  -v "$PWD:/src:ro" -w /src golang:1.26-bookworm \
  sh scripts/parser-bench.sh gate
```

`scripts/parser-bench.sh all` 顺序运行 parser short tests、闸门、benchmark、profile。闸门失败时脚本立即退出；仍可单独运行 `profile` 调查。profile 文件写入容器临时目录，脚本在 stdout 打印 CPU 与 `alloc_space` 摘要。

## 五轮结果

本次在现有代码上直接测得以下两组结果，单位 ms。第二组在新增 benchmark 和边界测试之后运行；生产 parser 逻辑没有改动。

| 轮次 | 初始基线 P50 | 初始基线 P99 | 复核 P50 | 复核 P99 | 复核闸门 |
| ---: | ---: | ---: | ---: | ---: | :---: |
| 1 | 0.625 | 1.924 | 0.618 | 1.917 | PASS |
| 2 | 0.542 | 1.881 | 0.576 | 2.075 | PASS |
| 3 | 0.546 | 1.869 | 0.557 | 1.994 | PASS |
| 4 | 0.567 | 1.934 | 0.579 | 2.011 | PASS |
| 5 | 0.574 | 1.958 | 0.551 | 1.911 | PASS |

两组命令退出码均为 0，均 **5/5 PASS**。用户报告的 9.50 ms P99 在本次环境中未复现。仓库此前在相同 CPU 型号与 Go 镜像系列下记录的优化后 P99 为 4.456/4.775/6.404/6.554/4.173 ms，只有 3/5 PASS（`docs/release-notes-v0.5.md`）。本次无生产代码性能改动，因此不能把两次结果差异归为新优化；宿主负载、调度或 GC 的具体贡献未被独立测定。过去的失败记录仍有效，发布日应在指定、固定且无无关负载的 Linux runner 上重新独占执行五轮；任一轮超过 5 ms 即按现有门槛停止发布。

## 场景、成本与 profile

`BenchmarkPostgresParseCorpus` 覆盖 SELECT、JOIN、带数字和字符串字面量的 WHERE、CTE、DDL，以及发布闸门的 8×8 UNION SQL。三次 benchmark 的范围如下；它们是平均耗时与每次分配量，不是 P99。

| 场景 | ns/op 范围 | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| SELECT | 25,032–25,866 | 8,932 | 120 |
| JOIN | 59,079–71,154 | 23,300 | 280 |
| WHERE + literals | 92,126–92,936 | 26,364 | 328 |
| CTE | 75,803–79,436 | 27,814 | 337 |
| DDL | 26,870–32,515 | 10,831 | 144 |
| 8×8 UNION | 759,008–822,845 | 316,434–316,439 | 3,578 |

分段 benchmark 中，典型 SQL 的 `pg_query.ParseToJSON` 为 74–89 µs / 1 次分配，通用 JSON 树解码为 367–392 µs / 2,888 次分配，完整 `Parser.Parse` 为 767–787 µs / 3,578 次分配。这些分段不可简单相加为逐次端到端时延，仍说明 JSON 解码占主要可见成本。

5 秒完整解析 profile 的 `alloc_space`：`encoding/json.(*decodeState).objectInterface` 平坦占 57.95%，Decoder refill 占 10.56%，`resolveQualifiedBinding` 占 4.86%，`pg_query` 的 GoString 占 3.68%。CPU profile 中 `runtime.cgocall` 平坦占 8.63%，`runtime.futex` 8.75%，JSON decoder、map 遍历、malloc 和 GC 扫描均有可见成本。`cgocall` 样本只说明调用边界上的 CPU 样本，不能将全部尾延迟归因于 cgo；没有单一稳定热点足以证明一个小的生产代码修改会让所有 runner 的 P99 下降。

## 语义与发布结论

本次没有替换 PostgreSQL AST、缓存解析结果或调整 5 ms 门槛。`go test -short ./internal/parser -count=1` 通过。新增测试验证超过 1 MiB 的字符串字面量不会进入 `Normalized`，且字面量内分号不被误判为多语句、真正双语句仍返回 `ErrUnparseable`。`AST.RawSQL` 按现有契约仍保存原始 SQL，调用方不能将它当作脱敏日志字段。原有测试覆盖 NULL/三值逻辑、注释与大小写、并发解析、深度和投影数硬上限。parser 本机闸门 PASS 不能单独表示整个 v0.5.0 发布已获准。

`go test -short ./internal/parser ./internal/rules ./internal/pipeline -count=1 -p 1` 在同一 Linux 镜像中三包均 PASS、退出码 0。全仓 `go test -short ./... -p 1` 的首次冷编译/测试尝试设有 10 分钟上限；仅获得 `cmd/agentsql` 与 `cmd/agentsqlctl` 的 PASS 后以非零状态结束，其余包没有形成结果，故全仓 short 测试状态是**未完成**，不能记为 PASS。发布前仍需在有持久 Go 缓存的 CI runner 上完成全仓验证。
