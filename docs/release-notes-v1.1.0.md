# AgentSQL v1.1.0 发布说明

## 本版能力

- MCP 双身份模式扩展到审批申请、审批单读取和 `execute_approved`。审批单不可变 seal 包含申请用户；数据库在单次领取时同时检查租户、Agent 和申请用户。错误用户或旧单 Agent 请求无法消耗委托审批单。
- B5 logical session 在双身份模式中开放。服务端从已验证的用户、Agent、租户派生跨请求 principal，绑定持久化会话、continuation proof 和事务计划。DML grant 继续取自同一 Agent，委托身份不增加数据库权限。
- 新签发的 MCP 用户委托令牌可在控制台凭原令牌单独撤销，活跃 MCP 请求逐次查验撤销状态。v1.0.0 的无令牌 ID 凭据保留原有效期，最长 30 分钟，可通过退出父会话撤销。
- 控制台密码与 LDAP 登录按租户和账号做每分钟尝试次数限制；当前限流为单实例边界。

## 兼容与升级

SQLite 合并元数据 schema 从 20 升至 21；PostgreSQL 合并 schema 同步升至 21。分离 metadata schema 从 19 升至 20；audit schema 不变。迁移新增审批单申请用户字段和索引。旧审批单的申请用户为空，继续在原单 Agent 模式下按既有条件执行；双身份模式不能领取旧审批单。

本版不改变 DM8、KingbaseES V9、Vastbase G100 或 MySQL 的生产支持级别。委托 B5 仅沿现有 PostgreSQL B5 的已验证边界开放；数据库方言、计划或审计无法证明时仍拒绝。

## 分阶段公开发布

v1.1.0 独立创建 GitHub tag、正式 Release 和精确版本资产。按产品负责人指定的发布节奏，官网及公网 Live Demo 在 v1.1.0 至 v1.4.0 期间保持 v1.0.0，到 v1.5.0 完成后直接更新；GHCR `latest` 也保持 v1.0.0，避免指向与官网 Demo 不同的默认版本。请使用 v1.1.0 精确版本资产或镜像进行本版部署。

正式 CI、资产签名、真实客户端和升级恢复结果以 [v1.1.0 验收记录](v1.1.0-acceptance.md)及正式 GitHub Release 附件为准；未完成的项目不会写作通过。
