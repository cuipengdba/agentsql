# Batch 79 ProductionPrepare 与签名复验报告

**状态：本地 15 项候选资产和 C 节校验通过；发布来源审计仍有一项遗留。** 时间均为 2026-10-09 Asia/Shanghai。本批没有执行 git add/commit/tag/merge/push、上传或发布。

## Gate 修复与运行方式

`scripts/release-dryrun.ps1` 仅在 ProductionPrepare 的 `$productionDirty` 查询中将 `git status --porcelain` 改为 `git status --porcelain --untracked-files=no`。保留退出码检查和 dirty 时抛错。正式脚本改动后，已跟踪状态是 ` M scripts/release-dryrun.ps1`；负向测试以退出码 1 抛出 `Production preparation requires a completely clean worktree`，见 `dist/release-b79/tracked-dirty-gate.log`。受保护的两个未跟踪路径没有被加入或修改。

当前机器没有 `pwsh` 命令，使用 Windows PowerShell 执行同一 `.ps1`。由于本批禁止提交，而 gate 必须拒绝任何已跟踪修改，ProductionPrepare 于已跟踪工作区干净时从 `scripts/release-dryrun-b79-run.ps1` 临时同目录副本执行；该副本只含上述一行修复，构建后与正式修复版的 SHA-256 完全一致，随后删除。传参为 `-Version v0.5.0 -ProductionPrepare -PublicKeyPath D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json -OutputDirectory dist/release-prepare/v0.5.0-final`，无 `-SkipImages`。完整日志为 `dist/release-b79/production-prepare.log`。第一次尝试被日志包装层把 Docker 的普通 stderr 当作终止错误而中断；日志和部分目录保存在 `dist/release-b79/production-prepare-attempt-1.log` 与 `failed-attempt-1/`，未用于签名。修正包装层后第二次运行于 14:53:01 退出码 0。

第二次输出 `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`、`RELEASE_PREPARE_IMAGES=PASS`、`RELEASE_PREPARE_RESULT=PASS_UNSIGNED_REQUIRES_SIGNING`。`provenance.json` 的 `source.commit=a41955e87d2e603d6ba4bd2dcd554f2665e86931`、`dirtyAtBuild=false`，说明文档和 provenance 无 `DRY RUN ONLY` 等演练声明。生产说明中出现的 `release-dryrun.ps1` 仅为验证工具文件名。

## 最终 15 项资产

目录：`dist/release-prepare/v0.5.0-final/assets/`，恰好 15 个普通文件，无额外文件或子目录。

| # | 文件名 | 字节 |
| ---: | --- | ---: |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | 103724113 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | 94562868 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | 101 |
| 5 | `agentsql-v0.5.0.spdx.json` | 2852836 |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | 451 |
| 7 | `ed25519-release-public-key.json` | 230 |
| 8 | `go-version-metadata.txt` | 50348 |
| 9 | `install.sh` | 46844 |
| 10 | `provenance.json` | 6495 |
| 11 | `provenance.json.sig.json` | 441 |
| 12 | `SBOM-GENERATION.txt` | 570 |
| 13 | `SHA256SUMS` | 1225 |
| 14 | `SHA256SUMS.sig.json` | 436 |
| 15 | `VERIFYING-SIGNATURES.md` | 1258 |

签名顺序是 SPDX、provenance、生成覆盖另外 13 项的 `SHA256SUMS`、签 `SHA256SUMS`。使用仓库 `scripts/releasesign/main.go` 编译出的正式 signer，未使用开发签名工具；三次 `-mode verify` 均输出 `verified`，同一 key ID 为 `sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`。`-ValidateOnly -AssetsDirectory` 返回 `RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_ITEM_FAIL_COUNT=0`。完整输出在 `dist/release-b79/sign-and-verify.log`。

模拟回下载将最终 15 文件逐项复制到 `dist/release-b79/redownload/assets/`，逐个重算 SHA-256 与原件相等；复制件的 `SHA256SUMS` 恰好 13 个唯一 basename，逐项重算均匹配。记录：`dist/release-b79/redownload-verify.log`，结果 `COPIED_ASSETS=15 MANIFEST_MATCH=13 PASS`。这是本地复制模拟，没有访问远端 Release。

## 双架构 tar 证据

直接从**本批两个 tar**各提取 `agentsql`、`agentsqlctl`，读取 ELF 头中的 `e_machine`，结果如下；日志为 `dist/release-b79/architecture-evidence.log`。

| 时间（+08:00） | tar 平台 | 二进制 | ELF `e_machine` | 结果 |
| --- | --- | --- | ---: | --- |
| 14:53:44 | linux/amd64 | `agentsql` | 62（x86-64） | PASS |
| 14:53:46 | linux/amd64 | `agentsqlctl` | 62（x86-64） | PASS |
| 14:53:48 | linux/arm64 | `agentsql` | 183（AArch64） | PASS |
| 14:53:49 | linux/arm64 | `agentsqlctl` | 183（AArch64） | PASS |

`-ValidateOnly` 另通过两 tar 的 YashanDB 必需库检查；ProductionPrepare 本地多架构 OCI 镜像检查也输出 `RELEASE_PREPARE_IMAGES=PASS`。本批未进行 GHCR 推送或公开拉取。

## 密钥保管与遗留问题

复用已有私钥种子 `D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed` 和公钥文档 `D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json`，两者仍在仓库 `D:\ruanjiansheji\agentsql-v04` 之外；文件时间保持 12:50:33，未重新生成。资产中的公钥副本与 secure 公钥 SHA-256 相同。没有打印或记录私钥及 license 内容，git 状态不含 secure 路径。

**来源审计遗留：** sealed HEAD 的 `scripts/release-dryrun.ps1` 是修复前版本。`provenance.inputs` 中该脚本哈希为 `d3857a4627bb63aa5ecb6d069e76cfd1e612a853cd9b6bf3f759886f9fdbc9b7`；实际运行的临时修复版与最终工作区脚本哈希为 `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0`。本批按禁提交要求无法使修复进入 sealed HEAD，所以这些资产虽通过上述内容、签名和架构校验，**不可在未消除该来源差异时直接发布**。下一批须将 gate 修复纳入新的封板提交，从该提交重新构建、签名、验签，并更新 provenance commit。tag、Release、资产上传、远端回下载和 GHCR 公开验收也都留待下一批。

受保护的 `cmd/agentsql/b5-wal/` 与 `cmd/agentsql/﹎memory﹎.instance-id` 仍未跟踪，未纳入 git；原有未跟踪 `docs/release-prepare-b78-report.md` 未改动。本批新增的报告为本文件，已跟踪文件改动只有 `scripts/release-dryrun.ps1`。`git diff --check` 通过。
