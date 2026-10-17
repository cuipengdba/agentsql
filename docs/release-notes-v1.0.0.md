# AgentSQL v1.0.0 发布说明

v1.0.0 收紧列级授权失败关闭，新增可选的 HTTP MCP 人员委托身份，并把设计、验证、发布、官网和 Live Demo 固定为每版交付流程。此版本不增加数据库类型，也不扩大国产数据库的生产支持声明。

## 新增与变更

- 配有列级策略的 SELECT 不再退回表级授权路径。PostgreSQL 必须由已启用且可用的 B2 绑定器验证投影、谓词、JOIN/自连接等列引用；B2 不可用时执行前拒绝并写审计。MySQL、Vastbase 等没有等价证明的方言仍不支持列策略。
- 新增可选 `mcp.delegation.require_human_token: true`。HTTP MCP 请求同时使用 Agent API Key 和短时、指定 Agent 的人员委托令牌。控制台登录令牌不能直接用于 MCP；委托令牌也不能调用控制台 API。用户退出、停用或失去 `query.execute` 后，委托立即失效。
- 控制台「任务授权」页在启用该模式时提供人员令牌签发与一次性展示；签发写管理审计，审计不可用时不返回令牌。MCP 流量审计的 `details_json.delegated_user_id` 标明经验证的用户 ID；已有 Agent 和任务授权 ID 仍保留。
- 有状态 MCP 会话绑定 Agent 与人员双身份，换人必须重新初始化；无状态入口逐请求验证。双身份模式下 B5 工具、`request_approval` 和 `execute_approved` 暂不开放完整人员归属链路，故关闭。普通模式维持原工具行为。
- 权限页在列策略存在时展示 B2 与方言的执行边界；离线预检仍只给出拒绝或未知，不是允许执行的证明。

## 升级与兼容

- 本版无新增元数据库迁移。仍要先备份 metadata、audit 和配对的 `AGENTSQL_SECRET`，使用 `agentsqlctl migrate --check` 检查后升级；恢复时使用升级前成套备份。
- 人员委托默认关闭，不影响旧客户端。启用后 HTTP MCP 每次请求都必须加 `X-AgentSQL-User-Authorization: Bearer <人员委托令牌>`；控制台须开启。先由已登录且有 `query.execute` 的用户调用 `POST /api/v1/auth/delegation-token`，提交 `{"agent_id":"..."}` 取得令牌。令牌最长 30 分钟，不能续期；到期后重新签发。客户端需能发送自定义 HTTP 头。
- 旧部署中若设置了列策略但显式关闭 PostgreSQL B2，相关 SELECT 会从可能的表级放行改为失败关闭。升级前应检查列策略、B2 就绪状态和目标查询；不能为了恢复查询而移除必要的列约束。

## 明确边界

- 人员委托只证明 HTTP MCP 请求中的人员身份与当前查询权限；Agent Key、数据库账号最小权限、SQL 策略、脱敏和审计仍各自生效。它不等同 OAuth 第三方令牌交换。委托令牌进入可信客户端凭据配置，不能放入日志或公开 Demo。
- 有状态会话在换人后重新初始化；跨请求审批执行和 B5 计划事务在委托模式下关闭，直到其完整双身份审计合同得到验证。stdio 沿用本地 Agent Key 合同。
- PostgreSQL B2 适用的 SQL 形状仍以现有绑定器能力为限。MySQL 的列级绑定、国产数据库复杂 JOIN/TLS/HA/负载及真实客户环境尚未取得新版证据；数据库支持级别维持原档。
- 正式 CI、资产签名、GHCR 摘要、真实 MCP 客户端、官网和 Live Demo 的证据以 [v1.0.0 验收记录](v1.0-acceptance.md)与 GitHub Release 正文为准。未通过的闸门不能写成已交付。
