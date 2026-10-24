# AgentSQL 当前发行资产与验签

**语言 / Language:** [简体中文](RELEASE_ASSETS.md) | [English](en/RELEASE_ASSETS.md)

当前发布候选版本：v4.0.0。每个维护版都从其正式封板提交重新生成，资产名称的可执行定义为 `scripts/release-dryrun.ps1`；不能复用历史包、占位签名或旧 provenance。

## 正式资产

```text
agentsql-v4.0.0-linux-amd64.tar.gz
agentsql-v4.0.0-linux-amd64.tar.gz.sha256
agentsql-v4.0.0-linux-arm64.tar.gz
agentsql-v4.0.0-linux-arm64.tar.gz.sha256
agentsql-v4.0.0.spdx.json
agentsql-v4.0.0.spdx.json.sig.json
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
pwsh ./scripts/release-dryrun.ps1 -Version v4.0.0 -ProductionPrepare -PublicKeyPath '<trusted-public-key-absolute-path>'
pwsh ./scripts/release-dryrun.ps1 -Version v4.0.0 -ValidateOnly -AssetsDirectory '<signed-assets-directory>'
```

生产准备成功只表示待签资产和三种双架构 OCI 归档已生成；正式签名、密码学验证、GitHub 上传、匿名重下、GHCR 和线上验收分别完成后才可宣称发布。`-ValidateOnly` 的签名结构检查不替代使用受信公钥的密码学验签。

v4.0.0 的包、CI、签名、GHCR、官网与 Demo 结果待发布关卡完成，进度见[发布证据](v4.0.0-release-evidence.md)和[验收记录](v4.0.0-acceptance.md)。[历史资产计划](release-assets-history.md)仅供追溯。

## v4.0.0 本地验签入口

使用已通过独立验签的工具执行下列命令。公钥必须独立取得并保存在资产目录外；初次信任工具的方法见[离线交付指南](v4.0.0-delivery.md)。验签不运行包内程序，输出包含源码提交与 key ID。

```sh
agentsqlctl release verify --directory ./release-assets --public-key ./trusted-release-key.json --version v4.0.0
```
