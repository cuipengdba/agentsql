# RBAC / 多租户 / SSO MVP 设计（v0.5）

## 1. 现状调研

### 1.1 认证与用户现状

- 控制台登录位于 `internal/adminapi/handler.go`。账号来自
  `AGENTSQL_ADMIN_USER`（缺省 `admin`）与必填的
  `AGENTSQL_ADMIN_PASSWORD`，启动入口在 `cmd/agentsql/main.go` 调用
  `AdminCredentialsFromEnv` 和 `config.ValidateAdminPassword`。
- 控制台令牌实现位于 `internal/adminapi/auth_token.go`。它不是三段式 JWT，
  而是由 `AGENTSQL_SECRET` 派生密钥签名的 HMAC-SHA256 有时效令牌，原有
  有效期为 12 小时。Bearer 解析和校验位于 `Handler.adminAuth`。
- Agent（机器身份）认证位于 `internal/auth/auth.go`：明文 API Key 只在创建或
  轮换时返回一次，数据库仅存 SHA-256 摘要，并以常量时间比较。它与控制台
  人类用户认证是两个安全域，本设计不混用二者。
- v0.4 没有控制台用户、租户、角色、权限或用户角色关联表。全部控制台 API
  只校验“是否持有有效管理员令牌”，没有逐路由授权。

### 1.2 路由与数据库现状

- 控制台路由统一在 `adminapi.NewHandler` 的 `http.ServeMux` 注册，再由
  `mcpserver.WithAdminAPI` 挂载；现有路径前缀为 `/api/v1/`。
- `internal/store/migrate.go` 通过 `embed.FS` 嵌入迁移，以
  `schema_migrations` 记录版本，在事务内执行。元数据同时支持 SQLite 与
  PostgreSQL，并有“元数据/审计同库”和“审计分库”两种迁移流。
- v0.5 沿用该机制：同库流新增版本 11，独立元数据流新增版本 10；审计分库
  不承载身份数据，故不增加 RBAC 表。

## 2. 数据模型与隔离规则

关系如下：

```text
tenant 1──N user N──N role N──N permission
                    │
                    └──N parent_role（同租户继承）
```

| 表 | 关键字段 | 约束 |
|---|---|---|
| `tenants` | `id`, `name`, `status` | ID、名称唯一；状态为 active/disabled |
| `users` | `id`, `tenant_id`, `username`, `password_hash`, `status`, `auth_provider`, `external_subject` | `(tenant_id, username)` 唯一；只存 bcrypt 哈希 |
| `roles` | `id`, `tenant_id`, `name`, `builtin` | `(tenant_id, name)` 唯一 |
| `permissions` | `code`, `description` | 权限代码全局稳定、只读 |
| `user_roles` | `tenant_id`, `user_id`, `role_id` | 复合外键保证用户与角色同租户 |
| `role_permissions` | `tenant_id`, `role_id`, `permission_code` | 角色只在自身租户授权 |
| `role_inheritance` | `tenant_id`, `role_id`, `parent_role_id` | 同租户继承、禁止自继承；运行时检测环 |

所有用户、角色查询都要求显式非空 `tenant_id`，SQL 必须同时过滤该字段；空
租户、用户不存在/禁用、租户不存在/禁用、角色缺失、继承成环、权限缺失均按
拒绝处理。令牌内含 `sub`（用户 ID）、`tid`（租户 ID）和用户名，但不内嵌
权限；每个请求实时回查用户、租户及角色图，使禁用和撤权立即生效。

批三十六已完成全部存量业务元数据 tenant 化：Agent、数据源、策略及列权限、
规则、脱敏与密钥、通知、审批、审计日志、管理审计 outbox、审计链和 B5
持久业务实体均具有 `tenant_id`。历史数据没有可信归属信号，因此迁移统一、
确定性地回填到 `tenant_default`，绝不推测其他租户。控制台从令牌 `tid` 绑定
仓储上下文：列表只返回当前租户行，详情及修改、删除他租户资源按 404 处理，
不泄露资源存在性；不存在或禁用租户仍由实时认证检查拒绝。RBAC 表本身继续
使用原有租户约束，平台运行表不增加业务租户所有权。

## 3. 权限与预置角色

| 权限 | 含义 |
|---|---|
| `datasource.manage` | Agent 与数据源管理 |
| `strategy.manage` | 策略、规则、脱敏、发现、通知管理 |
| `query.execute` | 查询评估/执行、审批和 B5 操作 |
| `audit.view` | 审计、仪表盘、事件流、审计链查看 |
| `user.manage` | 当前租户用户与用户角色管理 |
| `role.manage` | 当前租户角色、继承与权限管理 |
| `tenant.manage` | 租户管理；跨租户操作仅限引导管理员 |

默认租户预置：

- `admin`：全部权限；
- `auditor`：`audit.view`；
- `operator`：`datasource.manage`、`strategy.manage`、`query.execute`；
- `viewer`：`audit.view`。

多角色取权限并集；子角色继承父角色权限。任何未知角色或继承环都会使整次
授权失败，不会忽略异常后继续放行。

## 4. API

响应继续使用现有 `{code,msg,data}` 信封。列表主体为数组；密码哈希永不进入
响应。除登录外均需 Bearer 令牌。

| 方法与路径 | 权限 | 请求/响应要点 |
|---|---|---|
| `POST /api/v1/auth/login` | 公开 | 请求 `{tenant_id?,username,password}`；兼容保留 `{token,expires_at}`，并返回 `{refresh_token,refresh_expires_at}`；租户缺省为默认租户 |
| `POST /api/v1/auth/refresh` | 公开（refresh token 认证） | 请求 `{refresh_token}`；轮换 refresh token 并返回新的 access/refresh token；重放会撤销整条 refresh family |
| `GET /api/v1/auth/me` | 已认证 | 返回用户、租户、角色及实时权限 |
| `POST /api/v1/auth/logout` | 已认证 | 保持原兼容响应；持久化撤销当前 access jti 及其 refresh family，立即失效 |
| `GET/POST /api/v1/users` | `user.manage` | 列表或创建；创建密码至少 12 字符，可同时给 `role_ids` |
| `GET/PUT/DELETE /api/v1/users/{id}` | `user.manage` | 当前租户 CRUD；禁止删除当前用户 |
| `PUT /api/v1/users/{id}/roles` | `user.manage` | 请求 `{role_ids}`，整体替换 |
| `GET/POST /api/v1/roles` | `role.manage` | 列表或创建，支持权限及父角色 |
| `GET/PUT/DELETE /api/v1/roles/{id}` | `role.manage` | 角色 CRUD；内置角色不可删除 |
| `PUT /api/v1/roles/{id}/permissions` | `role.manage` | 请求 `{permissions}`，整体替换 |
| `GET /api/v1/permissions` | `role.manage` | 返回稳定权限目录 |
| `GET/POST /api/v1/tenants` | `tenant.manage` | 普通租户管理员只看自身；创建仅引导管理员 |
| `GET/PUT/DELETE /api/v1/tenants/{id}` | `tenant.manage` | 跨租户及删除仅引导管理员；默认租户不可删除 |

跨租户管理是唯一的显式系统边界：只有默认租户、固定引导用户且仍持有
`tenant.manage` 时可指定其他 `tenant_id`。仅角色名为 admin 不构成系统权限。

## 5. 兼容与初始化

首次启动在已有数据库迁移后执行幂等初始化：

1. 建立 `tenant_default`；
2. 建立四个预置角色及权限；
3. 使用现有 `AGENTSQL_ADMIN_USER/PASSWORD` 建立引导管理员；
4. 密码用 bcrypt 存储，明文不写日志、不写响应；
5. 给引导管理员绑定内置 `admin` 角色。

已有控制台路径不变，原管理员登录请求不传 `tenant_id` 时自动使用默认租户。
令牌签名仍复用既有密钥派生和严格 Bearer 解析，载荷增加身份与租户字段。
Agent API Key 认证不受影响。

## 6. MVP 与 OIDC 边界

本批实现本地账号、密码哈希、签名令牌、用户/角色/租户 CRUD、多角色、角色
继承、逐路由权限和租户隔离。全部能力对外开放，无 License 或版本门禁。

OIDC 在本批不实现，原因是仓库没有 issuer/client/redirect/claim mapping 配置
契约，也没有可用于真实联调的提供方。后续需实现：PKCE 授权码流、一次性
state/nonce 存储、issuer discovery/JWKS 校验、严格 redirect allow-list、
`(issuer,sub)` 唯一映射、显式租户与角色 claim 映射以及本地账号绑定/恢复
策略。OIDC 必须默认关闭；配置不完整或校验失败时不得回退为信任未验证 claim。

本批已交付管理端本地用户的服务端令牌撤销，以及短期 access + 长期 refresh token。refresh token 仅存 SHA-256 哈希、每次使用均轮换；重用已轮换或撤销的 token 会撤销整条 refresh family，store 状态不可读时 fail-closed。批三十六进一步交付全部存量业务元数据的租户所有权迁移，并让管理审计 outbox 与最终审计事件携带租户上下文。后续仍包括登录限速与锁定，以及 OIDC/LDAP/MFA 等上述身份能力。
