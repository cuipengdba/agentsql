# AgentSQL v0.5.0 Release 资产清单

基线是 [公开的 v0.4.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.4.0)：使用 `gh release view v0.4.0 --repo cuipengdba/agentsql --json assets` 核对到 **15 个上传资产**，与本地 `dist/release/v0.4.0-official/` 的文件名、大小一致。v0.5.0 沿用这 15 个名称及签名结构；GitHub 自动生成的 Source code 压缩包不计入 15 个上传资产。

下表是预期清单，**不是 v0.5.0 已发布或已通过验证的声明**。来源对应 `scripts/release-dryrun.ps1`、`scripts/build-release-linux.sh`、`scripts/package-release.sh` 和 `scripts/releasesign/main.go`。正式 Release 必须使用最终封板提交重新生成，并逐项验收。

2026-09-30 的 v0.4.0 社区发布物料只概述了双架构安装包、SHA-256、SBOM 与数字签名，**没有逐项列出 15 个上传文件**；其 AGPLv3 文案也只适用于当时的 v0.4.0。v0.5.0 的 15 项名称和生成/验收规则应以下表与 `Get-ExpectedAssetNames` 为准。签名覆盖 SBOM、provenance 和汇总清单；归档包由 sidecar 与已签名汇总清单校验，不能把“每个发布物都带数字签名”理解为每个文件都有独立签名。旧发布物料不得直接复制为 v0.5 宣传文案。

| # | v0.5.0 资产 | 产生位置与来源 | 必查项 |
| ---: | --- | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | Rocky 8 amd64；`build-release-linux.sh` → `package-release.sh` | 外层和包内 SHA-256、ELF/GLIBC、版本、Yashan 客户端两库 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | `package-release.sh` | 唯一一行 `64hex␠␠filename`，重算 #1 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | Rocky 8 arm64；同 #1 | 与 #1 相同，并确认 AArch64 |
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

## 当前证据与判定

- `dist/release-dryrun/v0.5.0/assets/` 有 15 个**旧 dry-run 候选文件**，但 provenance 的 commit 为 `02565f5a40bad6b6b778db1043db98e3df194c3c`，不是当前 HEAD。签名和公钥文件是明确的未签名占位，不可上传。
- 旧 tar 缺少 `lib/yashandb/libyascli.so` 和 `libyas_infra.so`，Go metadata 缺 `yashandb-go v1.4.4`；对旧目录执行 `-ValidateOnly` 返回非零，打印 15 行库存和 `RELEASE_DRYRUN_RESULT=FAIL`。旧文件的 SHA-256 即使自身匹配，也不能证明它们是当前版本的合格发行物。
- **2026-10-05 新演练：**`dist/release-dryrun/v0.5.0-20261005b/` 由当前 HEAD `e3099a3737ff8dcfbf63b088cb949963e312e42a` 生成。命令退出码为 0，输出 `RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_IMAGES=PASS`、`RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE`。单独再次运行 `-ValidateOnly` 也返回 0。15 个文件均非空；`VERIFYING-SIGNATURES.md` 为 589 字节。stdout 列有每项名称、字节数和完整 SHA-256；该目录的 `SHA256SUMS` 可供复核其中 13 项。
- 新演练是**未签名、本地、不可发布**的证据：`provenance.json` 标记 `dryRun=true`、`releasable=false`，工作树在构建时不是干净状态。三个 OCI 归档的 digest JSON 标记 `pushed=false`。正式资产仍需在最终封板提交的干净工作树重新生成并完成签名与发布闸门。
- dry-run 的 `*.sig.json` 是占位 JSON。正式流程须分别运行 `scripts/releasesign/main.go -mode verify` 做密码学验签；`-ValidateOnly` 对正式签名只检查结构和文件摘要，不能替代验签。

## 只读核对与本地重建

Windows PowerShell 可直接运行 `.ps1`；装有 PowerShell 7 时也可将 `powershell` 改为 `pwsh`：

```powershell
powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory dist\release-dryrun\v0.5.0\assets
```

验证器会先逐行列出 15 个预期名称、字节数、本地 SHA-256 和 `PRESENT`/`MISSING`，然后校验名称集合、sidecar、`SHA256SUMS`、Yashan 内容、provenance commit 及签名文档。任一失败返回非零；**出现 15 行不等于通过**。`SHA256SUMS` 不包含自身与其签名，以避免循环摘要。

封板后在具备 Docker 和所需构建依赖的环境执行全量本地演练，输出到全新的目录：

```powershell
powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist\release-dryrun\v0.5.0-final
```

默认生成双架构包、SPDX SBOM、provenance、15 个本地 dry-run 文件及三个本地 OCI 镜像归档；不签名、不推送、不创建 git tag。只有 `RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_IMAGES=PASS` 和 `RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE` 同时出现，才能称为**本地未签名演练通过**。正式发行闸门见 [RELEASE_PROCESS.md](RELEASE_PROCESS.md) 和 [发布日清单](release-day-checklist-v0.5.md)。
