# Batch 80b：v0.5.0 生产候选重建与签名报告

**结果：本批 7 项目标全部通过。** 本文时间为 2026-10-09 Asia/Shanghai。构建封板为 `fe36c7cdecb9a68426727153ff2dc90487c40ca7`。最终资产位于 `dist/release-prepare/v0.5.0-final/assets/`，恰好 15 个普通文件，无额外文件或子目录。完整带时间戳证据位于 `dist/release-b80b/`。

## 构建和签名

在仓库直接运行 Windows PowerShell 5.1 版 `scripts/release-dryrun.ps1 -Version v0.5.0 -ProductionPrepare -PublicKeyPath D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json -OutputDirectory dist/release-prepare/v0.5.0-final`。19:38:55 完成，退出码 0，输出 `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`、`RELEASE_PREPARE_IMAGES=PASS`、`RELEASE_PREPARE_RESULT=PASS_UNSIGNED_REQUIRES_SIGNING`。完整输出见 `dist/release-b80b/production-prepare.log`。旧 `a41955e` 候选已移至 `dist/release-b80b/superseded-a41955e/` 留档，不属于本批最终资产。

使用仓库 `scripts/releasesign/main.go` 编译的正式 signer 和仓库外既有 Ed25519 seed，依序签署 SPDX、provenance，生成覆盖其余 13 项的 `SHA256SUMS`，再签署 `SHA256SUMS`。19:39:14 的三次 `-mode verify` 均输出 `verified`，key ID 均为 `sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`。签名和验签完整输出见 `dist/release-b80b/sign-and-verify.log`。

## 最终 15 项资产

| 文件 | 字节 |
| --- | ---: |
| `agentsql-v0.5.0-linux-amd64.tar.gz` | 103730667 |
| `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 |
| `agentsql-v0.5.0-linux-arm64.tar.gz` | 94569304 |
| `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | 101 |
| `agentsql-v0.5.0.spdx.json` | 2852836 |
| `agentsql-v0.5.0.spdx.json.sig.json` | 451 |
| `ed25519-release-public-key.json` | 230 |
| `go-version-metadata.txt` | 50348 |
| `install.sh` | 46844 |
| `provenance.json` | 6495 |
| `provenance.json.sig.json` | 441 |
| `SBOM-GENERATION.txt` | 570 |
| `SHA256SUMS` | 1225 |
| `SHA256SUMS.sig.json` | 436 |
| `VERIFYING-SIGNATURES.md` | 1258 |

## 验证证据

- 19:39:15–19:39:30 执行 `-ValidateOnly -AssetsDirectory`，退出码 0，输出 `RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_ITEM_FAIL_COUNT=0`；完整逐项输出见 `dist/release-b80b/validation.log`。
- 19:39:32–19:39:39 从本批两个 tar 各提取 `agentsql` 与 `agentsqlctl` 并读 ELF 头：amd64 两项均为十进制 `e_machine=62`，arm64 两项均为十进制 `e_machine=183`。两个 tar 均包含 `lib/yashandb/libyascli.so` 和 `lib/yashandb/libyas_infra.so`。逐项带时间戳证据见 `dist/release-b80b/architecture-evidence.log`。
- 19:39:40–19:39:44 将 15 文件逐项复制到全新 `dist/release-b80b/redownload/assets/`，每项复制后 SHA-256 与原件一致；复制件的 `SHA256SUMS` 恰好 13 个唯一 basename，逐项哈希匹配。输出 `COPIED_ASSETS=15 MANIFEST_MATCH=13 PASS`，见 `dist/release-b80b/redownload-verify.log`。本批禁止 Release 上传，此项是本地回下载模拟。

## 来源审计对账

`provenance.json` 的 `source.commit=fe36c7cdecb9a68426727153ff2dc90487c40ca7`、`source.dirtyAtBuild=false`，没有演练模式声明。必须出现的 `scripts/release-dryrun.ps1` 输入文件名不属于演练文案。

| 输入文件 | 当前封板预期 SHA-256 | 工作区文件 SHA-256 | provenance.inputs SHA-256 | 结果 |
| --- | --- | --- | --- | --- |
| `scripts/release-dryrun.ps1` | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` | PASS |

旧 `a41955e` 候选的对应输入哈希为 `d3857a4627bb63aa5ecb6d069e76cfd1e612a853cd9b6bf3f759886f9fdbc9b7`；本批新资产已消除该差异。原始带时间戳表见 `dist/release-b80b/source-audit-reconciliation.log`。

## 密钥、仓库边界及遗留

复用的私钥种子和公钥文档仍分别位于仓库外 `D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed` 与 `D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json`；仅核对存在性及导出的公钥副本哈希，未打印密钥或 license 内容，也未重新生成密钥。已跟踪工作树为空，`git diff --check` 通过；`cmd/agentsql/b5-wal/` 与 `cmd/agentsql/﹎memory﹎.instance-id` 均未跟踪、未触碰，git status 中没有 secure 路径。

本批没有执行 git 写操作、Release 创建或上传、GHCR push、官网部署及发布宣传。遗留工作仅为后续批次获授权后的 tag、Release、远端资产回下载、公网 GHCR 和官网发布验收；旧 `a41955e` 留档不可发布。
