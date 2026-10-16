# AgentSQL 发布流程与 tag 含义

## v0.9.0 发布执行顺序

1. 完成[发布说明](release-notes-v0.9.0.md)、[验收记录](v0.9-acceptance.md)、官网和三份 v0.9.0 PDF；从同一个干净封板提交运行全仓测试、真实 MCP/数据库回归与同提交 CI。
2. 从该提交运行双架构 dry-run、`-ProductionPrepare`、Ed25519 三签名及 15 项资产的来源/哈希校验；使用 v0.8.0 官方签名包演练 SQLite 合并库和 PostgreSQL metadata/audit 分库的升级及成套备份恢复。
3. 推送 annotated `v0.9.0` tag，创建 GitHub Release draft，上传并匿名回下载全部资产验签。从同一 tag 推送 GHCR 主镜像、demo 与 quickstart 的精确版本标签，核对 public、amd64/arm64 manifest、匿名拉取、OCI revision 与运行时健康。
4. 公开 GitHub Release，成套备份并升级独立 Live Demo 后端到同一精确 demo 镜像；以公网 `/healthz`、`/readyz`、真实演示查询和审计验证版本与链路一致。Demo 验收通过后再部署官网 v0.9.0 页面与 PDF；若升级失败，恢复旧后端及数据快照，不把旧后端展示为 v0.9.0。
5. 上述入口通过后按已验收 digest 单独提升 GHCR `latest`，再次匿名拉取。记录最终提交、CI、15 项签名资产、镜像摘要、官网与 Demo 外网烟测。

任何签名、升级恢复、匿名拉取或 Demo 实测失败都阻止宣布发布完成；按失败域修复并从新封板提交重新验收。

## v0.8.0 发布执行顺序

v0.8.0 的功能与隔离验收见 [发布说明](release-notes-v0.8.0.md)和[验收记录](v0.8-acceptance.md)。发行脚本的资产来源、签名和公钥门槛沿用下方已验证流程，但每一项必须从 v0.8.0 最终封板提交重新构建和验收，不能复用 v0.7.0 产物。

1. 完成候选源码、官网、三份 v0.8.0 PDF 和文档封板；同提交 CI 全绿，干净工作树中运行完整双架构 dry-run 与 `-ProductionPrepare`。
2. 使用受信公钥与仓库外正式私钥按 SPDX、provenance、SHA256SUMS 的顺序签名，三次密码学验签通过；`-ValidateOnly` 核对 15 项资产、提交、内容和构建来源。
3. 本地 annotated tag 指向封板提交，以 v0.7.0 和 v0.8.0 正式包复核 SQLite 合并库及 PostgreSQL metadata/audit 分库的升级与成套备份恢复。通过后推送 tag，创建 GitHub Release 草稿并上传 15 项资产，再公开回下载验签。
4. 从同一提交推送 GHCR `v0.8.0`、`v0.8.0-demo`、`v0.8.0-quickstart` 精确标签；核对 public、amd64/arm64 manifest、匿名拉取、OCI revision 与运行健康。然后正式发布 GitHub Release，部署官网 v0.8.0 页面及 PDF，并做外网烟测。
5. 上述入口全部通过后，以已验收主镜像 digest 单独提升 `latest`，再次匿名拉取。记录发布后烟测、源提交、资产摘要、签名、公网镜像 digest 和官网版本。

任一候选漂移、签名失败、资产缺失、构建失败或匿名拉取不通过时停止发布并修复；官网与 `latest` 不提前切换。

下方保留 v0.7.0 的发布执行顺序和 v0.5.0 筹备阶段的历史记录。历史日期、产物摘要与命令不能作为 v0.8.0 的验收结果。

## v0.7.0 发布执行顺序

Dify 与 MCP Inspector 的四次兼容入口 GUI 查询已有隔离验收证据；功能与回滚边界见[发布说明](release-notes-v0.7.0.md)和[验收记录](v0.7-acceptance.md)，15 项资产名称见[发布资产清单](RELEASE_ASSETS.md)。[v0.6.1 正式资产](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1)、v0.7.0 封板提交及同提交 CI、最终回退演练和 tag 后的资产、GHCR、官网结果均须写入 [v0.7.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.7.0) 正文及发布证据；本地未提交代码或旧 dry-run 的 PASS 不能替代。

1. 核对代码、真库与真实 MCP 客户端验收，整理版本说明。SQLite 合并库从升级前数据库和配对 `AGENTSQL_SECRET` 恢复后可回已发布的 v0.6.0。PostgreSQL metadata/audit 分库须以 v0.6.1 为旧版目标；发行前取得其正式资产，并使用本版已签名资产完成隔离双库升级回退复测。具体生产部署仍须在目标环境核验成套备份和恢复。原 v0.6.0 分库服务不能作为可用回退目标；旧版不能直接读取 v0.7.0 新 schema。
2. 将所有 v0.7.0 源码、迁移、内嵌控制台产物和文档纳入最终封板提交。推荐从该提交建立全新工作树，构建前后核对 `git rev-parse HEAD` 和 `git status --porcelain --untracked-files=all`，要求 HEAD 不变、状态无输出。`-ProductionPrepare` 会在开始时拒绝已跟踪和未跟踪的非忽略文件，并在包、SBOM 和镜像构建后再次检查工作树、HEAD 与关键输入哈希。
3. 从封板提交运行完整双架构 dry-run，要求 15 项本地资产和三个 OCI 归档通过；其中公钥和签名是占位文件，不能发布。然后以受控的正式公钥运行 `-ProductionPrepare` 生成 11 个待签名文件；此模式禁止与 `-SkipImages` 同用。签名前使用共用来源校验核对正式 provenance 状态、8 个 subjects 的大小与 SHA-256，以及 11 个构建输入相对封板 Git 对象的 SHA-256。对 SPDX SBOM、`provenance.json` 签名，生成覆盖其余 13 项的 `SHA256SUMS`，最后签该清单。分别运行三次 `scripts/releasesign/main.go -mode verify`，再以 `-ValidateOnly` 校验恰好 15 项并复核来源。`SIGNED_STRUCTURE_ONLY` 不代表密码学验签。
4. 确认 `provenance.json` 的提交与封板提交一致、`dirtyAtBuild=false`，并核对 tar 内二进制版本、ELF/GLIBC、YashanDB 客户端、包内清单与 SBOM。先创建指向同一提交的本地 annotated tag，以 v0.6.0/v0.6.1 正式包和本版已签名包完成 SQLite 与 PostgreSQL 分库的升级回退复测；通过后再推送 tag、创建 GitHub Release draft，上传 15 项资产。回下载到新目录重算摘要、验签与复核，暂时保留 draft。
5. 从同一封板提交的干净工作树构建并推送 GHCR `v0.7.0`、`v0.7.0-demo`、`v0.7.0-quickstart` 精确 tag。推送脚本强制校验本地与 GitHub annotated tag 均指向该提交。记录构建推送 digest，并与公开 manifest digest 逐项比对；确认三个 tag 均公开，可在未登录的 amd64/arm64 环境拉取，六个镜像配置的 OCI revision 均等于封板 tag SHA，且完成健康及适用业务链路验收。准备 v0.7.0 官网内容和 PDF，在预发布环境验证静态资源；上述闸门通过后将 GitHub Release draft 转正式、部署官网，并执行发布后烟测。记录原 `latest` digest，待正式 Release 与官网烟测通过后，使用已验收的主镜像顶层 digest 作为 `-AcceptedMainDigest` 单独提升 `latest`，核对结果；失败时仅对仍属于本次操作的值尝试恢复旧 digest。

本地命令示例（输出目录须是全新路径；正式公钥路径由发布负责人在仓库外提供）：

```powershell
git rev-parse HEAD
git status --porcelain --untracked-files=all
pwsh ./scripts/release-dryrun.ps1 -Version v0.7.0 -OutputDirectory dist/release-dryrun/v0.7.0-final
$trustedReleasePublicKey = '<独立核验的发布公钥绝对路径>'
pwsh ./scripts/release-dryrun.ps1 -Version v0.7.0 -ProductionPrepare -PublicKeyPath $trustedReleasePublicKey -OutputDirectory dist/release-prepare/v0.7.0-final
```

上述命令不会自动签名、创建 tag、发布 GHCR 或部署官网。具体部署与回滚操作见 [部署指南](DEPLOY.md)。

## v0.5.0 筹备阶段的历史记录

以下文字保留当时的发布工程约束和演练证据；其中“本地 dry-run”“未发布”等状态仅指当时的记录。

### 版本与资产

GitHub Release 的版本 tag 是 `v0.5.0`。其 15 个上传文件、来源、校验规则和当时的演练状态见 [RELEASE_ASSETS.md](RELEASE_ASSETS.md)。`v0.4.0` 的 [公开 Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.4.0) 是资产命名基线，不能把旧 tar 或旧 SHA-256 改名复用。`docs/release-notes-v0.5.md`、`CHANGELOG.md` 与 `docs/release-day-checklist-v0.5.md` 是该版发布文案和执行清单。

当时仓库没有 `.github/workflows/` 发布工作流。构建与发布入口分布在 `Makefile`、`scripts/` 和发布日清单中；不能将本地 dry-run 的 PASS 解释为 GitHub Actions 或正式发布闸门已通过。当前已有 CI 工作流，但仍没有可把本地演练直接变成正式发布的自动步骤。

v0.5.0 当时记录的目标发布闸门为 **2026-10-16 16:00 CST（北京时间，UTC+08:00）**。这是历史计划时间，不是 v0.7.0 已封板或通过闸门的证明。

### 本地 dry-run

在仓库根目录运行 `powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist\release-dryrun\v0.5.0-final`。脚本固定 Rocky 8 与 Syft 镜像 digest，构建 amd64/arm64 包、SBOM、provenance 和三个本地 OCI 归档；需要可用的 Docker daemon、Buildx、双架构运行能力及构建依赖。输出目录已存在时会拒绝覆盖。dry-run 的公钥和三份签名文件均明确标为 `dryRun=true`、`signed=false`、`releasable=false`。

若只核对已有目录，运行 `powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory <目录>`。这个模式只读资产和 git HEAD，不构建镜像。它显示 15 行库存和 SHA-256，并对名称集合、sidecar、13 项 manifest、Yashan 内容、provenance commit 和签名文档执行 fail-closed 校验。正式签名还必须单独使用 `scripts/releasesign/main.go -mode verify` 验证三个原始文件；验证器的 `SIGNED_STRUCTURE_ONLY` 不是密码学验签成功。

### 两个辅助 GHCR tag

`ghcr.io/cuipengdba/agentsql:v0.5.0-demo` 与 `ghcr.io/cuipengdba/agentsql:v0.5.0-quickstart` **都是容器镜像 tag，不是 git tag**。`v0.5.0-demo` 的 Bake target 以主仓库 `Dockerfile` 和 `VERSION=v0.5.0` 构建 demo base；`v0.5.0-quickstart` 的 target 以同一图中的 demo base 为基础，额外放入 `/etc/agentsql/config.demo.yaml` 与 `/etc/agentsql/demo-seed.yaml`。两者都应有 `linux/amd64` 和 `linux/arm64`，且 quickstart 的内容层应与 demo 不同。

`scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -IncludeAuxiliaryTags` 默认只生成本地 OCI 归档和 digest JSON；只有显式 `-Push` 才会写入 GHCR。`-Push` 只处理精确版本及辅助 tag，不移动 `latest`；`-PromoteLatest` 是发布日精确 tag 经公开、匿名双架构验收后的单独操作。dry-run 输出中的 OCI digest 是**本地归档证据**，不表示 GHCR 已有这些 tag、已公开或可匿名拉取。

2026-10-05 的完整本地演练在 `dist/release-dryrun/v0.5.0-20261005b/` 退出码为 0，三个 OCI 归档均通过 `linux/amd64` 与 `linux/arm64` descriptor 检查。`ghcr-agentsql-v0.5.0-image-digests.json` 的 `pushed=false`，记录的三个本地 digest 分别为：主镜像 `sha256:33f98bcbaf66dddc2a055a007931f2ecd25f41d284660663b3ec31bd957509d4`、demo `sha256:8bb2e984d4cdac125f32d01eef0adf609a6f79d0de74ee7f89172e99a97aea96`、quickstart `sha256:a2b81a0ce48d1b22ef7cab033884587522a90ad7322016f91b1452032fd34c2e`。这是本地未签名演练，不是 GHCR 远端状态。

### v0.5.0 当时的正式发布闸门

先在最终封板提交的干净工作树重建，核对 15 个正式文件，完成三份 Ed25519 签名的密码学验证，再按 [发布日清单](release-day-checklist-v0.5.md) 执行 tag、GitHub Release draft、资产回下载、GHCR 精确 tag 的公开与匿名双架构验收、`latest` 提升和最终发布。任一资产缺失、哈希不符、签名失败、架构缺失、provenance 指错提交或发布闸门未通过，均停止；不得以旧 dry-run 文件替代。
