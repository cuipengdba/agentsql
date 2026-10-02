# AgentSQL v0.5.0 Release Notes / 发布说明

> 目标发布日 / Target release date: **2026-10-12**
>
> 状态 / Status: **待发布 / Pending release**
>
> 版本 / Version: **v0.5.0**

## 中文

### 范围与口径

v0.5.0 相对已发布的 v0.4.0 增加了国产数据库 dialect 边界、RBAC / 多租户 MVP、MCP 会话兼容修复、英文文档和联合案例记录，并将开源许可证调整为 Apache-2.0。完整变更基线是 `v0.4.0..1d6682d`；任务书指定的 `391dcf0..1d6682d` 只包含许可证与后续 dialect / 案例的 6 个提交，不包含更早的 RBAC、EXPLAIN、MCP 和英文文档提交。

本文中的“协议路径实测通过”仅指定版本、指定镜像/拓扑和合成数据用例，**不等于厂商认证、完整方言兼容、商业版支持或生产可用性承诺**。

### 新特性与变更

- **DM / Oracle / YashanDB 独立 dialect 边界**：新增独立数据源类型与有界连接/元数据路径；Oracle 完成严格只读子集查询和 `PLAN_TABLE` EXPLAIN 归一化实测。DM 代码路径已完成，但当前凭据被实例拒绝；YashanDB 普通查询、EXPLAIN 和写路径仍 fail-closed。
- **OpenTenBase 双内核深化**：OpenTenBase v2.5.0 PostgreSQL 内核对特定远程计划实现严格、版本受限的规范化，指定单机 GTM/CN/DN 拓扑的最小安全闭环有实测记录。TXSQL/MySQL 内核是独立路线，本版未实测、未实现产品专用代码。
- **PolarDB 兼容路径**：指定 PolarDB for PostgreSQL 15 社区镜像通过现有 `postgres` 路径完成 Ping、discovery、R006 拒绝、EXPLAIN、只读查询、脱敏与审计闭环；未新增 PolarDB 别名或厂商识别开关。
- **EXPLAIN 兼容适配**：增加 OpenTenBase、TiDB 和 OceanBase 的严格计划适配与回归 fixture；未知版本、列、节点或计划形态继续 fail-closed。TiDB / OceanBase 本批有代码回归证据，不借此宣称厂商环境完整闭环已通过。
- **RBAC / SSO / 多租户 MVP（#33）**：新增本地用户、租户、角色、权限、多角色和角色继承，并对管理 API 执行逐路由授权。MVP 完全开放、无许可门控；OIDC/LDAP/MFA 与存量业务元数据的全租户化不在本版。
- **MCP 会话修复**：Streamable HTTP 首次 `initialize` 可以不携带 `MCP-Protocol-Version`，后续请求继续严格校验协议版本、会话 ID 和 Agent/Key 绑定。跨进程会话持久化和 SSE 断线重放未实现。
- **企业审计报表与合规导出**：增加 `agentsqlctl audit` 查询/报表能力及配套文档；这不改变“应用层只追加审计不等于法规级 WORM”的信任边界。
- **快速上手、Demo 与错误口径**：新增下载单个 Compose 文件即可启动的自包含五分钟演示栈；演示场景卡对齐真实剧本和审批结果，修复演示表外键/密封 JOIN 冲突与每日 reset 的 seed/gateway 时序，并区分可预期授权失败、对象缺失与数据库执行错误。
- **生态与官网配套**：新增 MCP Registry 描述、官网“生态与兼容”板块、社区入口与国产数据库分批研究/联合验证资料；主站搜索闸门已在仓库中调整为放行主站、保持 `/demo/` 禁止索引，仍需在发布部署后做外网复核。
- **英文文档**：增加英文首页、快速上手、管理、MCP 接入、安全和覆盖度文档。
- **已知问题修复与记录**：修正 R005 前端动态规则元数据；加固 bootstrap / MCP 测试诊断。R005 生产动态告警缺失仍为 P0 待修，未以部分路径补丁冒充完整修复。
- **联合案例配套**：新增金仓 + 瀚高验证记录、运营口径和瀚高演示 SQL。瀚高指定第三方 SEE 镜像有协议路径实测记录；金仓 V9 仍待厂商环境。

### Dialect 与生态适配矩阵

| 数据库 / 路径 | v0.5.0 状态 | 边界 |
| --- | --- | --- |
| PostgreSQL 14–18 | 原生支持 | 既有 PG parser / executor / discovery；B2/B5 仅在 PostgreSQL 路径提供 |
| MySQL 8 | 原生支持 | 表级授权与单请求执行；不声称 B2 列级授权 |
| DM8 | 独立 dialect 最小切片 | 连接/元数据与严格只读代码路径已落地；当前凭据无效，真库 Query 与 EXPLAIN 待验证 |
| Oracle Free 23.26.3 | 独立 dialect 有界实测 | 严格 SELECT 子集、元数据和 EXPLAIN 归一化有记录；完整 parser、写入、列授权/脱敏 pipeline 未实现 |
| YashanDB 23.4.1.109 | 独立 dialect 最小切片 | 连接、当前 schema 和列发现有界实现；通用 Query / EXPLAIN / 写路径 fail-closed |
| OpenTenBase v2.5.0（PG 内核） | 指定环境协议路径闭环通过 | 仅指定单机 GTM/CN/DN 和已知计划形态；非厂商认证 |
| TXSQL/MySQL 内核 | 待官方环境实测 | 未实现产品专用代码；不用普通 MySQL 结果替代 |
| PolarDB for PostgreSQL 15 | 指定社区镜像协议路径闭环通过 | 复用 `postgres`；商业服务拓扑、TLS、故障切换待联合终验 |
| openGauss 7.0.0-RC3 / IvorySQL 5.3 | 指定社区版协议路径闭环通过 | 复用 PG 路径；不替代 GaussDB / HighGo 商业版终验 |
| HighGo SEE 4.5 | 指定第三方镜像协议路径闭环通过 | 非官方镜像、非厂商认证；HighGo 商业版待终验 |
| KingbaseES V9 | 待厂商环境 | 第三方旧镜像被过期 license 阻断；不声称连接/发现/查询通过 |
| TiDB 7.5.1 / OceanBase CE 4.4.2.1 | EXPLAIN 适配与回归已落地 | 本批不把 fixture/代码回归扩大为厂商环境完整认证 |

### 许可变更

自 v0.5 起，开源版依 [Apache License 2.0](../LICENSE) 授权，并提供独立的 [商业许可说明](../COMMERCIAL-LICENSE.md)。Apache-2.0 的权利与义务以 `LICENSE` 和 `NOTICE` 为准；商标许可、企业能力、SLA 与支持条款以双方书面协议为准。该变更由提交 `391dcf0` 引入，不构成任何数据库厂商的授权、认证或背书。

### 已知边界与不承诺事项

- DM 实例凭据未通过，DM 真库业务 SELECT 和 EXPLAIN 不标记为通过。
- KingbaseES V9R1C10 目标环境待厂商提供；HighGo、GaussDB、TDSQL 等商业版仍待目标环境终验。
- P0 R005 生产动态大结果集告警缺失待修；`row_limit` 仍截断返回结果，但不得把截断写成风险告警已完整生效。
- RBAC 是 MVP：非默认租户对存量共享业务元数据仍拒绝；OIDC/LDAP/MFA、令牌撤销/刷新和全量租户化未交付。
- 跨进程 MCP transport session、断线重放与服务重启前会话恢复未提供；豆包、Claude Desktop / Inspector 真实客户端仍待账号和指定版本联调。
- “协议路径实测”不覆盖完整 SQL 方言、生产拓扑、HA/故障切换、TLS/认证矩阵、性能 SLA 或厂商支持责任。

### 版本 bump 文件清单与保留项

下列 42 个文件只调整版本默认值、镜像/tag/包名、客户端自报版本或对应文档示例，不改运行时功能：

- 入口与构建：`.github/ISSUE_TEMPLATE/bug_report.yml`、`CONTRIBUTING.md`、`Dockerfile`、`Makefile`、`README.md`、`README.en.md`、`internal/version/version.go`、`server.json`、`web/package.json`、`web/package-lock.json`。
- 发布脚本：`scripts/build-ghcr-multiarch.ps1`、`scripts/build-release-linux.sh`、`scripts/package-release.sh`。
- Compose 与示例：`docker-compose.yml`、`docker-compose.controlplane.yml`、`docker-compose.demo.yml`、`examples/docker/.env.example`、`examples/docker/demo.env.example`、`demo/reset.ps1`。
- 数据库扩展打包：`dbext/packaging/Dockerfile.packages`、`dbext/packaging/README.md`、`dbext/packaging/build-images.ps1`、`dbext/packaging/build-packages.ps1`、`dbext/packaging/images/Dockerfile`。
- 部署示例：`deploy/ecs-demo/README.md`、`deploy/ecs-demo/docker-compose.yml`、`deploy/quickstart/docker-compose.yml`、`deploy/quickstart/docker-compose.debug.yml`、`deploy/quickstart/gateway/Dockerfile`、`deploy/quickstart/postgres/Dockerfile`、`deploy/quickstart/qs-companion-verify.ps1`、`deploy/quickstart/qs-error-probe.ps1`、`deploy/quickstart/qs-inspect-notes.ps1`。
- 当前版本文档：`docs/DEPLOY.md`、`docs/GETTING_STARTED.md`、`docs/INTEGRATIONS.md`、`docs/SPEC.md`、`docs/USER_GUIDE.md`、`docs/en/GETTING_STARTED.md`、`docs/en/INTEGRATIONS.md`、`docs/en/QUICKSTART.md`、`docs/en/README.md`。

另同步修正 `docs/en/ADMIN.md`、`docs/en/COVERAGE.md`、`docs/en/SECURITY.md` 的“待发布 v0.5.0 / 已发布 v0.4.0”状态表述。历史版本事实、v0.4 行为契约与性能记录保留原文。`website/` 当前仍展示 v0.4.0 GA 并链接三份现存 v0.4.0 PDF；必须等 v0.5.0 Release 与新 PDF 实际就绪后在发布日切换，避免本批制造失效链接。

### v0.5.0 发布资产清单

v0.4.0 的 GitHub Release 已核实包含下列 15 个资产。v0.5.0 沿用同一命名与签名结构；截至本草稿，v0.5.0 Release 尚不存在，因此下表不填写未生成的 SHA-256。

| # | GitHub Release 资产 | 用途 | 状态 |
| ---: | --- | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | amd64 glibc 2.28+ 原生包 | 待构建 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | amd64 外层 SHA-256 | 待构建 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | arm64 glibc 2.28+ 原生包 | 待构建 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | arm64 外层 SHA-256 | 待构建 |
| 5 | `agentsql-v0.5.0.spdx.json` | SPDX SBOM | 待生成 |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | SBOM Ed25519 签名 | 待签名 |
| 7 | `ed25519-release-public-key.json` | 发布公钥 | 待复核/导出 |
| 8 | `go-version-metadata.txt` | Go 工具链与模块元数据 | 待生成 |
| 9 | `install.sh` | 架构感知安装器 | 待从封板提交导出 |
| 10 | `provenance.json` | 产物来源记录 | 待生成 |
| 11 | `provenance.json.sig.json` | provenance Ed25519 签名 | 待签名 |
| 12 | `SBOM-GENERATION.txt` | SBOM 生成说明 | 待生成 |
| 13 | `SHA256SUMS` | 15 资产统一校验和 | 待生成 |
| 14 | `SHA256SUMS.sig.json` | 校验和 Ed25519 签名 | 待签名 |
| 15 | `VERIFYING-SIGNATURES.md` | 签名验证说明 | 待生成 |

| 其他分发项 | 状态 | 发布日验收 |
| --- | --- | --- |
| `ghcr.io/cuipengdba/agentsql:v0.5.0` | 本地非推送双架构构建已通过；推送待发布日 | 本地 OCI/digest 已记录；发布日仍须匿名 pull 并确认 `/healthz` 回报 `v0.5.0` |
| `ghcr.io/cuipengdba/agentsql:latest` | 待发布日移动 | 仅在精确 tag 验收后指向同一 digest |
| `v0.5.0-demo` / `v0.5.0-quickstart` 辅助镜像 | 本地非推送双架构构建已通过 | `scripts/build-ghcr-multiarch.ps1 -IncludeAuxiliaryTags` 已按现有 Dockerfile 分别生成 OCI 归档和 digest JSON，并离线核对 amd64/arm64 descriptor；发布日仍需推送并匿名拉取 |
| `docs/release-notes-v0.5.md` | 已完成（本草稿） | 发布日复核日期、状态和 SHA-256 |
| `CHANGELOG.md` | 已更新为待发布条目 | 发布后才可改成“已发布” |
| 官网三份 v0.5.0 PDF | 待重导 | 按 `website/tools/build-docs-pdf.mjs` 重导并通过 `verify-static.sh`；本批不制造空链接 |

### 相对 v0.4.0 的发布流程

| 类型 | v0.5.0 步骤 |
| --- | --- |
| 复用 | 仓库公开与 PVR 复核；固定 Rocky Linux 8 digest 构建 amd64/arm64；恰好 15 个 Release 资产、SHA-256、SBOM/provenance 与 Ed25519 签名；先建 Release 草稿并回下载验证；GHCR 精确 tag → public → 匿名双架构 pull → 再移动 `latest`；Demo reset；官网 PDF/静态验收/部署；Release 草稿转正式；发布后无登录烟测 |
| v0.5 新增闸门 | 核对 Apache-2.0 / `NOTICE` / 商业许可口径；逐项复核 dialect 矩阵且保留“协议路径实测 ≠ 厂商认证”；构建并演练 demo/quickstart 辅助镜像；确认 P0 R005、DM/金仓凭据和商业版终验未被误标为完成；在标准 runner 清零 parser P99 失败；由维护者确认 `SECURITY.md` 支持版本矩阵 |

### 发布检查清单

| 阶段 | 检查项 | 状态 | 证据 / 发布日动作 |
| --- | --- | --- | --- |
| 准备 | 仓库公开 | **已完成** | GitHub REST 只读查询返回 `private=false` / `visibility=public` |
| 准备 | v0.5.0 Release Notes、资产表、检查表 | **已完成** | 本文；状态仍为待发布 |
| 准备 | 版本常量与 CHANGELOG | **已完成** | 源码/构建/示例默认版本对齐 `v0.5.0`；历史口径保留 |
| 准备 | 完整变更基线复核 | **已完成** | 使用 `v0.4.0..1d6682d`，并记录 `391dcf0` 边界不完整 |
| 安全 | Private Vulnerability Reporting（PVR） | **待发布日** | 匿名 API 不返回该设置；由仓库管理员在 Security settings 复核为开启 |
| 验证 | Go 构建、short tests、gofmt、`git diff --check` | **待发布日** | 批十九已降低 PostgreSQL parser 主体延迟和分配，但同环境独占 5 轮 P99 仍有 3 轮超过 5 ms（5.576/4.906/4.744/5.485/5.643 ms）；不得据此清零闸门，仍须在标准 Linux runner 独占复核并稳定通过后才可发布 |
| 构建 | 固定 Rocky Linux 8 digest 构建 amd64/arm64 | **待发布日** | 两个二进制均回报 `v0.5.0`，GLIBC 符号不高于 2.28 |
| 供应链 | SBOM、provenance、Go metadata、生产 Ed25519 签名 | **dry-run 已补齐；生产签名待发布日** | `scripts/release-dryrun.ps1` 编排双架构包、SBOM、metadata、provenance 和说明；dry-run 只生成显式不可发布的未签名占位文档，绝不读取私钥 |
| 校验 | SHA-256 与签名双重验证 | **本地 SHA-256 闭环已补齐；生产签名待发布日** | dry-run 断言恰好 15 项，回验 tarball sidecar 与统一 `SHA256SUMS` 并输出逐项名称/大小/SHA-256；有效 Ed25519 签名仍须发布日替换占位文档后验证 |
| 草稿 | 建立 GitHub Release draft 并上传恰好 15 资产 | **待发布日** | 本批禁止创建；草稿中先验证名称、大小、SHA-256 和下载 |
| 镜像 | 推送 `v0.5.0`，设 GHCR public，匿名双架构 pull | **本地非推送命令已补齐；发布闸门待发布日** | dry-run 生成主镜像 OCI 与结构化 digest 记录；发布日先验精确 tag，后更新 `latest` |
| Demo | 生成辅助镜像并执行 reset | **辅助 tag 本地命令已补齐；联调待发布日** | `-IncludeAuxiliaryTags` 分别构建 `v0.5.0-demo` / `v0.5.0-quickstart` 并记录 digest；reset、推送和匿名拉取仍待发布日 |
| 官网 | 重导 PDF、运行静态验收、部署 | **待发布日** | 先验 Release/镜像/Demo，再部署；复核 HTTPS、`www` 301、sitemap 和 security.txt |
| 搜索 | 放行主站、保持 `/demo/` noindex | **待发布日** | 主页 `index,follow`；`robots.txt` 为 `Allow: /` + `Disallow: /demo/`；demo meta 仍 `noindex,nofollow` |
| 发布 | Release draft → 正式 | **待发布日** | 只在上述闸门全绿后发布；本批禁止执行 |
| 发布后 | `releases/latest`、安装器、匿名镜像、官网、Demo 回归 | **待发布日** | 从无登录干净环境走完下载→校验→安装→健康/就绪→允许/拒绝/审批 |

### 发布链待办

1. **本批已补齐本地 dry-run。** `scripts/release-dryrun.ps1` 一条命令复用 v0.4.0 双架构构建/打包与 SBOM 路径，在 `dist/release-dryrun/v0.5.0/assets/` 组装并校验恰好 15 项，生成统一 `SHA256SUMS` 并输出逐项名称、大小和 SHA-256；`evidence/` 单独保存 OCI/digest 证据，不混入 Release 资产。仓库仍不新增未经演练的 Release CI。dry-run 的公钥与三份签名文件是显式 `dryRun=true`、`signed=false` 的不可发布占位文档；生产签名和验签仍是不可跳过的发布日人工闸门。
2. **本批已补齐并实跑辅助 tag 非推送命令。** `scripts/build-ghcr-multiarch.ps1 -IncludeAuxiliaryTags` 保持主镜像精确 tag 与显式 `-Push` 才移动 `latest` 的既有规则，同时分别为 `v0.5.0-demo` / `v0.5.0-quickstart` 生成双架构 OCI 归档和结构化 digest JSON。本地离线检查确认三个归档均包含 amd64/arm64 descriptor；发布日仍须由主控执行推送、GHCR public 和匿名双架构拉取验收。
3. PVR 设置、GHCR public、官网实时状态和搜索放行需管理员/外网证据；当前无法从匿名 API 完整取证的项均保持“待发布日”。
4. `SECURITY.md` 的受支持版本矩阵仍列 0.3.x，与待发布的 v0.5.0 不一致。该项涉及安全修复承诺，必须由维护者在发布日前确认并更新；本批不代替维护者猜测支持周期。

### PostgreSQL parser P99 调查（批十九）

- 口径：`TestParseProjectionLineageP99Budget/postgres` 测量完整 `Parser.Parse`，典型 SQL 为 8 个 `UNION ALL` 分支、每分支 8 列，每轮 2,000 样本；包含 `pg_query`、JSON AST 解码、Normalize 前扫描和 lineage 后处理，不包含闭包 DML 执行链路。
- 环境：`golang:1.25-bookworm`、Linux/amd64、Intel i7-6700、8 个逻辑 CPU。未优化独占 3 轮 P99 为 7.329/8.938/6.518 ms；补采的一轮 P50/P90/P99 为 2.554/4.138/5.671 ms。
- 根因：典型 SQL 无字面量，实际不会执行 `pg_query.Normalize`；优化前却每次执行拆句、ParseToJSON、Normalize 前扫描和注释扫描共 4 次 `pg_query` 前端调用。CPU profile 中两次 Scan 合计约 20.1%，两段 JSON 解码约 30.2%，lineage 约 10.3%，CGO 平坦耗时约 18.5%，没有独立类型推断阶段。
- 最小优化：无分号单语句跳过冗余拆句扫描；Normalize 与注释检测复用一次 token scan；JSON AST 从两段解码合并为一次并拒绝 NULL 根；表、CTE、列、函数收集合并为一次只读遍历。现有 `set_arms_8`（8 分支、每分支 1 列）benchmark 从 720,686–756,478 ns/op、110,278–110,279 B/op、1,255 allocs/op 改善到 495,068–544,081 ns/op、86,421 B/op、1,139 allocs/op。
- 结论：最终 5 轮 P50 为 1.702–1.861 ms、P90 为 3.146–3.289 ms，但 P99 为 5.576/4.906/4.744/5.485/5.643 ms，仅 2/5 通过。主体延迟约降低 27%–33%，尾部仍受 C parser/scan、JSON 大量分配及 GC/调度共同影响；保留 5 ms 发布日闸门，不放宽、不标记达标。
- 全仓并行 `go test -short ./... -count=1` 的功能包均通过，但性能门因 CPU 竞争失败（MySQL/PostgreSQL P99 为 19.518/12.546 ms），因此命令总体为 FAIL；发布判定仍只采用无并行负载的独占复跑，同时不能把这次全仓结果记为通过。

## English

### Scope and evidence language

AgentSQL v0.5.0 adds bounded database-dialect work, an RBAC/multi-tenant MVP, MCP session compatibility fixes, English documentation, joint-case material, and an Apache-2.0 licensing change. The complete comparison base is `v0.4.0..1d6682d`. The narrower `391dcf0..1d6682d` range contains only six commits and would omit the earlier RBAC, EXPLAIN, MCP, and English-documentation commits.

"Protocol-path tested" means only the named version, image/topology, and synthetic test cases. It is **not vendor certification, complete SQL-dialect compatibility, commercial-edition support, or a production-readiness commitment**.

### Highlights

- Independent, fail-closed dialect boundaries for DM, Oracle, and YashanDB. Oracle has evidence for the strict read-only subset and normalized `PLAN_TABLE` EXPLAIN. DM live query/EXPLAIN remains pending valid credentials; YashanDB general query, EXPLAIN, and writes remain disabled.
- Strict, version-gated OpenTenBase v2.5.0 PostgreSQL-plan normalization and a scoped single-node GTM/CN/DN safety-loop record. TXSQL/MySQL is a separate, untested path with no product-specific implementation in this release.
- A scoped PolarDB for PostgreSQL 15 community-image exercise through the existing `postgres` path, with no PolarDB alias or vendor-identification switch.
- Fail-closed EXPLAIN adapters and regression fixtures for OpenTenBase, TiDB, and OceanBase. Fixture coverage is not represented as full vendor-environment certification.
- An openly available RBAC/multi-tenant MVP: local users, tenants, roles, permissions, multiple roles, inheritance, and per-route authorization. OIDC/LDAP/MFA and full tenant ownership for legacy business metadata are deferred.
- MCP Streamable HTTP initialization now accepts the standards-compliant first request without a pre-sent protocol-version header; subsequent protocol, session, and Agent/Key checks remain strict.
- Audit query/reporting work, the self-contained five-minute quickstart stack, hardened demo reset sequencing, clearer authorization/database-error reporting, MCP Registry metadata, and ecosystem/website documentation are included in the `v0.4.0..1d6682d` change set.
- A new English documentation suite, R005 metadata/test-diagnostic fixes, and the KingbaseES + HighGo joint-case evidence and wording package.
- Starting with v0.5, the open-source edition uses [Apache License 2.0](../LICENSE) plus a separate [commercial-license notice](../COMMERCIAL-LICENSE.md). This does not imply database-vendor authorization or endorsement.

### Known boundaries

- DM live validation is blocked by invalid instance credentials. KingbaseES V9 and the named commercial database editions remain pending vendor-provided target environments.
- The P0 R005 production dynamic large-result warning gap remains open. `row_limit` still bounds returned rows, but that is not a substitute for the missing risk hit.
- RBAC is an MVP. OIDC/LDAP/MFA, server-side token revocation/refresh, and complete tenant ownership migration are not included.
- MCP transport sessions are not persisted across processes or restarts, and SSE replay is not implemented. Real-account tests for Doubao and specific Claude Desktop/Inspector versions remain pending.
- Protocol-path results do not cover full dialects, production topologies, HA/failover, the complete TLS/authentication matrix, performance SLAs, or vendor support obligations.

### Release assets and release-day gates

The v0.5.0 GitHub Release must contain exactly the same 15 named asset classes listed in the Chinese asset table, with `v0.5.0` substituted in versioned filenames. `scripts/release-dryrun.ps1` now assembles and checksum-verifies that 15-file topology locally, while deliberately emitting unmistakable unsigned, non-releasable placeholders for the public key and three signatures. `scripts/build-ghcr-multiarch.ps1 -IncludeAuxiliaryTags` adds non-pushing, digest-recorded builds for the demo and quickstart tags. Production signing and verification, GHCR push/public/anonymous pull, GitHub Release creation, website PDFs/deployment, demo reset, search-index gate, PVR verification, draft-to-final transition, tag creation, and post-release anonymous smoke tests remain release-day actions. No publication, tag, merge, or push is performed by this preparation batch.
