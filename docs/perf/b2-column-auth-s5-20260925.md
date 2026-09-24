# AgentSQL v0.4 B2 S5 列级 SELECT 负载观测

日期：2026-09-25

本报告是 S5 的短窗口工程证据，不是 GA 或发布默认开启结论。B2 出厂默认仍为 off。

## 环境与命令

- Docker Desktop 29.8.0，约 11.9 GB 可用内存。
- PostgreSQL：由 `dbext/postgres/agentsql_binder/Dockerfile.test` 构建的 PG16、PG18 真容器。
- Go 1.25.14，CGO enabled，Windows named pipe `npipe:////./pipe/docker_engine`。
- 每个 major：4 workers、10 秒；完整列级 SELECT 路径包含 binder、catalog Fpre/Fpost、只读执行、mask、encode、audit callback、final fence callback 与 seal。

```powershell
$env:AGENTSQL_B2_COLUMN_LOAD='1'
$env:AGENTSQL_B2_LOAD_SECONDS='10'
go test ./internal/authorizedexecute -run '^TestB2ColumnSelectProductionLoad$' -count=1 -v
```

## 结果

| PG | 完成请求 | 吞吐 | p95 | p99 | 对 S0 P99 5.5ms 的增量 | catalog+binder p95 | 错误码 | reservation 触发 |
|---|---:|---:|---:|---:|---:|---:|---|---|
| 16 | 405 | 40.20/s | 112.01ms | 125.57ms | +120.07ms | 86.89ms | 0 | 是 |
| 18 | 432 | 42.97/s | 110.00ms | 122.92ms | +117.42ms | 86.13ms | 0 | 是 |

两个 major 的端到端 P99 均低于本门的 250ms 上限，且远低于 2s 总持锁 wall 上限；没有观察到授权、数据库、frame、audit 或 reservation 错误。reservation 证据通过先占满同一 agent 的 4 个槽位并断言下一次申请稳定返回 `AUTH_CONCURRENCY_LIMIT` 取得。

增量不能解释为纯 SQL 执行成本：每个请求按安全设计重新执行 binder、锁、catalog Fpre/Fpost，因此 catalog+binder 是主要开销。该短窗口不替代 S7 的攻击总验收或 S8 的真实部署长时间 SLO/锁争用演练。
