# AgentSQL 当前发行资产与验签

**语言 / Language:** [简体中文](RELEASE_ASSETS.md) | [English](en/RELEASE_ASSETS.md)

当前正式版本：v3.5.0。每个维护版都从其正式封板提交重新生成，资产名称的可执行定义为 `scripts/release-dryrun.ps1`；不能复用历史包、占位签名或旧 provenance。

## 正式资产

```text
agentsql-v3.5.0-linux-amd64.tar.gz
agentsql-v3.5.0-linux-amd64.tar.gz.sha256
agentsql-v3.5.0-linux-arm64.tar.gz
agentsql-v3.5.0-linux-arm64.tar.gz.sha256
agentsql-v3.5.0.spdx.json
agentsql-v3.5.0.spdx.json.sig.json
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

SPDX、provenance 和 SHA256SUMS 分别由 Ed25519 签名；清单覆盖其余 13 项。公钥必须通过独立受信渠道核对 key ID。下载并阅读随包 `VERIFYING-SIGNATURES.md`，使用受信公钥验证三份签名，再校验清单中的文件哈希；不要先运行尚未验证的安装脚本。

正式包还需验证二进制版本、OCI revision、实际 Go 工具链、源提交与 `dirtyAtBuild=false`。两个架构均包含已授权再分发的指定 YashanDB 客户端与驱动；不能仅因包内有库就声称已完成所有数据库生产验收。安装系统要求、升级和恢复见[部署指南](DEPLOY.md)。

## 维护版生产准备

```powershell
pwsh ./scripts/release-dryrun.ps1 -Version v3.5.0 -ProductionPrepare -PublicKeyPath '<trusted-public-key-absolute-path>'
pwsh ./scripts/release-dryrun.ps1 -Version v3.5.0 -ValidateOnly -AssetsDirectory '<signed-assets-directory>'
```

生产准备成功只表示待签资产和三种双架构 OCI 归档已生成；正式签名、密码学验证、GitHub 上传、匿名重下、GHCR 和线上验收分别完成后才可宣称发布。`-ValidateOnly` 的签名结构检查不替代使用受信公钥的密码学验签。

实际 v3.5.0 包摘要、CI、签名、GHCR、官网与 Demo 证据见[正式发布记录](v3.5.0-release-evidence.md)。[历史资产计划](release-assets-history.md)仅供追溯。
