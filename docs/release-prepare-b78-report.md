# Batch 78 发布资产准备报告

**结论：FAIL / 已停止。** 2026-10-09 12:51（Asia/Shanghai），正式密钥已生成，但生产候选脚本在构建前的干净工作区检查退出 1。没有生成、签名或验证 Release 资产。此报告不能作为 batch79 发布依据。

## 密钥与运行证据

- 私钥种子：`D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed`，仓库外，44 字节；未记录密钥内容。
- 公钥 JSON：`D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json`，仓库外，230 字节；keyId 为 `sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`。
- 两文件创建时间均为 2026-10-09 12:50:33（Asia/Shanghai）。路径级检查确认均在仓库 `D:\ruanjiansheji\agentsql-v04` 之外。公钥尚未复制进产物目录。
- 完整逐行带时间戳的脚本日志：`dist/release-b78/production-prepare.log`。命令为 `powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ProductionPrepare -PublicKeyPath D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json -OutputDirectory dist/release-prepare/v0.5.0-final`；退出码 1。脚本内部只读 git 状态检查；本批未执行 git 写操作。

## 阻塞点

脚本输出 `RELEASE_PREPARE_RESULT=FAIL`，原因是工作区存在以下两个未跟踪路径，脚本要求完全干净才开始构建：

```text
?? cmd/agentsql/b5-wal/
?? "cmd/agentsql/\357\200\272memory\357\200\272.instance-id"
```

这两个路径属于本批明确禁止触碰的范围。未删除、移动、修改或加入忽略规则；未绕过脚本的干净工作区闸门。`dist/release-prepare/v0.5.0-final` 未创建。用户给出的 sealed HEAD 简写为 `a41955e`；脚本在读取 provenance commit 前已退出，因此本批没有可核对的 provenance 文件。

## 权威 15 资产清单与实际状态

以下均为**预期名称，未生成**；最终目录不存在，实际资产数为 0。

| # | 文件名 | 状态 |
| ---: | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | 未生成 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 未生成 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | 未生成 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | 未生成 |
| 5 | `agentsql-v0.5.0.spdx.json` | 未生成 |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | 未生成 |
| 7 | `ed25519-release-public-key.json` | 未生成 |
| 8 | `go-version-metadata.txt` | 未生成 |
| 9 | `install.sh` | 未生成 |
| 10 | `provenance.json` | 未生成 |
| 11 | `provenance.json.sig.json` | 未生成 |
| 12 | `SBOM-GENERATION.txt` | 未生成 |
| 13 | `SHA256SUMS` | 未生成 |
| 14 | `SHA256SUMS.sig.json` | 未生成 |
| 15 | `VERIFYING-SIGNATURES.md` | 未生成 |

## 验收结果与遗留问题

- `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`、`PASS_UNSIGNED_REQUIRES_SIGNING`：未达到。
- `provenance.source.commit`、`dirtyAtBuild=false`、说明文件无 dry-run 文案：无文件可判定。
- SHA256SUMS 对 13 项逐项比对：未执行；没有可校验的 manifest。
- SPDX、provenance、SHA256SUMS 三次 Ed25519 验签：均未执行，不能报告 `verified`。
- `-ValidateOnly` 的 `RELEASE_DRYRUN_ASSET_COUNT=15`：未执行；不存在 15 资产目录。
- linux/amd64 与 linux/arm64 tar 内二进制架构：均无本批构建证据，不能用旧批次产物替代。
- 模拟回下载：未执行；没有最终资产可复制或重算。

要继续 C 节，需先由有权处理上述两个受保护路径的负责人提供**确实干净的工作区**，再重新运行生产候选、按 C 节顺序签名和逐项复验。现有正式私钥应继续在仓库外受控保管；本批未 tag、push、上传或发布。
