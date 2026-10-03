# AgentSQL v0.5.0 Release Notes / 发布说明

> 发布闸门 / Release gate: **2026-10-12 16:00 CST**
>
> 状态 / Status: **待发布 / Pending release**
>
> 版本 / Version: **v0.5.0**

## 中文

### 范围与口径

v0.5.0 相对已发布的 v0.4.0 增加了国产数据库 dialect 边界、RBAC / 多租户 MVP、MCP 会话兼容修复、英文文档和联合案例记录，并将开源许可证调整为 Apache-2.0。完整变更基线是 `v0.4.0..1d6682d`；任务书指定的 `391dcf0..1d6682d` 只包含许可证与后续 dialect / 案例的 6 个提交，不包含更早的 RBAC、EXPLAIN、MCP 和英文文档提交。

本文中的“协议路径实测通过”仅指定版本、指定镜像/拓扑和合成数据用例，**不等于厂商认证、完整方言兼容、商业版支持或生产可用性承诺**。

### 新特性与变更

- **DM / Oracle / YashanDB 独立 dialect 边界**：新增独立数据源类型与有界连接/元数据路径；DM8 已在 `COMPATIBLE_MODE=0` 的 Pack3 实例完成窄查询、分页和已知 EXPLAIN 形态实测，Oracle 完成严格只读子集查询和 `PLAN_TABLE` EXPLAIN 归一化实测。DM/Oracle 新增 v0.5 冻结、fail-closed 的受控 SELECT parser：提取物理表、引用列和直接/常量/通配符投影血缘，不改变 executor 放行语义；函数、括号、子查询、CTE、复杂表达式和未列出的结构仍拒绝。YashanDB Go 驱动 v1.4.4 与官方 C 客户端 23.4.7.100 计划随 Linux 发行物分发。崖山客户端再分发依据为用户于 2026-10-03 声明已取得厂商授权，本仓库未收到书面授权文件。批三十提交 `33f2d56` 已修复 pool/physical session 普通 Query 边界并在真实 23.4.1.109 实例复验 fail-closed；EXPLAIN 和写路径仍不宣称可用。批三十一发现旧 dry-run 包未包含新驱动/客户端，必须由封板提交重新构建。
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
| DM8 | 独立 dialect 有界实测 | `COMPATIBLE_MODE=0` Pack3 的窄 SELECT、分页和已知 EXPLAIN 形态有真库证据；v0.5 受控 SELECT 表/列/投影血缘已实现并对未知结构 fail-closed；完整 grammar、写入、列授权/脱敏 pipeline 未实现 |
| Oracle Free 23.26.3 | 独立 dialect 有界实测 | 严格 SELECT 子集、元数据和 EXPLAIN 归一化有记录；v0.5 受控 SELECT 表/列/投影血缘已实现并对未知结构 fail-closed；完整 grammar、写入、列授权/脱敏 pipeline 未实现 |
| YashanDB 23.4.1.109 | 独立 dialect 最小切片 | 连接、当前 schema 和列发现实测可达；提交 `33f2d56` 已在真实实例复验通用 Query fail-closed；EXPLAIN / 写路径仍不宣称可用；发布包双架构重建待发布日 |
| OpenTenBase v2.5.0（PG 内核） | 指定环境协议路径闭环通过 | 仅指定单机 GTM/CN/DN 和已知计划形态；非厂商认证 |
| TXSQL/MySQL 内核 | 待官方环境实测 | 未实现产品专用代码；不用普通 MySQL 结果替代 |
| PolarDB for PostgreSQL 15 | 指定社区镜像协议路径闭环通过 | 复用 `postgres`；商业服务拓扑、TLS、故障切换待联合终验 |
| openGauss 7.0.0-RC3 / IvorySQL 5.3 | 指定社区版协议路径闭环通过 | 复用 PG 路径；不替代 GaussDB / HighGo 商业版终验 |
| HighGo SEE 4.5 | 指定第三方镜像协议路径闭环通过 | 非官方镜像、非厂商认证；HighGo 商业版待终验 |
| KingbaseES V9 | 待厂商环境 | 第三方旧镜像被过期 license 阻断；不声称连接/发现/查询通过 |
| TiDB 7.5.1 / OceanBase CE 4.4.2.1 | EXPLAIN 适配与回归已落地 | 本批不把 fixture/代码回归扩大为厂商环境完整认证 |

### 许可变更

**许可更换声明：**v0.4.0 及更早版本的既有源码、tag 与发布包维持其发布时适用的原许可并可继续按原许可使用；不重打历史 tag、不重发历史资产。自 v0.5.0 起，开源版切换为 [Apache License 2.0](../LICENSE)，并提供独立的 [商业许可说明](../COMMERCIAL-LICENSE.md)。Apache-2.0 的权利与义务以 `LICENSE` 和 `NOTICE` 为准；商标许可、企业能力、SLA 与支持条款以双方书面协议为准。该变更由提交 `391dcf0` 引入，不构成任何数据库厂商的授权、认证或背书。

### 已知边界与不承诺事项

- DM/Oracle parser 只覆盖文档列出的 v0.5 受控 SELECT profile；`*` 仅绑定到物理来源表，parser 无 catalog，不能虚构逐列名称。该能力不等于完整 SQL grammar 或已接入列级授权/脱敏 pipeline。
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
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | amd64 glibc 2.28+ 原生包 | 发布日重建；旧 dry-run 包缺 Yashan runtime，不可用 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | amd64 外层 SHA-256 | 随 #1 重建 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | arm64 glibc 2.28+ 原生包 | 发布日重建；旧 dry-run 包缺 Yashan runtime，不可用 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | arm64 外层 SHA-256 | 随 #3 重建 |
| 5 | `agentsql-v0.5.0.spdx.json` | SPDX SBOM | 发布日从 sealed 双架构二进制生成 |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | SBOM Ed25519 签名 | 待签名 |
| 7 | `ed25519-release-public-key.json` | 发布公钥 | 待复核/导出 |
| 8 | `go-version-metadata.txt` | Go 工具链与模块元数据 | 发布日重建；旧文件缺 `yashandb-go v1.4.4` |
| 9 | `install.sh` | 架构感知安装器 | 待从封板提交导出 |
| 10 | `provenance.json` | 产物来源记录 | `-ProductionPrepare` 命令已补；待发布日生成 |
| 11 | `provenance.json.sig.json` | provenance Ed25519 签名 | 待签名 |
| 12 | `SBOM-GENERATION.txt` | SBOM 生成说明 | `-ProductionPrepare` 命令已补；待发布日生成 |
| 13 | `SHA256SUMS` | 除自身及其签名外 13 项的统一校验和 | 待发布日生成 |
| 14 | `SHA256SUMS.sig.json` | 校验和 Ed25519 签名 | 待签名 |
| 15 | `VERIFYING-SIGNATURES.md` | 签名验证说明 | `-ProductionPrepare` 命令已补；待发布日生成 |

| 其他分发项 | 状态 | 发布日验收 |
| --- | --- | --- |
| `ghcr.io/cuipengdba/agentsql:v0.5.0` | 旧 OCI 有双架构 descriptor，但缺新 Yashan 内容；当前 HEAD 待发布日重建 | 发布日须匿名双架构 pull 并确认 `/healthz` 回报 `v0.5.0` |
| `ghcr.io/cuipengdba/agentsql:latest` | 待发布日移动 | 仅在精确 tag 验收后指向同一 digest |
| `v0.5.0-demo` / `v0.5.0-quickstart` 辅助镜像 | 旧归档证实 quickstart 只是主镜像换 tag；构建图已修复，当前 HEAD 待重建 | Buildx Bake 的 quickstart target 从同图 demo base 叠加两份配置；发布日需核对文件存在、双架构 descriptor、Compose seed/gateway/reset |
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
| 安全 | 支持版本矩阵 | **已完成** | v0.5.x 支持至 v0.6.0 发布后 6 个月且不早于 2027-10-31；v0.4.x 支持至 2027-04-30；0.3.x 及更早 EOL |
| 安全 | Private Vulnerability Reporting（PVR） | **待发布日** | 匿名 API 不返回该设置；由仓库管理员在 Security settings 复核为开启 |
| 验证 | Go 构建、short tests、gofmt、`git diff --check` | **待发布日** | 批十九已降低 PostgreSQL parser 主体延迟和分配，但同环境独占 5 轮 P99 仍有 3 轮超过 5 ms（5.576/4.906/4.744/5.485/5.643 ms）；不得据此清零闸门，仍须在标准 Linux runner 独占复核并稳定通过后才可发布 |
| 构建 | 固定 Rocky Linux 8 digest + Go 1.26.8 构建 amd64/arm64 | **待发布日** | `go.mod` 为 1.26；脚本设 `GOTOOLCHAIN=local` 防止隐式换工具链；两个二进制均须回报 `v0.5.0`，GLIBC 符号不高于 2.28 |
| 构建 | 崖山驱动与 C 客户端双架构打包 | **脚本已落地；当前产物阻塞** | `yashandb-go@v1.4.4` 与客户端 23.4.7.100 的固定版本/SHA 已在脚本；批三十一验证旧 tar/metadata 不含它们，必须由 sealed commit 双架构重建 |
| 供应链 | SBOM、provenance、Go metadata、生产 Ed25519 签名 | **dry-run/生产准备命令已补；实跑待发布日** | dry-run 生成不可发布占位；`-ProductionPrepare` 从干净工作树和正式公钥生成 11 个待签名文件；生产签名仍待发布日 |
| 校验 | SHA-256 与签名双重验证 | **校验逻辑已补；当前 15 项 FAIL** | 旧目录真实在 Yashan metadata 闸门失败；发布日最终 15 项须通过三次密码学验签、sidecar/manifest 和 provenance commit 校验 |
| 草稿 | 建立 GitHub Release draft 并上传恰好 15 资产 | **待发布日** | 本批禁止创建；草稿中先验证名称、大小、SHA-256 和下载 |
| 镜像 | 推送 `v0.5.0`，设 GHCR public，匿名双架构 pull | **顺序缺口已修；构建/发布闸门待发布日** | `-Push` 不再移动 latest；精确 tag 验收后单独 `-PromoteLatest` 并比较 digest |
| Demo | 生成辅助镜像并执行 reset | **quickstart 构建缺口已修；实跑待发布日** | `-IncludeAuxiliaryTags` 用独立 quickstart target 加入 demo 配置；旧同 digest 归档不可作为通过证据 |
| 官网 | 重导 PDF、运行静态验收、部署 | **待发布日** | 先验 Release/镜像/Demo，再部署；复核 HTTPS、`www` 301、sitemap 和 security.txt |
| 搜索 | 放行主站、保持 `/demo/` noindex | **待发布日** | 主页 `index,follow`；`robots.txt` 为 `Allow: /` + `Disallow: /demo/`；demo meta 仍 `noindex,nofollow` |
| 发布 | Release draft → 正式 | **待发布日** | 只在上述闸门全绿后发布；本批禁止执行 |
| 发布后 | `releases/latest`、安装器、匿名镜像、官网、Demo 回归 | **待发布日** | 从无登录干净环境走完下载→校验→安装→健康/就绪→允许/拒绝/审批 |

### 发布链待办

1. **批三十一真实 dry-run 结论为阻塞。** 旧 15 项在 Yashan metadata 闸门 fail-closed，两个 tar 也缺两份客户端库；当前 HEAD 的完整重建需要任务书要求本批跳过的外网。详细命令与输出见 [`release-day-checklist-v0.5.md`](release-day-checklist-v0.5.md)。
2. **辅助镜像缺口已修但未冒充实跑通过。** 旧三个 OCI 的平台 manifest digest 相同，证实旧 quickstart 只是换 tag；新 `quickstart` target 会加入 `config.demo.yaml` / `demo-seed.yaml`。`-Push` 与 `-PromoteLatest` 已拆开，避免精确 tag 验收前移动 latest。
3. **生产候选命令已补。** `-ProductionPrepare -PublicKeyPath` 要求完全干净工作树并输出 11 个非 dry-run 待签名文件；发布日补齐三份签名和 `SHA256SUMS` 后，才可用 `-ValidateOnly` 核对 15 项并逐份密码学验签。
4. PVR 设置、GHCR public、官网实时状态和搜索放行需管理员/外网证据；当前无法从匿名 API 完整取证的项均保持“待发布日”。
5. **支持矩阵已按 2026-10-03 用户定案更新。** v0.5.x 与 v0.4.x 的截止口径已同步到中英文安全文档，0.3.x 及更早标记为 EOL。

### PostgreSQL parser P99 调查（批十九）

- 口径：`TestParseProjectionLineageP99Budget/postgres` 测量完整 `Parser.Parse`，典型 SQL 为 8 个 `UNION ALL` 分支、每分支 8 列，每轮 2,000 样本；包含 `pg_query`、JSON AST 解码、Normalize 前扫描和 lineage 后处理，不包含闭包 DML 执行链路。
- 环境：`golang:1.25-bookworm`、Linux/amd64、Intel i7-6700、8 个逻辑 CPU。未优化独占 3 轮 P99 为 7.329/8.938/6.518 ms；补采的一轮 P50/P90/P99 为 2.554/4.138/5.671 ms。
- 根因：典型 SQL 无字面量，实际不会执行 `pg_query.Normalize`；优化前却每次执行拆句、ParseToJSON、Normalize 前扫描和注释扫描共 4 次 `pg_query` 前端调用。CPU profile 中两次 Scan 合计约 20.1%，两段 JSON 解码约 30.2%，lineage 约 10.3%，CGO 平坦耗时约 18.5%，没有独立类型推断阶段。
- 最小优化：无分号单语句跳过冗余拆句扫描；Normalize 与注释检测复用一次 token scan；JSON AST 从两段解码合并为一次并拒绝 NULL 根；表、CTE、列、函数收集合并为一次只读遍历。现有 `set_arms_8`（8 分支、每分支 1 列）benchmark 从 720,686–756,478 ns/op、110,278–110,279 B/op、1,255 allocs/op 改善到 495,068–544,081 ns/op、86,421 B/op、1,139 allocs/op。
- 结论：最终 5 轮 P50 为 1.702–1.861 ms、P90 为 3.146–3.289 ms，但 P99 为 5.576/4.906/4.744/5.485/5.643 ms，仅 2/5 通过。主体延迟约降低 27%–33%，尾部仍受 C parser/scan、JSON 大量分配及 GC/调度共同影响；保留 5 ms 发布日闸门，不放宽、不标记达标。
- 全仓并行 `go test -short ./... -count=1` 的功能包均通过，但性能门因 CPU 竞争失败（MySQL/PostgreSQL P99 为 19.518/12.546 ms），因此命令总体为 FAIL；发布判定仍只采用无并行负载的独占复跑，同时不能把这次全仓结果记为通过。

发布前只读加固核验（批二十三）已记录于 [`security-scan-v0.5.md`](security-scan-v0.5.md)：前端生产依赖与 demo 功能链路通过，Go 源码可调用漏洞为 0 但有 4 个不可达模块级命中；F-04 的 208 处历史赋值类命中已由用户于 2026-10-03 复核为测试数据、示例或非敏感赋值并按不处置关闭。parser P99 仅 3/5 轮通过 5 ms 闸门，故整体不能标记为全绿。

## English

### Scope and evidence language

AgentSQL v0.5.0 adds bounded database-dialect work, an RBAC/multi-tenant MVP, MCP session compatibility fixes, English documentation, joint-case material, and an Apache-2.0 licensing change. The complete comparison base is `v0.4.0..1d6682d`. The narrower `391dcf0..1d6682d` range contains only six commits and would omit the earlier RBAC, EXPLAIN, MCP, and English-documentation commits.

"Protocol-path tested" means only the named version, image/topology, and synthetic test cases. It is **not vendor certification, complete SQL-dialect compatibility, commercial-edition support, or a production-readiness commitment**.

### Highlights

- Independent dialect boundaries for DM, Oracle, and YashanDB. DM8 has real-instance evidence for the narrow query/pagination path and known EXPLAIN shapes on the documented `COMPATIBLE_MODE=0` Pack3 baseline; Oracle has evidence for the strict read-only subset and normalized `PLAN_TABLE` EXPLAIN. DM/Oracle now have a frozen v0.5, fail-closed controlled-SELECT parser that extracts physical tables, referenced columns, and direct/constant/wildcard projection lineage without changing executor admission behavior. Functions, parentheses, subqueries, CTEs, complex expressions, and unlisted structures remain rejected. Commit `33f2d56` fixed and revalidated the YashanDB pool/physical-session general-Query fail-closed boundary against a real 23.4.1.109 instance; EXPLAIN and writes remain unavailable. The planned Linux artifacts include `yashandb-go@v1.4.4` and the official 23.4.7.100 C client under the user's 2026-10-03 declaration of vendor redistribution authorization, but batch 31 proved that the stale local dry-run artifacts do not contain them and must be rebuilt from the sealed commit.
- Strict, version-gated OpenTenBase v2.5.0 PostgreSQL-plan normalization and a scoped single-node GTM/CN/DN safety-loop record. TXSQL/MySQL is a separate, untested path with no product-specific implementation in this release.
- A scoped PolarDB for PostgreSQL 15 community-image exercise through the existing `postgres` path, with no PolarDB alias or vendor-identification switch.
- Fail-closed EXPLAIN adapters and regression fixtures for OpenTenBase, TiDB, and OceanBase. Fixture coverage is not represented as full vendor-environment certification.
- An openly available RBAC/multi-tenant MVP: local users, tenants, roles, permissions, multiple roles, inheritance, and per-route authorization. OIDC/LDAP/MFA and full tenant ownership for legacy business metadata are deferred.
- MCP Streamable HTTP initialization now accepts the standards-compliant first request without a pre-sent protocol-version header; subsequent protocol, session, and Agent/Key checks remain strict.
- Audit query/reporting work, the self-contained five-minute quickstart stack, hardened demo reset sequencing, clearer authorization/database-error reporting, MCP Registry metadata, and ecosystem/website documentation are included in the `v0.4.0..1d6682d` change set.
- A new English documentation suite, R005 metadata/test-diagnostic fixes, and the KingbaseES + HighGo joint-case evidence and wording package.
- **License change notice:** existing v0.4.0-and-earlier source, tags, and release packages remain available under the license that applied when they were published; historical tags and assets will not be recreated. Starting with v0.5.0, the open-source edition uses [Apache License 2.0](../LICENSE) plus a separate [commercial-license notice](../COMMERCIAL-LICENSE.md). This does not imply database-vendor authorization or endorsement.

### Known boundaries

- The DM/Oracle parser covers only the documented v0.5 controlled-SELECT profile. Wildcards are bound to physical source relations, but the parser has no catalog and does not invent concrete column names. This is not full SQL grammar support or integration with the column-authorization/redaction pipeline. KingbaseES V9 and the named commercial database editions remain pending vendor-provided target environments.
- The P0 R005 production dynamic large-result warning gap remains open. `row_limit` still bounds returned rows, but that is not a substitute for the missing risk hit.
- RBAC is an MVP. OIDC/LDAP/MFA, server-side token revocation/refresh, and complete tenant ownership migration are not included.
- MCP transport sessions are not persisted across processes or restarts, and SSE replay is not implemented. Real-account tests for Doubao and specific Claude Desktop/Inspector versions remain pending.
- Protocol-path results do not cover full dialects, production topologies, HA/failover, the complete TLS/authentication matrix, performance SLAs, or vendor support obligations.

### Release assets and release-day gates

The v0.5.0 GitHub Release must contain exactly the 15 named assets in the Chinese table. Batch 31 did not obtain a current green dry-run: the stale local set fails the new Yashan metadata/library gates, while a sealed-current rebuild requires release-day network access. `scripts/release-dryrun.ps1 -ProductionPrepare` now creates the 11 non-signature production candidates from a clean worktree and authorized public key; the release owner must add two content signatures, `SHA256SUMS`, and its signature, then cryptographically verify all three signatures and run `-ValidateOnly`. The GHCR script now builds a real quickstart target, validates OCI platforms, pushes exact tags without moving `latest`, and exposes a separate post-validation `-PromoteLatest` action. The full ordered checklist is in `release-day-checklist-v0.5.md`. No signing, tag, Release, image push, or publication was performed in batch 31.
