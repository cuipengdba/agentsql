# v0.5 已知问题修复记录（批十四 / #36）

**语言 / Language:** [简体中文](known-issues-fixes.md) | [English](en/known-issues-fixes.md)

## 批八十六（WorkBuddy 接管自验）：问题复核结论

来源：WorkBuddy 在“不动代码、不破坏环境”前提下对 `feature/v0.4` 做端到端自验
（原始清单 `codex-task-batch86-issues.md`，仓库外）。豆包逐条独立复核，结论如下。
复核日期：2026-10-11。以下为只读核验，本批未改任何代码，未执行修复。

### 优先级清单（复核后）

| 编号 | 原始定级 | 复核结论 | 定级调整 | 处置 |
| --- | --- | --- | --- | --- |
| P0-1 | P0 | 隐患原理成立，但**当前门禁未复现** | 降为 P2 加固建议 | 统一脚本退出码收口规范 |
| P0-2 | P0 | **确认**：本地两个测试假失败（假失败，非假绿） | 维持 P0 | 方案 B（SkipDir 补 `dist`）为根治，可叠加方案 A（清理 `dist`） |
| P0-3 | P0 | **确认**：仅在只读 `:ro` 挂载下失败 | 降为 P1 健壮性 | 临时目录改系统临时位置并保证模块解析 |
| P1-1 | P1 | **确认**：gofmt 历史遗留 | 维持 P1 | 末尾多余空行，下次改动顺手修 |
| P1-2 | P1 | **确认**：runner 硬编码失效路径 | 维持 P1 | 改为动态探测、取最新版本 |
| P2-1 | P2 | **确认**：环境问题 | 维持 P2 | 仅记录，不需处理 |
| P2-2 | P2 | **确认**：环境事实 | 维持 P2 | 真库可达性须加同网段负例对照 |

### P0-1：门禁“假绿”隐患——原理成立，当前脚本未复现

- 原理：`go test` 经管道 `| grep/tail` 时，管道末尾的退出码来自最后一个命令，
  会吞掉 `go test` 的非零退出码——这是真实风险，值得统一加固。
- 但权威门禁脚本 `dist/release-gate-b77/run-gate.ps1` 未出现该问题：
  第 36 行 `& docker ... 2>&1 | ForEach-Object {...} | Tee-Object` 后，
  第 37 行 `$result = $LASTEXITCODE`、第 39 行 `exit $result`；
  `ForEach-Object`/`Tee-Object` 是 cmdlet，不修改 `$LASTEXITCODE`，
  故收口的是 `docker`（即容器内 `go test`）的真实退出码。
- `scripts/check-capability-imports.ps1` 以 `$ErrorActionPreference='Stop'`
  并在 `go list` 后显式检查 `$LASTEXITCODE -ne 0`，违例即 `throw`。
- 源码 `scripts/` 下没有任何 `go test` 调用，也不存在 WorkBuddy 提到的
  `b43-verify.sh`（在 `scripts/tests/deploy/.github` 等目录均未找到）。
- 结论：当前不存在“测试 FAIL 却报绿”的实际缺陷；作为通用加固项保留，
  建议后续所有门禁显式记录每步退出码、以累积变量收口。

### P0-2：`dist/` 物理目录污染导致两个测试假失败——确认

- 仓库根 `dist/` 实测 9.43 GB，虽被 `.gitignore:15` 忽略，但物理存在。
- 内含 5 个历史产物 `.go` 副本（`dist/release-b81/validation-source/scripts/...`）。
  其中 `highgo/pgx-probe/main.go` 导入 `jackc/pgx/v5` 与 `pgconn`，
  `yashan-native-probe.go` 导入 `database/sql`，均命中违禁清单；
  它们的白名单路径是 `scripts/highgo/pgx-probe/main.go`、
  `scripts/yashan-native-probe.go`，而 `dist` 副本路径不匹配，被判越权。
- `TestBusinessDriverImportsStayInsideCapabilityDomain`
  与 `TestSQLSinkCallsitesMatchManifest` 均以 `filepath.WalkDir(root)` 遍历物理目录；
  `capability_test.go` 的 SkipDir 名单（第 38、178 行）含
  `.git/.gocache/.design/go-build*/capabilitycompile*`，**缺 `dist`**。
- 这是“假失败”而非代码缺陷，且在 batch81（20:30 后）才产生；
  发布当时（10-09 12:39）的门禁绿是在该污染出现之前取得，不影响已发布产物。
- 修复方案（改已提交测试文件，**待用户拍板**）：
  - 方案 B（推荐、根治）：在两处 SkipDir 名单补 `entry.Name() == "dist"`；
  - 方案 A（可选、回收空间）：清理 `dist/` 9.43 GB。
  建议 A、B 都做：B 防止复发，A 回收磁盘。

### P0-3：只读挂载下编译器测试假失败——确认（仅 `:ro`）

- `TestCompilerRejectsRawBusinessDatabaseCapabilities` 第 138 行
  `os.MkdirTemp(root, "capabilitycompile")` 在源码根内建临时目录；
  仓库以 `:ro` 挂载时会报 `read-only file system`。
- 实际门禁 `run-gate.ps1` 的挂载是 `${root}:/src`（可读写，无 `:ro`），
  故正常发布流程不受影响；该项属健壮性问题。
- 修复方向（改已提交测试，**待用户拍板**）：临时目录改用
  `os.MkdirTemp("", "capabilitycompile")`；因被测用例需导入本仓
  `internal/...` 包，须同时保证模块解析（临时目录加 `go.mod` 并以
  `replace` 指向仓库根，或等价的 `go.work`），不能只改一行。

### P1-1：gofmt 历史遗留——确认

- `gofmt -l internal/b5wal/key_registry.go` 命中；`gofmt -d` 显示仅
  文件末尾多一个空行（`}\n\n` → `}\n`）。该文件已提交、非本次改动引入，
  下次顺手修正即可。

### P1-2：runner 硬编码失效路径（定时炸弹）——确认

- `scripts/codex-batch-runner.ps1` 第 231、400 行写死
  `...\bin\f544b3844e0f14e9\codex.exe`，实测该目录已不存在；
  现靠第 233-236 行递归查找“恰好 1 个 codex.exe”兜底。
  若 Codex 升级后留下多个版本目录，会抛 `Cannot identify unique codex.exe`。
- 修复（改已提交脚本，**待用户拍板**）：枚举 `bin\*\codex.exe` 并按
  版本/时间取最新，去掉写死路径。

### P2-1 / P2-2：环境项——确认，仅记录

- P2-1：备份副本 `.git` 指向原主目录 worktree，切回主工程目录即消失，不需处理。
- P2-2：Docker Desktop（WSL2）对出网流量透明劫持，容器内裸 TCP/`/dev/tcp`
  探测对跨网段地址会假阳性；真库 E2E 的“可达”结论必须用同网段不存在地址做
  负例对照（对照组 CLOSED 时 OPEN 才可信）。

### 建议修复顺序

1. 可直接做、无需取舍的：P1-1（gofmt）、P1-2（runner 动态探测）、P0-2 方案 B
   （SkipDir 补 `dist`）、P0-3（临时目录外移，注意模块解析）——均属改已提交文件，
   建议合并成一批由 Codex 实施、豆包验收后精确提交。
2. 需用户拍板的空间操作：P0-2 方案 A（清理 `dist/` 9.43 GB），与第 1 步独立。
3. P2-1、P2-2 仅作环境记录，不产生代码改动。

## 批三十六：业务元数据租户所有权与边界

控制台业务元数据已完成 tenant 化。迁移对缺少可信归属信息的历史行统一回填
`tenant_default`；这是确定性兼容约定，不是对实际业务归属的推断。控制台从
访问令牌 `tid` 绑定仓储上下文，跨租户列表不可见，详情、更新和删除返回 404；
管理审计 outbox 及落库事件保留同一租户上下文。运行时/平台表保持原结构。

尚未关闭的边界是 MCP 执行数据路径：Agent API Key 认证协议保持不变，Agent
实体能够解析出所属租户，但该租户尚未显式传播到全部 datasource/policy 等
仓储调用。无显式租户的旧内部/MCP 调用当前固定在 `tenant_default` 兼容边界，
因此不得把本批控制台隔离扩大表述为 MCP 数据访问也已全租户化。后续应在
机器身份认证结果与 MCP 请求上下文之间传递 tenant，并补充跨租户执行攻击矩阵。

记录日期：2026-10-04。

## 批三十五：MCP SSE 重放与重启边界

go-sdk v1.7.0 的 `StreamableHTTPHandler.sessions` 是纯内存表，上游仍保留 session retention 的 TODO（#148），且没有把已关闭 `ServerSession` 重新装载回 handler 的接口。因此本批没有实现、也不宣称实现“旧 `Mcp-Session-Id` 跨进程无缝续命”。

本批交付的是两个明确路径：stateful 旧协议使用 SQLite/PostgreSQL 持久化 EventStore 支持 `GET /mcp` + `Last-Event-ID` 断线重放，并在 TTL/容量 purge 造成缺口时以 HTTP 400 fail-closed；进程重启后旧 session 返回 HTTP 404 与 `Mcp-Session-Expired: 1`，客户端据此重新 `initialize`。崩溃遗留事件和 cursor 由后续活动按 TTL 惰性回收，不依赖 `SessionClosed`，也没有后台清理线程。

记录日期：2026-10-02。

> 状态更新（2026-10-04，批三十三）：本文件主体保留批十四当时的调查、复现与处置记录。其 R005 “待修”结论已由批十七提交 `02565f5` 关闭，不再代表当前产品状态；当前能力与发布口径见 `docs/release-notes-v0.5.md` 和 `docs/USER_GUIDE.md`。

## 调研范围与结论

- GitHub 公共 REST 查询 `cuipengdba/agentsql`：仓库 `has_issues=true`，`open_issues_count=0`；`state=open` 与 `state=all` 均返回空数组；直接读取 issue `#36` 返回 404。因此当前公开仓库没有可列出的 open issue，`#36` 按本任务编号记录，不能据此编造远端 issue 内容。
- 对项目自有源码、配置、脚本、测试与 `docs/` 做 `TODO` / `FIXME` / `XXX` 大小写敏感词边界扫描，未发现待处理标记。`docs/v0.5-known-issues-task7.md` 中有一处对既往扫描结果的文字记录，不是新 TODO；`web/node_modules` 中有上游依赖的 FIXME，不属于项目自有代码。
- 在批十四当时的仓库基线上，明确的产品已知问题是 R005 生产动态告警缺失。另有 R005 前端规则元数据错误、parser P99 环境相关波动，以及真实 MCP 客户端和 Testcontainers/Ryuk 待实测项；其中 R005 运行时缺口后来由批十七关闭。
- 本批没有运行任何 Git 命令。

## 优先级清单

| 优先级 | 项目 | 结论 | 理由 |
| --- | --- | --- | --- |
| P0 | R005 在普通生产查询中可能不产生大结果集告警 | **批十四未修；批十七 `02565f5` 已关闭** | 批十四时用户可见的风险告警缺失；后续修复同时覆盖生产动态 EXPLAIN、通用执行截断与 PostgreSQL 列级授权截断，未以部分路径补丁冒充完整修复。 |
| P1 | PostgreSQL parser lineage P99 偶发超过 5 ms | **环境相关，只记录** | 既往 Windows Docker 独占复现仍受调度影响，缺少固定 Linux runner 的 CPU/alloc profile，不能稳定归因。不得放宽门槛掩盖失败。 |
| P2 | R005 在规则页元数据中被标为静态 | **已修复** | R005 的判定读取 `AST.Explain`，前端显示“静态”与实现、用户指南均矛盾，会误导管理员。 |
| 环境 | Testcontainers/Ryuk、Windows Docker 文件系统、宿主缺少 `rg`/Go/Node、真实豆包/Claude 客户端 | **不修，只记录** | 均需要运行环境、工具链或真实客户端/账号，不应据此改生产逻辑。 |

数据库兼容性文档中的“待实测/未实现”是已声明的产品边界，不属于 #36 的可直接修复缺陷。本批未改 Yashan、OpenTenBase、DM 或 Oracle dialect。

## P0：R005 生产动态告警缺失（批十四历史；批十七已关闭）

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

批十七提交 `02565f5` 后，R005 被纳入普通生产动态规则集合；通用执行结果和 PostgreSQL 列级授权持久审计回调还会在 `Truncated=true` 时重新评估 R005。结构化命中进入响应 `Assessment.Hits`，并由 `internal/pipeline/auditmap.go` 序列化到同一审计记录的 `rule_hits`。`internal/pipeline/pipeline_test.go`、`internal/pipeline/r005_column_select_test.go` 与 `internal/rules/generic_test.go` 覆盖估算超阈值、实际截断、未超阈值和显式 LIMIT 仍被实际截断的边界。

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
- `go test -short ./internal/pipeline -run '^TestProcessDemoAddsDynamicR005WithDatasourceRowLimitOnly$' -count=1 -v`：批十四时通过；该用例当时复现 Demo 命中 R005、production 不命中的差异，批十七已将 production 断言更新为同样命中 R005。
- R005 元数据源码断言：修复前失败并输出 `R005 is not classified as dynamic`；修复后源码与用户指南一致性检查通过。
- 改动文件尾随空白、冲突标记、末尾换行检查：通过。任务红线禁止任何 Git 操作，因此没有运行 `git diff --check`，使用上述精确文件检查替代，不能将其表述为执行过 Git 检查。
- 产品 Go 源码的全仓 `gofmt -l` 扫描输出了未由本批触碰的既有文件 `internal/b5wal/key_registry.go`。本批没有 Go 文件改动；按“仅 #36、禁止顺手重构/格式噪音”约束未修改该文件，因此全仓 gofmt 门禁不能标为通过。
- 新增 Vitest 用例已落盘，但宿主没有 Node，`vitest.cmd` 实测返回 `node is not recognized`；未虚报测试通过。

## 本批改动文件

- `web/src/constants/ruleMeta.ts`
- `web/src/constants/ruleMeta.test.ts`
- `docs/USER_GUIDE.md`
- `docs/known-issues-fixes.md`
