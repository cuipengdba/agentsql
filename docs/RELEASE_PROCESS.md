# AgentSQL v0.5.0 发布流程与 tag 含义

本文件记录现有脚本的职责及发布闸门。当前工作仅做本地 dry-run 和文档核对；**没有创建 git tag、推送镜像、创建 GitHub Release、签名或发布**。

## 版本与资产

GitHub Release 的版本 tag 是 `v0.5.0`。其 15 个上传文件、来源、校验规则和当前演练状态见 [RELEASE_ASSETS.md](RELEASE_ASSETS.md)。`v0.4.0` 的 [公开 Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.4.0) 是资产命名基线，不能把旧 tar 或旧 SHA-256 改名复用。`docs/release-notes-v0.5.md`、`CHANGELOG.md` 与 `docs/release-day-checklist-v0.5.md` 是现有发布文案和执行清单。

仓库当前没有 `.github/workflows/` 发布工作流。构建与发布入口分布在 `Makefile`、`scripts/` 和发布日清单中；不能将本地 dry-run 的 PASS 解释为 GitHub Actions 或正式发布闸门已通过。

v0.5.0 的目标发布闸门统一为 **2026-10-16 16:00 CST（北京时间，UTC+08:00）**。这是计划时间，不是已封板、已通过闸门或已获准公开发布的证明。封板提交与正式发布仍须通过本流程及[发布日清单](release-day-checklist-v0.5.md)的全部检查。

## 本地 dry-run

在仓库根目录运行 `powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist\release-dryrun\v0.5.0-final`。脚本固定 Rocky 8 与 Syft 镜像 digest，构建 amd64/arm64 包、SBOM、provenance 和三个本地 OCI 归档；需要可用的 Docker daemon、Buildx、双架构运行能力及构建依赖。输出目录已存在时会拒绝覆盖。dry-run 的公钥和三份签名文件均明确标为 `dryRun=true`、`signed=false`、`releasable=false`。

若只核对已有目录，运行 `powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory <目录>`。这个模式只读资产和 git HEAD，不构建镜像。它显示 15 行库存和 SHA-256，并对名称集合、sidecar、13 项 manifest、Yashan 内容、provenance commit 和签名文档执行 fail-closed 校验。正式签名还必须单独使用 `scripts/releasesign/main.go -mode verify` 验证三个原始文件；验证器的 `SIGNED_STRUCTURE_ONLY` 不是密码学验签成功。

## 两个辅助 GHCR tag

`ghcr.io/cuipengdba/agentsql:v0.5.0-demo` 与 `ghcr.io/cuipengdba/agentsql:v0.5.0-quickstart` **都是容器镜像 tag，不是 git tag**。`v0.5.0-demo` 的 Bake target 以主仓库 `Dockerfile` 和 `VERSION=v0.5.0` 构建 demo base；`v0.5.0-quickstart` 的 target 以同一图中的 demo base 为基础，额外放入 `/etc/agentsql/config.demo.yaml` 与 `/etc/agentsql/demo-seed.yaml`。两者都应有 `linux/amd64` 和 `linux/arm64`，且 quickstart 的内容层应与 demo 不同。

`scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -IncludeAuxiliaryTags` 默认只生成本地 OCI 归档和 digest JSON；只有显式 `-Push` 才会写入 GHCR。`-Push` 只处理精确版本及辅助 tag，不移动 `latest`；`-PromoteLatest` 是发布日精确 tag 经公开、匿名双架构验收后的单独操作。dry-run 输出中的 OCI digest 是**本地归档证据**，不表示 GHCR 已有这些 tag、已公开或可匿名拉取。

2026-10-05 的完整本地演练在 `dist/release-dryrun/v0.5.0-20261005b/` 退出码为 0，三个 OCI 归档均通过 `linux/amd64` 与 `linux/arm64` descriptor 检查。`ghcr-agentsql-v0.5.0-image-digests.json` 的 `pushed=false`，记录的三个本地 digest 分别为：主镜像 `sha256:33f98bcbaf66dddc2a055a007931f2ecd25f41d284660663b3ec31bd957509d4`、demo `sha256:8bb2e984d4cdac125f32d01eef0adf609a6f79d0de74ee7f89172e99a97aea96`、quickstart `sha256:a2b81a0ce48d1b22ef7cab033884587522a90ad7322016f91b1452032fd34c2e`。这是本地未签名演练，不是 GHCR 远端状态。

## 正式发布闸门

先在最终封板提交的干净工作树重建，核对 15 个正式文件，完成三份 Ed25519 签名的密码学验证，再按 [发布日清单](release-day-checklist-v0.5.md) 执行 tag、GitHub Release draft、资产回下载、GHCR 精确 tag 的公开与匿名双架构验收、`latest` 提升和最终发布。任一资产缺失、哈希不符、签名失败、架构缺失、provenance 指错提交或发布闸门未通过，均停止；不得以旧 dry-run 文件替代。
