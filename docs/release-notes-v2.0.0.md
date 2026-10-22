# AgentSQL v2.0.0 发布说明

**语言 / Language:** [简体中文](release-notes-v2.0.0.md) | [English](en/release-notes-v2.0.0.md)

v2.0.0 增加固定双网关 Kubernetes 部署方案。两个网关共享 PostgreSQL 元数据库与独立审计库，各自保存 B2 运行时实例标识；无状态 MCP 只读请求在一个网关不可用时由另一个网关继续受控处理。部署包含非 root 运行、独立 PVC、节点反亲和、Service、就绪探针、手动迁移 Job、只读 schema 检查和部署前检查脚本。

PostgreSQL 列策略新增管理端 `POST /api/v1/policies/{id}/bind-postgres`。管理员提交策略当前 `ETag` 和一条登记用 SELECT；网关先发现目录，再在控制面策略版本锁下持有业务表目录锁，复核对象与列身份后原子提交正式绑定。登记 SQL 不执行。本版仅支持 `CATALOG_CLOSED_V1`、单张普通表和明确列投影；原生扩展模式、跨表、视图及通配投影的管理端登记仍会拒绝。

两个网关同一版本同时启动时，RBAC 初始创建与 B2 控制栅栏的短暂并发冲突会有有界重试。持续的数据库故障、策略未登记或不同二进制摘要仍保持失败关闭。

## 升级和运行边界

- 与 v1.9.0 使用相同的 PostgreSQL metadata 21、audit 6 schema。迁移仍须显式运行并检查双库状态，不能由网关暗中迁移。
- B2 租约绑定二进制摘要，v1.9.0 与 v2.0.0 不能混合活跃。Kubernetes 清单使用 `OnDelete`；升级和回退均需维护窗口：成套备份、停止两个旧 Pod、等待租约过期、检查 schema，再启动新版本。**本版不承诺无停机滚动升级。**
- 双网关配置关闭 B5 跨请求事务、HTTP 状态会话与事件续传；不会把节点本地连接或 WAL 当作共享状态。原有单节点部署合同另行适用。
- 固定两个副本，各自 Agent QPS 上限为 10，集群理论上界为 20；单节点故障时上界为 10。此限流按进程执行，不构成全局强一致配额；不支持任意水平扩容。
- PostgreSQL 元数据库、审计库的高可用、TLS、备份、最小权限和容量由部署者提供。本版 Kubernetes 验收使用隔离的两节点 kind 集群与合成 PostgreSQL 数据，不等同于客户生产拓扑认证。

本版不增加数据库适配或扩大既有数据库的生产支持声明。部署、故障恢复与验收细节见 [Kubernetes 操作指南](../deploy/kubernetes/README.md)、[设计](v2.0.0-design.md)与[验收记录](v2.0.0-acceptance.md)。
