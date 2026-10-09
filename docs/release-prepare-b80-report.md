# Batch 80 新封板重建预检报告（阻断）

状态：**未完成，禁止发布现存资产**。预检时间：2026-10-09 16:21:02 +08:00（Asia/Shanghai）。本批未运行 ProductionPrepare、签名、ValidateOnly、架构提取或回下载复验，也未执行任何上传或发布操作。下表仅记录上一批留在目标目录的旧资产，**不是 batch80 重建产物**。

## 阻断原因

本批同时要求“直接运行正式 ProductionPrepare”和“全部在本地完成，不联网”。现有正式脚本无法在不联网的条件下完成：`scripts/release-dryrun.ps1` 在两个架构的 Docker 容器中先执行 `dnf install`，再调用 `scripts/build-release-linux.sh`；后者无条件访问 `go.dev`、`mirrors.aliyun.com` 和 GitHub 的 YashanDB 客户端 URL，并运行 `go mod download`。镜像构建还会通过 Buildx 解析镜像。`dist/release-b79/production-prepare.log` 记录了上批实际访问 Rocky/Debian 镜像源、下载依赖及解析 Docker Hub 镜像的过程。仓库没有可使这条正式命令离线完成的参数。为遵守本批无联网红线，预检在执行命令前停止；没有尝试网络连接。

本机也没有 `pwsh` 可执行文件；上批使用 Windows PowerShell 执行同一 `.ps1`。这个差异尚未进入 batch80 构建，因为无联网条件已先行阻断。

## 来源审计预检

| 项目 | SHA-256 / 值 |
| --- | --- |
| 当前 HEAD | `f40d2441f042430745cb2a3eeb887aa07f5234ff` |
| 当前 HEAD 工作区 `scripts/release-dryrun.ps1`（跟踪文件无改动） | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` |
| 现存 `provenance.json` 的 `source.commit` | `a41955e87d2e603d6ba4bd2dcd554f2665e86931` |
| 现存 `provenance.inputs` 中 `scripts/release-dryrun.ps1` | `d3857a4627bb63aa5ecb6d069e76cfd1e612a853cd9b6bf3f759886f9fdbc9b7` |

结论：当前两个脚本哈希不相等，来源审计差异**仍存在**。旧目录 `dist/release-prepare/v0.5.0-final/assets/` 保持原样，没有重签或改写来源声明。

## 目标目录现存 15 项旧资产（不可作为 batch80 交付）

| 文件 | 字节 |
| --- | ---: |
| `agentsql-v0.5.0-linux-amd64.tar.gz` | 103724113 |
| `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 |
| `agentsql-v0.5.0-linux-arm64.tar.gz` | 94562868 |
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

现存目录恰好 15 个普通文件，但其提交来源为旧封板。上批验签、ValidateOnly、ELF 架构、回下载证据均不计作 batch80 验收。本批对应四份日志位于 `dist/release-b80/`，明确记录各步骤未执行；没有 `RELEASE_PREPARE_*` 成功行、三次 `verified`、`RELEASE_DRYRUN_*` 成功行或 `COPIED_ASSETS=15 MANIFEST_MATCH=13 PASS`。

## 密钥、工作区与遗留问题

私钥路径 `D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed`，公钥源路径 `D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json`，两者均在仓库外且仅检查存在性；未读取、打印或记录私钥及 license 内容。`git status --porcelain --untracked-files=no` 为空，`git diff --check` 通过；受保护的 `cmd/agentsql/b5-wal/` 与 `cmd/agentsql/﹎memory﹎.instance-id` 保持未跟踪、未触碰，git 状态无 secure 内容。

遗留问题：需要提供获准的离线构建流程及完整本地依赖/镜像缓存，或明确调整“不得联网”约束，然后才能从 `f40d244` 重新执行 ProductionPrepare 和本批其余验收。此前不得发布现存目录，也不得把旧资产改写为新封板来源。
