# AgentSQL

[![Release](https://img.shields.io/badge/Release-v0.1.0-blue)](#)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](go.mod)
[![License: AGPLv3](https://img.shields.io/badge/License-AGPLv3-blue)](LICENSE)
[![Commercial License](https://img.shields.io/badge/License-Commercial-orange)](COMMERCIAL-LICENSE.md)

AgentSQL 是面向 AI Agent 的数据库安全网关与生产级 MCP Server：让模型发出的每条 PostgreSQL/MySQL 请求在到达数据库前，经过身份认证、SQL 解析、授权与规则评估、受控执行、结果脱敏和审计。

```text
AI Agent → LLM / MCP Client → AgentSQL 网关 → PostgreSQL / MySQL
                                  │
                                  └─ 鉴权 · 授权 · 规则 · 审批 · 脱敏 · 审计
```

## 核心特性

- 双 MCP 承载：本机 `stdio` 与 Streamable HTTP `/mcp`，提供 7 个受控数据库工具。
- 默认拒绝的安全链路：API Key 认证、Agent 能力档位、对象/列授权、SQL AST 规则与 fail-closed 错误处理。
- 受控读写：只读保护、危险 SQL 拦截、Explain 风险评估、超时、连接/QPS/结果行数限制和人工审批。
- 基础数据保护：v0.1 支持手机号、邮箱按列确定性打码；数据源口令使用 32 字节 SECRET 加密保存。
- 可追溯运维：SQLite 元数据与审计库、审计导出、审批闭环、Prometheus 指标、健康/就绪探针。
- 内嵌 Web 控制台：总览、审计、演示台、Agent、数据源、权限、规则、审批和脱敏规则管理。

## 5 分钟快速开始：Docker Compose + SQLite

前置条件：Docker 与 Docker Compose；默认只启动 AgentSQL，元数据和审计写入命名卷 `agentsql-data` 中的 `/var/lib/agentsql/agentsql.db`，避免 Linux 宿主 bind mount 产生 root 权限文件。

1. 创建本地环境文件。

```bash
cp examples/docker/.env.example .env
```

2. 生成自己的 SECRET 与强管理员口令并填入 `.env`。不要提交 `.env` 或任何真实凭据。

Bash：

```bash
openssl rand -base64 24   # 填入 AGENTSQL_SECRET，输出恰好 32 个 ASCII 字节
openssl rand -base64 24   # 填入 AGENTSQL_ADMIN_PASSWORD
```

PowerShell（分别执行两次并填写两个变量）：

```powershell
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); [Convert]::ToBase64String($bytes); $rng.Dispose()
```

3. 启动并打开控制台。

```bash
docker compose up -d --build
docker compose ps
```

访问 <http://127.0.0.1:7780>，用 `.env` 中的 `AGENTSQL_ADMIN_USER` 和 `AGENTSQL_ADMIN_PASSWORD` 登录。Compose 只在宿主机回环地址暴露端口。

需要同时启动 Prometheus 与零手工配置的 Grafana 六面板时，可从仓库根目录一条命令起栈（实际部署请换成自行生成并保存的随机值）：

```bash
AGENTSQL_SECRET='N7vK2mQ9xR4tY8pL6cW3sD5fH1jB0zUa' \
AGENTSQL_ADMIN_PASSWORD='S9afe-Admin-Passphrase-2026' \
docker compose --profile observability up -d --build
```

完整说明见 [可观测性示例](examples/observability/README.md)。

> `AGENTSQL_SECRET` 必须与 `agentsql.db` 成对备份。直接更换 SECRET 会让既有数据源口令无法解密，不是无损轮换。

## 二进制方式

构建需要 Go 1.25、cgo、C 编译器和 glibc 兼容环境；普通构建直接使用仓库已有的内嵌控制台产物，不需要 Node.js。

```bash
make build VERSION=v0.1.0
./bin/agentsqlctl init-config -o config.yaml
export AGENTSQL_SECRET="$(openssl rand -base64 24)"
export AGENTSQL_ADMIN_USER='admin'
export AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 24)"
./bin/agentsql serve -c config.yaml
```

Windows PowerShell：

```powershell
./bin/agentsqlctl.exe init-config -o config.yaml
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); $env:AGENTSQL_SECRET = [Convert]::ToBase64String($bytes); $rng.Dispose()
$env:AGENTSQL_ADMIN_USER = 'admin'
$passwordBytes = [byte[]]::new(24); $passwordRng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $passwordRng.GetBytes($passwordBytes); $env:AGENTSQL_ADMIN_PASSWORD = [Convert]::ToBase64String($passwordBytes); $passwordRng.Dispose()
./bin/agentsql.exe serve -c config.yaml
```

`agentsqlctl check-config -c config.yaml` 只校验配置，不要求凭据；`agentsqlctl migrate -c config.yaml` 会校验 SECRET 后执行元数据库迁移。

## MCP 接入

先在控制台创建 Agent、配置数据源和授权策略，并保存只显示一次的 `asql_...` API Key。

### stdio

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/to/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<YOUR_32_BYTE_SECRET>",
        "AGENTSQL_API_KEY": "<YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

### Streamable HTTP

本地端点为 `http://127.0.0.1:7780/mcp`；非本机部署必须在前面配置 HTTPS 反向代理。

```json
{
  "mcpServers": {
    "agentsql-http": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

v0.1 的 MCP 工具包括 `list_datasources`、`list_schema`、`explain_query`、`query`、`execute_write`、`request_approval`、`get_approval_result`。

## 功能矩阵

| 能力 | v0.1.0 | 说明 |
| --- | --- | --- |
| MCP stdio / Streamable HTTP | 支持 | HTTP 使用 Bearer API Key；stdio 绑定单个 Agent Key |
| Agent 与数据源管理 | 支持 | API Key 仅创建/轮换时返回明文，库内保存哈希 |
| PostgreSQL / MySQL SQL 解析 | 支持 | 结构化 AST 判定，不依赖文本正则代替解析 |
| 表级与列级授权 | 支持 | 默认拒绝；特殊 JOIN/通配语义见“已知限制” |
| 只读、危险语句、限流与 Explain 风险规则 | 支持 | 四态结果：allow / deny / approve / warn |
| 受控查询与写入 | 支持 | 超时、连接上限、结果截断与错误脱敏 |
| 人工审批 | 支持 | 建单、管理员决定、Agent 查询结果 |
| 手机号/邮箱打码 | 支持 | v0.1 仅 `mask` 算法 |
| SQLite 审计、导出与仪表盘 | 支持 | `/metrics`、`/healthz`、`/readyz` 可用于运维 |
| Web 管理控制台 | 支持 | 可用 `console_enabled: false` 完全不挂载管理面 |
| 大屏实时事件流 | 支持 | SSE，默认开启，最多 100 条并发管理端连接 |

## 数据库兼容矩阵

| 用途 | 数据库 | v0.1.0 |
| --- | --- | --- |
| 被防护业务库 | MySQL 8 | 支持 |
| 被防护业务库 | PostgreSQL 14 / 15 / 16 / 17 / 18 | 支持 |
| 元数据与审计库 | SQLite | 唯一支持的 v0.1 后端 |
| 元数据与审计库 | PostgreSQL 18（兼容目标 15+） | v0.2 的 T28 路线，v0.1 不支持 |

## v0.1 已知限制

- 脱敏优先按最终结果列名匹配，并对位置可确定的顶层直接列引用按源裸列名兜底。函数/表达式/聚合/CAST、UNION、跨子查询/CTE/视图的内部重命名，以及多星号之间无法定位的投影槽不做 v0.1 血缘兜底。该能力不是完整 DLP；防绕行还需结合只读数据库账号、列级权限、安全视图与审批。
- 多表 JOIN 与自连接只做表级授权；v0.1 不推断投影列归属。两个表的表级授权通过后，不再按投影列归属收紧。
- `AllowedTables` 中的 `*` 或 `schema.*` 表示管理员显式授予匹配表的全部列；此时精确列白名单不再收紧。单表使用精确列白名单时，应显式列出投影列。
- MCP 不暴露跨请求会话或事务参数，内部 `SessionID` 不是公开协议能力。
- 多语句事务和受控的跨请求事务能力计划在 v0.2 提供。
- v0.1 的元数据/审计库只支持单节点 SQLite；PostgreSQL 元库属于 T28。

## 本地测试模式

`AGENTSQL_INSECURE=1` 永久只表示“允许公开测试凭据”：它可在本地开发/测试中放行长度正确的公开示例 SECRET 和非空弱管理员口令，但仍拒绝空 SECRET、非 32 字节 SECRET 与空管理员口令。它不会关闭认证、安全规则或 fail-closed 行为，生产环境不得设置。

## 截图

截图将放在 [`docs/screenshots/`](docs/screenshots/)；发布前可补充控制台总览、安全判定链路、审计与审批页面截图。

## 文档与社区

- [部署、升级、备份与 systemd](docs/DEPLOY.md)
- [产品与工程规范](docs/SPEC.md)
- [版本变更](CHANGELOG.md)
- [Issue（公开仓库链接占位）](#)
- [Discussions（社区链接占位）](#)
- [安全报告（私密报告渠道占位）](#)

请不要在公开 Issue 中提交真实密钥、口令、连接串或未修复漏洞细节。

## 构建与贡献

```bash
go vet ./...
go test -race -count=1 ./...
make build VERSION=v0.1.0
```

`pg_query_go` 要求 cgo；不要使用 `CGO_ENABLED=0` 或 Alpine/musl 构建。提交改动前请阅读 [SPEC](docs/SPEC.md)，为行为变化补测试，并保持 `tests/corpus` 决策语料不被无意改写。

## 许可、商业授权与商标

开源版本依据 [GNU AGPLv3](LICENSE) 授权。需要闭源集成、SaaS 商用、企业模块、保修或 SLA 时，请参阅 [商业授权说明](COMMERCIAL-LICENSE.md)。AgentSQL 名称与 Logo 的商标权保留；可以依许可证 fork 源码，但不得以 AgentSQL 名称或 Logo 冒充官方版本对外发行。
