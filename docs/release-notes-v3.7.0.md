# AgentSQL v3.7.0 发布说明

**语言 / Language:** [简体中文](release-notes-v3.7.0.md) | [English](en/release-notes-v3.7.0.md)

状态：发布候选；正式发布与公网验收完成后再更新结论。

## 本版能力

1. MongoDB 7.0.45 在指定三成员副本集上增加受控 `find_documents`：管理员显式批准副本集成员与集合索引，网关在连接前拒绝未知成员，逐成员验证 TLS 证书；执行前用 `executionStats` 校验批准索引及实际扫描量，实际读取复用相同索引 hint。
2. Redis 8.2.9 与 Valkey 8.1.10 在指定 Sentinel 拓扑上增加精确键 Hash 字段 `read_hash_fields`：每个字段均需精确授权，固定只读脚本在服务器端限制类型、字段和字节数。Sentinel 与数据节点使用独立 ACL 身份，发现的主节点先核对地址白名单和 TLS 证书。
3. 控制台及 API 支持保存 `read_control_json`。它只包含拓扑与索引元数据；Sentinel 密码由网关环境变量提供，变量名由租户和数据源 ID 派生。SQLite/PostgreSQL 合库与分库元数据均有新迁移。
4. 读取前重验 Agent、租户、权限与数据源，返回前执行脱敏和持久终态审计；审计写入有两秒上限，失败时不交付读取值。

## 兼容与限制

原有 MongoDB、Redis、Valkey 单实例受控读取路径继续保留；新拓扑必须由管理员显式配置。支持结论仅覆盖验收记录中的镜像版本、TLS/ACL 配置和合成数据。MongoDB 分片、Redis/Valkey Cluster、任意聚合/命令/写入、厂商认证及完整生产 SLA 均未纳入本版结论。公开 Demo 仍只展示已部署的 PostgreSQL/MySQL 合成场景。

升级、回退与配置步骤见[升级指南](v3.7.0-upgrade.md)，实际测试和未完成项见[验收记录](v3.7.0-acceptance.md)。
