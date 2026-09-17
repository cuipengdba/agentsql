# AgentSQL v0.2.0

> 面向 AI Agent 的数据库安全网关 / 生产级 MCP Server。让模型发出的每一条 PostgreSQL/MySQL 请求，在到达数据库前先过一道「鉴权 → 解析 → 授权 → 规则 → 审批 → 执行 → 脱敏 → 审计」的安全闸。
>
> 中文品牌名：智盾（控制台显示为「AgentSQL 智盾控制台」）。

```text
AI Agent → LLM / MCP Client → AgentSQL 网关 → PostgreSQL / MySQL
                                  │
                                  └─ 鉴权 · 授权 · 规则 · 审批 · 脱敏 · 审计
```

v0.2.0 在 v0.1 的安全网关核心之上，补齐了三件让产品「可在线体验、可生产管控、可实时观测」的能力：**自托管 Live Demo、控制台实时安全大屏、PostgreSQL 18 控制面与独立审计库**。

---

## 本版亮点

### 1. 一键自托管 Live Demo：六个真实受控剧本（T26）

一条命令拉起只含合成数据的 PostgreSQL/MySQL 演示库与网关（端口仅绑回环、数据每日重置、绝不连接真实数据）。演示台提供两种模式：

- **静态评估**：只做 SQL 解析与静态规则安检，不连库、不执行、不写审计，零风险；
- **真实试运行（Live）**：通过仅在 demo 模式注册的 `POST /api/v1/playground/run`，复用与生产完全相同的八段安检管线，内置六个剧本，一眼看懂网关到底拦了什么、放了什么：
  1. 正常放行（真实返回 5 行，带回 audit_id）；
  2. 无 WHERE 全表更新 → 触达数据库前硬拦截，不显示任何数据库底层报错；
  3. phone/email 列结果层脱敏（`138****0001`、`u***@example.test`）；
  4. 大结果集扫描 → R005 告警、预估 600 行、按上限截断返回 20 行；
  5. 越权访问敏感表 `internal_notes` → R010 拒绝、不返回任何内容；
  6. 用 audit_id 一键跳转审计证据链，或到总览大屏看实时流。

Live 通道的演示密钥只保存在服务端、不下发浏览器；非 SELECT 语句由结构性硬屏障在触库前拦截，且不可被规则配置覆盖；数据源白名单、严格字段校验、demo 限流（QPS=2）与不含密钥/DSN 的白名单响应共同保证演示环境安全。

> 六张真实运行截图随仓库提供，见 README 与 `docs/images/demo-scenario-1.png` … `demo-scenario-6.png`。

### 2. 控制台实时安全大屏（T27）

总览大屏新增经 Bearer 鉴权的 **SSE 实时事件流**（`GET /api/v1/stream`），放行/告警/待审批/拦截事件与拦截趋势实时上屏；网络中断时自动降级为原有定时刷新，不影响可用性。默认开启，管理端最多 100 条并发连接。单向推送选用 SSE 而非 WebSocket，更简单也更易过反向代理。

### 3. PostgreSQL 18 控制面与独立审计库（T28）

- 元数据库与审计库在原有 SQLite 之外，新增 **PostgreSQL 支持（兼容 15+，基准与 CI 为 PostgreSQL 18）**，开源版与企业版均可用；
- 支持 metadata 与 audit 放入**两个独立数据库**，审计库保留不可变审计 ID 与 `approvals.audit_id` 的应用级关联，写操作走审计先行（audit-first）的提交屏障；
- 生产部署采用一次性 migration 账号 + 最小权限运行账号 + `store.auto_migrate:false`；
- 新增 `agentsqlctl migrate-sqlite-to-postgres`，把默认 SQLite 控制面迁移到 PostgreSQL（单库或独立双库），带校验与序列对齐。

SQLite 仍是默认零配置控制面，原有起手路径完全不变。

### 4. 结果值渲染修复

修复 PostgreSQL `numeric/decimal` 在结果表被渲染成结构体文本（如 `{2274 -2 false finite true}`）的问题，统一显示为定点十进制 `22.74`（基于 `big.Int` 与指数，**不经过 float64**，保留尾随零）；interval、jsonb、uuid、数组、枚举、bytea 等结构化类型也以人类可读形式返回。MySQL decimal 行为不变。

---

## 5 分钟体验

**Live Demo（合成数据，最直观）：**

```bash
cp examples/docker/demo.env.example demo/demo.env   # 按提示替换全部占位凭据
bash ./demo/reset.sh                                 # Windows 用 .\demo\reset.ps1
# 打开 http://127.0.0.1:17880
```

**生产形态（Docker Compose + SQLite，零配置起步）：**

```bash
cp examples/docker/.env.example .env                 # 填入自行生成的 32 字节 SECRET 与管理员口令
docker compose up -d --build
# 打开 http://127.0.0.1:7780
```

完整部署、PostgreSQL 控制面、systemd、备份与升级见 [`docs/DEPLOY.md`](docs/DEPLOY.md)，Live Demo 说明见 [`docs/DEMO.md`](docs/DEMO.md)。

## MCP 接入

同时支持本机 **stdio** 与 **Streamable HTTP（`/mcp`，Bearer API Key）** 两种承载，提供 7 个受控数据库工具：`list_datasources`、`list_schema`、`explain_query`、`query`、`execute_write`、`request_approval`、`get_approval_result`。配置片段见 README「MCP 接入」。

## 从 v0.1 升级

- SQLite 用户：替换二进制/镜像即可，控制面自动迁移，**升级前请成对备份 `agentsql.db` 与 `AGENTSQL_SECRET`**（更换 SECRET 会使既有数据源口令无法解密，非无损轮换）。
- 想用 PostgreSQL 控制面：先备份，再用 `agentsqlctl migrate-sqlite-to-postgres`，按 `docs/DEPLOY.md` 配置一次性迁移账号与最小权限运行账号。
- 被防护业务库支持范围不变：MySQL 8 与 PostgreSQL 14/15/16/17/18。

## 安全边界与已知限制（请先阅读）

AgentSQL 是**网关层**的安全管控，不是银弹，本版明确不承诺以下能力：

- 最小权限只约束**经过网关的数据库账号**；不阻止持有 owner/superuser 的人绕过网关直连。
- 脱敏是按列规则的**结果层打码**，不是完整 DLP；函数/表达式/聚合、UNION、跨子查询/CTE/视图内部重命名不做完整血缘兜底，需配合只读账号、列级权限、安全视图与审批。
- JOIN/自连接只做表级授权，不推断投影列归属；`*`/`schema.*` 表示授予整表全部列。
- 审计为应用层不可变，**不是法规级 WORM**，也不防 DBA 直接改库；等保报告、WORM、SIEM、行级安全、SSO/LDAP/MFA、多租户等属企业版路线。
- MCP 不暴露跨请求会话/事务参数。

## 企业版路线（Roadmap）

- **T29** 国产/商业业务库矩阵：达梦、金仓 KingbaseES、瀚高 HighGo、GaussDB、OceanBase、TiDB、Oracle、SQL Server；
- **T30** 合规与身份管控：等保报告、WORM、SIEM、敏感发现、SSO/LDAP/MFA、多租户、RBAC、会签工单；
- **T31** 高可用与集中管控：K8s Operator、跨实例审计总线、UEBA、私有化交付与 SLA。

## 许可与商业授权

开源版本采用 **GNU AGPLv3**；闭源集成分发、对外 SaaS/托管且不希望按网络条款开源、需要企业模块或 SLA/保修的场景，需要商业授权。AgentSQL 名称与 Logo 商标保留，fork 不得冒充官方版本。详见 [LICENSE](LICENSE) 与 [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md)。

商业授权、企业版与私有化合作：**邮箱 87326549@qq.com ｜ 官网 https://agentsql.cn**

## 资产校验和

发布时在此附上 `agentsql-v0.2.0.tar.gz` / `.zip` 的 SHA-256（随 GitHub Release 资产一并提供）。

---

**相关链接**：[README](README.md) · [部署指南](docs/DEPLOY.md) · [Live Demo 指南](docs/DEMO.md) · [架构与工程规格](docs/SPEC.md) · [变更记录](CHANGELOG.md)
