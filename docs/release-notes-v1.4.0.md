# AgentSQL v1.4.0 发布说明

## 范围

PostgreSQL metadata 部署中的控制台密码与 LDAP 登录改用共享尝试预算。每个规范化租户/账号从首次尝试起的 60 秒内最多 10 次，两个网关在数据库事务中共同裁定；预算键为部署密钥计算的 HMAC，不存账号明文。metadata 故障返回 503，预算耗尽返回 429。SQLite 单节点仍使用本地预算。

本版以共享 PostgreSQL metadata/audit 的两个网关和独立业务数据库进行故障演练：一节点退出、审计库暂时不可用、恢复后的结果交付、链验证和管理 outbox 状态。原有授权、脱敏、审批和审计的失败关闭合同不变，MySQL 列策略仍限定于 v1.3.0 已验证的 8.0/8.4 单表 InnoDB SELECT 子集；其他数据库的支持级别不因本版提升。

## 部署与兼容

新增登录预算表。SQLite 与合并 PostgreSQL metadata 由 21 升至 22，独立 PostgreSQL metadata 由 20 升至 21，audit-only 保持 6。两个节点必须使用相同的密钥和配置、不同的实例身份。升级需停流并成套备份；v1.3.0 与 v1.4.0 节点不能同时承接登录。详见[部署说明](DEPLOY.md)。

## 发布验收

共享预算并发/故障、双节点审计恢复、上一版正式包升级/备份恢复、Go/Web/安全 CI、Ed25519 签名双架构资产、GHCR 三个精确 tag 的匿名双架构拉取均已通过；详见[验收记录](v1.4.0-acceptance.md)和[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.4.0)。官网、公网 Demo 和 GHCR `latest` 按分阶段计划维持 v1.0.0，待 v1.5.0 完成后统一更新。
