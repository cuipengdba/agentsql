# AgentSQL v1.5.0 发布说明

**语言 / Language:** [简体中文](release-notes-v1.5.0.md) | [English](en/release-notes-v1.5.0.md)

## 范围

本版对达梦 DM8 Pack3（`dm8_20241022_x86_rh6_64_single`、COMPATIBLE_MODE=0）、金仓 KingbaseES V9R1C10（PG 模式）和 Vastbase G100 V3.0 Build 9 commit 30156（PG 模式）的指定实例完成深度验收。各自使用最小权限只读账号和合成数据，验证受控 SELECT、表级授权、手机号脱敏、规则拒绝、持久审计、策略与业务账号权限撤销、数据库断连的失败关闭和恢复。

MCP Inspector 0.16.8 CLI 与 Dify 1.17.1 的实际 `MCPClient` 分别对三款数据库完成允许和拒绝调用，并核对脱敏、规则 ID 与审计 ID。Dify 结果来自客户端 SDK 调用，不包含可视化工作流编辑器操作；Claude Desktop 未实测。Vastbase 使用临时许可，不能据此推断正式生产许可。

## 升级与发行

从 v1.4.0 正式包验证了 SQLite schema 22 与 PostgreSQL metadata/audit 分库 schema 21/6 到 v1.5.0 的迁移检查、已有 Agent/审批数据保留和成套备份恢复。v1.5.0 不改变这些 schema，v1.4.0 包的只读 `migrate --check` 仍报告 current。实际回滚仍须停流、备份并确认配置兼容，不应让新旧节点无计划混跑。

正式发行物包含 linux/amd64 和 linux/arm64 安装包、SPDX SBOM、provenance、SHA256SUMS 及 Ed25519 分离签名。主镜像、Demo 镜像与 quickstart 镜像提供 v1.5.0 精确标签和双架构清单。官网、下载资料、GHCR `latest` 与公网 Demo 从 v1.0.0 直接更新到 v1.5.0；发行与公网验收证据见[验收记录](v1.5.0-acceptance.md)。

## 支持边界

结论只覆盖上述数据库版本、模式、拓扑、合成数据和已测试的受控只读 SQL 子集。逐库真实查询超时/取消、独立审计存储故障、厂商 HA、TLS、长时负载、跨版本兼容、正式生产许可和真实客户环境尚未完成验证。本版不声明这些数据库的完整生产支持或厂商认证；完整数据库支持声明须等待相应证据。v1.4.0 的共享 Pipeline 审计故障测试不能替代每款厂商实例的独立结果。
