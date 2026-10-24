# AgentSQL v3.9.0 发布说明

**语言 / Language:** [简体中文](release-notes-v3.9.0.md) | [English](en/release-notes-v3.9.0.md)

状态：已正式发布；完整交付结果见[发布证据](v3.9.0-release-evidence.md)。

## 本版能力

1. 指定 Oracle 26ai Free 23.26.3.0.0 单服务支持 `verify-full` TCPS：管理员提供绝对路径 CA 文件，连接主机名必须匹配证书。网关拒绝未知 CA、错误主机名和跳过验证；连接池每次重建物理连接均独立校验证书。驱动升级到 go-ora v3.0.1，禁用跨账号共享的 fast-login 缓存。 传输超时后立即丢弃物理连接，避免驱动清理阶段重复等待。
2. Oracle 管理员可显式设置 `read_control_json` 为 `{"oracle":{"explain":true}}`。该模式要求验证 TLS 和专用非系统只读账号；授权的窄 SELECT 在交付前取得有界 `PLAN_TABLE` 计划并执行 R004/R005 成本预检。计划权限、结构、超时或业务查询失败均不交付原始行。未开启时不宣称 Oracle 计划预算有效。
3. 指定 SQL Server 2025 17.0.5015 单实例完成严格证书轮换、错误 CA/主机名、SHOWPLAN 撤权、受控查询及审计验证。该路径沿用已有只读控制，不把单实例验证写成 Always On、只读副本或厂商认证。

## 安全与兼容边界

授权、精确表列策略、来源脱敏和持久终态审计仍在 MCP 交付路径上。Oracle 计划模式只覆盖已验收的窄 SELECT；TCPS 钱包、mTLS、RAC、Oracle 商业版、SQL Server Always On 与其他部署拓扑未验证。本版不声明两库“完整生产支持”，无业务写入开放，公网 Demo 仍用 PostgreSQL/MySQL 合成数据。

本版没有新增 metadata/audit schema 迁移。升级和回退须成套备份 metadata、独立 audit 与密钥，并按[升级指南](v3.9.0-upgrade.md)处理新增 Oracle 配置。真实环境及正式资产结果见[验收记录](v3.9.0-acceptance.md)和[发布证据](v3.9.0-release-evidence.md)。
