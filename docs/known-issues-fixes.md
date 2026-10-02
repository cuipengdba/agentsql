# v0.5 已知问题修复记录（批十四 / #36）

记录日期：2026-10-02。

## 调研范围与结论

- GitHub 公共 REST 查询 `cuipengdba/agentsql`：仓库 `has_issues=true`，`open_issues_count=0`；`state=open` 与 `state=all` 均返回空数组；直接读取 issue `#36` 返回 404。因此当前公开仓库没有可列出的 open issue，`#36` 按本任务编号记录，不能据此编造远端 issue 内容。
- 对项目自有源码、配置、脚本、测试与 `docs/` 做 `TODO` / `FIXME` / `XXX` 大小写敏感词边界扫描，未发现待处理标记。`docs/v0.5-known-issues-task7.md` 中有一处对既往扫描结果的文字记录，不是新 TODO；`web/node_modules` 中有上游依赖的 FIXME，不属于项目自有代码。
- 仓库内明确且仍有效的产品已知问题是 R005 生产动态告警缺失。另有 R005 前端规则元数据错误、parser P99 环境相关波动，以及真实 MCP 客户端和 Testcontainers/Ryuk 待实测项。
- 本批没有运行任何 Git 命令。

## 优先级清单

| 优先级 | 项目 | 结论 | 理由 |
| --- | --- | --- | --- |
| P0 | R005 在普通生产查询中可能不产生大结果集告警 | **本次不修，后续安全小批** | 用户可见的风险告警缺失；执行层 `row_limit` 仍截断结果，但不能用截断替代风险命中。最小分类改动无法覆盖默认 PostgreSQL 列级授权闭环，半修复会造成错误的支持口径。 |
| P1 | PostgreSQL parser lineage P99 偶发超过 5 ms | **环境相关，只记录** | 既往 Windows Docker 独占复现仍受调度影响，缺少固定 Linux runner 的 CPU/alloc profile，不能稳定归因。不得放宽门槛掩盖失败。 |
| P2 | R005 在规则页元数据中被标为静态 | **已修复** | R005 的判定读取 `AST.Explain`，前端显示“静态”与实现、用户指南均矛盾，会误导管理员。 |
| 环境 | Testcontainers/Ryuk、Windows Docker 文件系统、宿主缺少 `rg`/Go/Node、真实豆包/Claude 客户端 | **不修，只记录** | 均需要运行环境、工具链或真实客户端/账号，不应据此改生产逻辑。 |

数据库兼容性文档中的“待实测/未实现”是已声明的产品边界，不属于 #36 的可直接修复缺陷。本批未改 Yashan、OpenTenBase、DM 或 Oracle dialect。

## P0：R005 生产动态告警缺失

### 现象

无 `LIMIT` 的 `SELECT` 即使 EXPLAIN 估算超过 R005 阈值，普通生产流水线也可能返回 `allow` 且没有 R005 命中。结果仍按数据源 `row_limit` 截断，所以数据返回上限没有失守，但风险提示、规则页预期和审计命中不完整。

### 复现路径

1. 构造无 `LIMIT` 的 PostgreSQL 或 MySQL `SELECT`。
2. 令受控 EXPLAIN 返回大于 R005 阈值的 `EstScanRows`。
3. 调用 `Pipeline.Process`。
4. `internal/pipeline/demo_test.go` 的既有回归用例明确记录：Demo 返回 `warn` 并命中 R005，而 production 返回 `allow` 且不含 R005。

### 根因

`internal/pipeline/rules.go` 仅在 Demo 模式把 R005 放入动态规则集合；生产静态阶段执行 R005 时 `AST.Explain` 尚为空，因此规则按允许返回。常规动态阶段随后虽执行 EXPLAIN，却不再评估 R005。

此外，默认启用的 PostgreSQL 列级授权 SELECT 路径在静态门禁后进入独立的绑定、授权、脱敏、持久审计、最终 fence 和 delivery seal 闭环，不经过通用动态阶段。简单把 R005 加入动态集合只能修复非列级授权路径，不能覆盖默认生产路径。

### 后续修复方案

单独设计列级授权路径的动态事实传递：必须复用同一受控准备语句/业务快照，禁止为 EXPLAIN 新增第二个未经证明的 SQL 入口；动态 assessment 必须在执行许可与持久审计前确定，并进入同一审计记录。未知计划格式、事实缺失、EXPLAIN 或审计失败均保持 fail-closed。

### 验收标准

- 普通路径与默认 PostgreSQL 列级授权路径中，无 `LIMIT` 且估算超过阈值的查询均命中 R005 并返回 `warn`；带 `LIMIT`、未分组纯聚合及阈值相等场景不误报。
- 规则覆盖 PostgreSQL/MySQL；未知计划格式和 EXPLAIN 错误不得跳过门禁后执行。
- R005 命中、估算行数、最终决策写入同一持久审计；列级授权、脱敏、final fence、delivery seal 和 `row_limit` 语义不变。
- 提供真实失败到修复后通过的单测，并通过列级授权攻击矩阵与相关 E2E。

本批不提交只覆盖部分路径的一行分类改动，也不削弱授权、脱敏、审计或查询门禁。

## P2：R005 元数据错误

### 现象与复现

规则页读取 `web/src/constants/ruleMeta.ts`，其中 R005 的 `dynamic` 原为 `false`，界面显示“静态”；但后端 R005 明确读取 `AST.Explain.EstScanRows`，用户指南也说明其依赖 EXPLAIN。

### 根因

前端静态元数据没有随 R005 的 EXPLAIN 依赖更新，用户指南表格用“否（但实际依赖 EXPLAIN）”记录了同一矛盾。

### 修复与验收

- 将 R005 的 `dynamic` 改为 `true`，并把用户指南表格改为“是”。
- 新增 `web/src/constants/ruleMeta.test.ts`，同时锁定 R004、R005 为动态规则。
- 修复前同一源码断言失败：`R005 is not classified as dynamic`；修复后通过：`R005 dynamic metadata PASS`。
- 当前宿主没有 Node，现有 `vitest.cmd` 返回 `node is not recognized`；安全审查不允许下载新 Node 镜像，因此 Vitest 本机执行标为环境未满足，不虚报 PASS。CI/具备 Node 的环境应运行 `npm test -- src/constants/ruleMeta.test.ts`。

该修复只改展示元数据与文档，不改变运行时规则、授权、脱敏、审计、查询门禁或数据库 dialect。

## 环境与待实测项

- `rg`、宿主 Go、宿主 Node 均不在 PATH；使用 PowerShell 完成只读扫描，Go 验证使用已有 `golang:1.25-bookworm` 容器。
- Testcontainers/Ryuk 的原生 Windows Docker 生命周期仍需具备原生 CGO 与可访问 Docker named pipe 的环境；不通过禁用安全组件或改生产代码处理。
- parser P99 需在固定 CPU、无并行负载的 Linux 发布 runner 复跑并采集 CPU/alloc profile。
- 豆包 Streamable HTTP 与 Claude Desktop stdio 仍需真实账号/客户端验证；本地 SDK 测试不能替代真实客户端结论。
- Windows Docker Desktop 持久化文件系统上的 SQLite/WAL fsync 抖动只作为环境事实记录；生产持久化不得改用 tmpfs 规避。

## 自检证据

- `go build ./...`：在已有 `golang:1.25-bookworm` 容器中通过，退出码 0。
- `go test -short ./internal/pipeline ./internal/rules -count=1`：通过，分别为 0.584 秒和 0.050 秒。
- `go test -short ./internal/pipeline -run '^TestProcessDemoAddsDynamicR005WithDatasourceRowLimitOnly$' -count=1 -v`：通过；该既有用例同时复现 Demo 命中 R005、production 不命中的当前差异。
- R005 元数据源码断言：修复前失败并输出 `R005 is not classified as dynamic`；修复后源码与用户指南一致性检查通过。
- 改动文件尾随空白、冲突标记、末尾换行检查：通过。任务红线禁止任何 Git 操作，因此没有运行 `git diff --check`，使用上述精确文件检查替代，不能将其表述为执行过 Git 检查。
- 产品 Go 源码的全仓 `gofmt -l` 扫描输出了未由本批触碰的既有文件 `internal/b5wal/key_registry.go`。本批没有 Go 文件改动；按“仅 #36、禁止顺手重构/格式噪音”约束未修改该文件，因此全仓 gofmt 门禁不能标为通过。
- 新增 Vitest 用例已落盘，但宿主没有 Node，`vitest.cmd` 实测返回 `node is not recognized`；未虚报测试通过。

## 本批改动文件

- `web/src/constants/ruleMeta.ts`
- `web/src/constants/ruleMeta.test.ts`
- `docs/USER_GUIDE.md`
- `docs/known-issues-fixes.md`
