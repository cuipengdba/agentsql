# AgentSQL Release 资产清单

## v0.7.0 预期资产与正式校验门槛

`scripts/release-dryrun.ps1` 的 `Get-ExpectedAssetNames` 是资产名称的唯一可执行定义。v0.7.0 正式 GitHub Release 应恰好上传以下 15 个文件；不能复用旧版本文件或把本地 dry-run 占位签名上传：

```text
agentsql-v0.7.0-linux-amd64.tar.gz
agentsql-v0.7.0-linux-amd64.tar.gz.sha256
agentsql-v0.7.0-linux-arm64.tar.gz
agentsql-v0.7.0-linux-arm64.tar.gz.sha256
agentsql-v0.7.0.spdx.json
agentsql-v0.7.0.spdx.json.sig.json
ed25519-release-public-key.json
go-version-metadata.txt
install.sh
provenance.json
provenance.json.sig.json
SBOM-GENERATION.txt
SHA256SUMS
SHA256SUMS.sig.json
VERIFYING-SIGNATURES.md
```

当前已完成的 v0.7.0 本地演练得到 15/15 文件校验通过，但签名和公钥仍是明确的 dry-run 占位文件，镜像构建曾以 `-SkipImages` 跳过；该产物来自未封板工作树，不能发布。最终必须先将源码、迁移、内嵌控制台及文档纳入同一提交，从该提交的全新干净工作树重建。运行 `-ProductionPrepare` 前后检查完整 `git status --porcelain --untracked-files=all` 与 HEAD；脚本会拒绝未跟踪非忽略文件，并在构建后复核关键输入哈希。`-ProductionPrepare` 禁止与 `-SkipImages` 同用。`provenance.json` 必须指向最终提交且 `dirtyAtBuild=false`。

正式资产的封板提交 SHA、15 项实际摘要、独立核验的发布公钥 key ID、三份 Ed25519 验签退出码、GitHub Release 回下载结果，以及 GHCR 三个精确 tag 的双架构 digest 和匿名拉取结果，均以 [v0.7.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.7.0) 正文及发布证据为准。上述本地演练不是这些字段的替代。

正式准备首先生成 11 个待签名文件；共用来源校验须在签名前确认正式 provenance 状态、8 个 subjects 的大小及 SHA-256、11 个输入与封板提交中 Git 对象的 SHA-256，并拒绝 dry-run 标记与说明。用 `scripts/releasesign/main.go` 分别签 SPDX SBOM、`provenance.json`，再生成覆盖其余 13 个文件的 `SHA256SUMS` 并签清单，才形成 15 项。分别对三份签名做 `-mode verify` 密码学验签，再运行 `-ValidateOnly` 复核名称、摘要、YashanDB 客户端、来源和内容结构；回下载与官网验收也须执行同一来源校验。`-ValidateOnly` 的 `SIGNED_STRUCTURE_ONLY` 不能替代验签。双架构 OCI、GHCR 精确 tag 的匿名拉取、`latest`、GitHub Release 回下载与官网均是单独闸门，见 [发布流程](RELEASE_PROCESS.md) 和 [部署指南](DEPLOY.md)。

v0.7.0 回滚资产也要分拓扑处理：SQLite 合并库恢复升级前备份与配对密钥后可回已发布的 v0.6.0；PostgreSQL metadata/audit 分库的回退目标是已通过隔离演练的 [v0.6.1 正式维护版](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1)。两版正式包的隔离升级回退复测结果以 v0.7.0 GitHub Release 正文为准。不能使用有分库启动缺陷的原 v0.6.0；旧版不得直接读取新 schema。具体生产部署还须在目标环境单独演练成套备份与恢复。

## v0.5.0 历史资产与演练记录

基线是 [公开的 v0.4.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.4.0)：使用 `gh release view v0.4.0 --repo cuipengdba/agentsql --json assets` 核对到 **15 个上传资产**，与本地 `dist/release/v0.4.0-official/` 的文件名、大小一致。v0.5.0 沿用这 15 个名称及签名结构；GitHub 自动生成的 Source code 压缩包不计入 15 个上传资产。

下表记录 v0.5.0 筹备阶段的预期清单，**不是 v0.7.0 已发布或已通过验证的声明**。来源对应 `scripts/release-dryrun.ps1`、`scripts/build-release-linux.sh`、`scripts/package-release.sh` 和 `scripts/releasesign/main.go`。每个正式 Release 都必须使用其最终封板提交重新生成，并逐项验收。

2026-09-30 的 v0.4.0 社区发布物料只概述了双架构安装包、SHA-256、SBOM 与数字签名，**没有逐项列出 15 个上传文件**；其 AGPLv3 文案也只适用于当时的 v0.4.0。v0.5.0 的 15 项名称和生成/验收规则应以下表与 `Get-ExpectedAssetNames` 为准。签名覆盖 SBOM、provenance 和汇总清单；归档包由 sidecar 与已签名汇总清单校验，不能把“每个发布物都带数字签名”理解为每个文件都有独立签名。旧发布物料不得直接复制为 v0.5 宣传文案。

| # | v0.5.0 资产 | 产生位置与来源 | 必查项 |
| ---: | --- | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | Rocky 8 amd64；`build-release-linux.sh` → `package-release.sh` | 外层和包内 SHA-256、ELF/GLIBC、版本；含 `lib/yashandb/libyascli.so` 与 `libyas_infra.so` |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | `package-release.sh` | 唯一一行 `64hex␠␠filename`，重算 #1 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | Rocky 8 arm64；同 #1 | 与 #1 相同，并确认 AArch64 客户端运行库 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | `package-release.sh` | 唯一一行，重算 #3 |
| 5 | `agentsql-v0.5.0.spdx.json` | 固定 digest 的 Syft，对封板依赖及双架构二进制扫描 | SPDX JSON、版本、包及关系、正式 namespace |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | 正式 Ed25519 签名工具 | 对 #5 原始字节验签 |
| 7 | `ed25519-release-public-key.json` | 正式 release 公钥导出 | key class、key ID、公钥；与三份签名一致 |
| 8 | `go-version-metadata.txt` | 双架构 `go version -m` 合并 | 四个二进制；`yashandb-go v1.4.4` |
| 9 | `install.sh` | 封板提交的 `scripts/install.sh` | 与源码同 SHA-256；语法及离线安装 |
| 10 | `provenance.json` | `release-dryrun.ps1 -ProductionPrepare` | 版本、封板 commit、输入摘要、`dirtyAtBuild=false`；非 dry-run |
| 11 | `provenance.json.sig.json` | 正式 Ed25519 签名工具 | 对 #10 原始字节验签 |
| 12 | `SBOM-GENERATION.txt` | `release-dryrun.ps1 -ProductionPrepare` | Syft digest、epoch、扫描输入和实际 SBOM 一致 |
| 13 | `SHA256SUMS` | #6、#11 完成后生成 | 恰好覆盖除自身与 #14 外的 **13 个**唯一文件名；逐项重算 |
| 14 | `SHA256SUMS.sig.json` | 正式 Ed25519 签名工具，最后签 | 对 #13 原始字节验签 |
| 15 | `VERIFYING-SIGNATURES.md` | `release-dryrun.ps1 -ProductionPrepare` | 三个正式验签命令指向本版本文件 |

这 15 项是 AgentSQL 的上传资产清单；YashanDB C 客户端不是第 16 个独立上传资产，而是包含在两个 Linux tarball 的 `lib/yashandb/` 中。`scripts/package-release.sh` 校验 `yashandb-go v1.4.4`、客户端 ELF 架构和动态依赖，并检查归档中两份必需库。`scripts/install.sh` 校验包内清单，将客户端安装到 `/usr/local/lib/agentsql/yashandb`，在升级和回滚时管理该目录。GHCR 最终镜像包含客户端，并设置 `LD_LIBRARY_PATH=/opt/yashandb-client/lib`。

崖山 C 客户端再分发已获厂家口头授权；仓库无书面授权文件。上述打包位置是现有脚本的目标设计，不是批七十一的产物验收结论。批七十一没有从服务器镜像打包客户端，也没有重建发行物；当时可复现的接入路径是用户自行取得匹配架构的独立客户端，并在启动前设置 `LD_LIBRARY_PATH`。详见 [DEPLOY.md](DEPLOY.md)。客户端缺失或加载失败时，YashanDB 连接失败关闭。资产名称仍为上表 15 项；必须从最终提交重新生成 tarball 和 dry-run，再做验收。

## v0.5.0 当时的证据与判定

- `dist/release-dryrun/v0.5.0/assets/` 有 15 个**旧 dry-run 候选文件**，但 provenance 的 commit 为 `02565f5a40bad6b6b778db1043db98e3df194c3c`，不是当前 HEAD。签名和公钥文件是明确的未签名占位，不可上传。
- 旧目录的 Go metadata 缺 `yashandb-go v1.4.4`；对 `dist/release-dryrun/v0.5.0/assets/` 执行 `-ValidateOnly` 返回非零，打印 15 行库存和 `RELEASE_DRYRUN_RESULT=FAIL`。旧文件的 SHA-256 即使自身匹配，也不能证明它们是当前版本的合格发行物。
- **2026-10-05 历史演练：**`dist/release-dryrun/v0.5.0-20261005b/` 曾由提交 `e3099a3737ff8dcfbf63b088cb949963e312e42a` 生成，当时输出 `RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_IMAGES=PASS`、`RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE`。该演练的 tarball 含 YashanDB C 客户端，但必须按当前完整校验规则从最终提交重新演练；历史结果不能充当发行验收。历史目录中的 15 个文件均非空；`VERIFYING-SIGNATURES.md` 为 589 字节。
- 该历史演练是**未签名、本地、不可发布**的证据：`provenance.json` 标记 `dryRun=true`、`releasable=false`，工作树在构建时不是干净状态。三个 OCI 归档的 digest JSON 标记 `pushed=false`。正式资产仍需在最终封板提交的干净工作树重新生成并完成签名与发布闸门。
- dry-run 的 `*.sig.json` 是占位 JSON。正式流程须分别运行 `scripts/releasesign/main.go -mode verify` 做密码学验签；`-ValidateOnly` 对正式签名只检查结构和文件摘要，不能替代验签。

## v0.5.0 当时的只读核对与本地重建

Windows PowerShell 可直接运行 `.ps1`；装有 PowerShell 7 时也可将 `powershell` 改为 `pwsh`：

```powershell
powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory dist\release-dryrun\v0.5.0\assets
```

验证器会先逐行列出 15 个预期名称、字节数、本地 SHA-256 和 `PRESENT`/`MISSING`，然后校验名称集合、sidecar、`SHA256SUMS`、Yashan Go 依赖与 tarball 内两份必需客户端库、provenance commit 及签名文档。任一失败返回非零；**出现 15 行不等于通过**。`SHA256SUMS` 不包含自身与其签名，以避免循环摘要。

封板后在具备 Docker 和所需构建依赖的环境执行全量本地演练，输出到全新的目录：

```powershell
powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist\release-dryrun\v0.5.0-final
```

默认生成双架构包、SPDX SBOM、provenance、15 个本地 dry-run 文件及三个本地 OCI 镜像归档；不签名、不推送、不创建 git tag。只有 `RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_IMAGES=PASS` 和 `RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE` 同时出现，才能称为**本地未签名演练通过**。正式发行闸门见 [RELEASE_PROCESS.md](RELEASE_PROCESS.md) 和 [发布日清单](release-day-checklist-v0.5.md)。
