# AgentSQL 当前版本发布流程

**语言 / Language:** [简体中文](RELEASE_PROCESS.md) | [English](en/RELEASE_PROCESS.md)

当前正式基线：v3.0.2。每次维护版按[每版交付流程](RELEASE_ROUTINE.md)完成设计、代码、测试、升级恢复、CI、签名产物、GitHub Release、GHCR、官网和 Demo 的完整交付。

1. 核对上一正式版和本版范围，编写设计与验收用例。数据库支持声明绑定指定版本、模式、操作与实测证据。
2. 在隔离分支完成代码、内嵌 Web、文档、官网和 PDF。执行 Go/Web、适用安全负向集、真实集成以及升级恢复检查。
3. 审查并合入 PR，等待对应提交的 Go、Web、govulncheck 全绿。封板后从完整干净提交构建，前后检查源码哈希和 HEAD。
4. 运行 `release-dryrun.ps1 -ProductionPrepare` 生成 11 项待签资产和三种双架构 OCI 归档；正式签署 SPDX、provenance、SHA256SUMS，完成三份 Ed25519 密码学验签，15 项清单与双架构内容全部通过。见[当前资产清单](RELEASE_ASSETS.md)。
5. 创建指向该提交的带注释标签，推送精确 GHCR 主/Demo/quickstart 标签，验证公开状态、amd64/arm64 清单、匿名拉取、版本与 OCI revision。保存原 `latest`，暂不移动它。
6. 创建 GitHub 草稿 Release 并上传全部签名资产，核对名称、数量、来源与哈希。公开后匿名重新下载并验签。
7. 备份旧 Demo 的数据、配置和配对密钥；在独立候选卷验证只读诊断、允许/拒绝、脱敏和持久审计，再用可回滚脚本切换。核对公网版本与真实调用。
8. 部署同版本中英文官网、下载、PDF 与兼容边界，核对 HTTP、安全头、资源、版本、语言切换和公网浏览器表现。
9. 官网与 Demo 通过后，按已验收的主镜像 digest 单独提升 GHCR `latest`，再次匿名拉取并运行。补齐发布记录、实际命令退出码、摘要、环境与未测项。
10. 将规范工作区 `D:\ruanjiansheji\agentsql` 的 `main` 同步到最新 `origin/main`，核对清洁状态；辅助分支与已有本地修改须保留。

任一必需门禁失败就修复并重新验证对应候选。若线上切换失败，恢复旧配置与成套数据备份，不把候选写成已通过。历史发布物及其许可、摘要和签名保持不变。

当前版本证据见[v3.0.2 正式发布记录](v3.0.2-release-evidence.md)。[早期发布执行记录](release-process-history.md)保留原始时间与版本，不能代替本轮验收。
