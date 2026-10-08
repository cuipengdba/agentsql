# v0.5 测试覆盖与离线限制（待发布）

本文记录批六十七新增边界/错误路径测试的**源码覆盖范围**，不把测试文件存在或本次文档核对写成测试通过、真库通过或发布闸门通过。发布日执行要求见 [发布日清单](release-day-checklist-v0.5.md)。

## 批六十七新增范围

| 测试源码 | 已编码断言 | 未由此证明 |
| --- | --- | --- |
| `internal/parser/boundary_error_paths_test.go` | MySQL、DM、SQL Server 的空串、空白、未闭合字符串、不完整 `ORDER BY` 与堆叠语句返回 `ErrUnparseable`；保留非空 AST 的 dialect/原文，重复解析错误文本稳定，失败后同一 parser 可处理有效 SELECT；DM/SQL Server 的无效 UTF-8、未闭合注释、超长 SQL 被拒；并发畸形输入保持方言隔离。 | Oracle/YashanDB 的全部语法、真库执行、标准 Yashan `NewParser` 路由，以及所有可能的畸形输入。 |
| `internal/pipeline/boundary_error_paths_test.go` | 空 SQL、空白 SQL、不完整 `ORDER BY` 经 `Process` 返回 `decision=error`、`DB_SYNTAX_ERROR` / parse 阶段、无结果；假执行器/会话方法零调用，审计 fixture 记录一次 error。 | 真实数据库零触达的独立端到端证据、所有 pipeline 错误路径、发布日全仓测试结果。 |

以上断言的方言和错误码取自对应测试源码；不能用 parser 的离线接受范围扩大 DM/Oracle/YashanDB 在完整网关的支持结论。DM/Oracle 批六十三回归范围见 [方言边界](dm-oracle-dialect.md)，YashanDB 批六十六离线 profile 及标准入口限制见 [YashanDB 边界](yashan-dialect.md)。

## 离线执行与依赖边界

- 本次文档批次未运行 Go 测试，也未连接数据库、拉取镜像或下载依赖。上述表格是源码核对结论，**不是本批 PASS 记录**。
- `internal/pipeline` 的测试包包含 `e2e_mysql_test.go` 和 `e2e_postgres_test.go`，它们 import `testcontainers-go`；是否在 `-short` 下跳过真库路径，应以对应测试守卫和实际运行输出为准。即使只运行边界测试名，Go 也要先编译该包的测试依赖；离线环境若缺工具链、CGO 或模块缓存，无法把编译失败归因于边界断言。
- 仓库 [PostgreSQL parser 性能复核](PARSER_PERF.md) 记录：指定 Linux/Go 1.26.8 环境中 `go test -short ./internal/parser ./internal/rules ./internal/pipeline -count=1 -p 1` 三包通过；全仓 `go test -short ./... -p 1` 在时限内未完成，因此全仓 short 状态不能写成 PASS。P99 两组 5/5 结果也只代表该次指定环境，发布日仍需独占复测。
- 发布日需要在预备好依赖、可记录完整退出码的环境里重跑 parser、rules、pipeline 与全仓测试；真库 E2E 另按发布清单和显式环境门执行。离线缺依赖时记录为“未执行/未完成”，不能记为测试通过。
