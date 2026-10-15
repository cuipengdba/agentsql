# AgentSQL 发布流程与 tag 含义

本文件先列 v0.7.0 候选的当前发布顺序，后半保留 v0.5.0 筹备阶段的历史记录。历史日期、产物摘要与命令不能作为 v0.7.0 的验收结果。

## v0.7.0 候选发布顺序

v0.7.0 尚未正式发布。功能与回滚边界见 [v0.7.0 候选发布说明](release-notes-v0.7.0.md)，准确的 15 项资产名称见 [发布资产清单](RELEASE_ASSETS.md)。GitHub Actions 的 CI 结果必须对应最终候选提交；本地未提交代码或旧 dry-run 的 PASS 不能替代。

1. 完成代码、真库与真实 MCP 客户端验收，整理版本说明。SQLite 合并库从升级前数据库和配对 `AGENTSQL_SECRET` 恢复后可回已发布的 v0.6.0。PostgreSQL metadata/audit 分库须以已通过隔离回退演练的 v0.6.1 为旧版目标，但必须先正式发布 v0.6.1 资产并完成目标环境验收；原 v0.6.0 分库服务不能作为可用回退目标。旧版不能直接读取 v0.7.0 新 schema。
2. 将所有 v0.7.0 源码、迁移、内嵌控制台产物和文档纳入最终封板提交。推荐从该提交建立全新工作树，构建前后核对 `git rev-parse HEAD` 和 `git status --porcelain --untracked-files=all`，要求 HEAD 不变、状态无输出。`-ProductionPrepare` 会在开始时拒绝已跟踪和未跟踪的非忽略文件，并在包、SBOM 和镜像构建后再次检查工作树、HEAD 与关键输入哈希。
3. 从封板提交运行完整双架构 dry-run，要求 15 项本地资产和三个 OCI 归档通过；其中公钥和签名是占位文件，不能发布。然后以受控的正式公钥运行 `-ProductionPrepare` 生成 11 个待签名文件。对 SPDX SBOM、`provenance.json` 签名，生成覆盖其余 13 项的 `SHA256SUMS`，最后签该清单。分别运行三次 `scripts/releasesign/main.go -mode verify`，再以 `-ValidateOnly` 校验恰好 15 项。`SIGNED_STRUCTURE_ONLY` 不代表密码学验签。
4. 确认 `provenance.json` 的提交与封板提交一致、`dirtyAtBuild=false`，并核对 tar 内二进制版本、ELF/GLIBC、YashanDB 客户端、包内清单与 SBOM。创建指向同一提交的 annotated tag 和 GitHub Release draft，上传 15 项资产；回下载到新目录重算摘要、验签与复核，暂时保留 draft。
5. 从同一封板提交构建并推送 GHCR `v0.7.0`、`v0.7.0-demo`、`v0.7.0-quickstart` 精确 tag。确认三个 tag 均公开，且可在未登录的 amd64/arm64 环境拉取、启动和完成健康及适用业务链路验收后，才单独把 `latest` 提升到已验收的精确 tag。准备 v0.7.0 官网内容和 PDF，在预发布环境验证静态资源；全部闸门通过后将 GitHub Release draft 转正式、部署官网，并执行发布后烟测。

本地命令示例（输出目录须是全新路径；正式公钥路径由发布负责人在仓库外提供）：

```powershell
git rev-parse HEAD
git status --porcelain --untracked-files=all
pwsh ./scripts/release-dryrun.ps1 -Version v0.7.0 -OutputDirectory dist/release-dryrun/v0.7.0-final
pwsh ./scripts/release-dryrun.ps1 -Version v0.7.0 -ProductionPrepare -PublicKeyPath X:\secure\ed25519-release-public-key-v0.7.0.json -OutputDirectory dist/release-prepare/v0.7.0-final
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
