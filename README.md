# AgentSQL（智盾）

AgentSQL 是面向 AI Agent 的数据库安全网关与生产级 MCP Server，让模型访问 PostgreSQL/MySQL 的每次请求都经过鉴权、解析、规则评估、受控执行、脱敏和审计。

## Docker Compose 快速开始（推荐）

1. 创建环境文件，并把其中的密钥和管理员密码替换为生产值。`AGENTSQL_SECRET` 必须恰好 32 字节。

```bash
cp examples/docker/.env.example .env
```

2. 构建并启动 AgentSQL。默认不会启动可选演示数据库。

```bash
docker compose up -d --build
```

3. 浏览器打开 <http://127.0.0.1:7780>，使用 `.env` 中的 `AGENTSQL_ADMIN_USER` 和 `AGENTSQL_ADMIN_PASSWORD` 登录。

需要 PostgreSQL 16 与 MySQL 8 演示库时运行：

```bash
docker compose --profile demo up -d --build
```

## 本机二进制快速开始

构建需要 Go 1.25、cgo、C 编译器和 glibc 兼容环境。仓库已经包含控制台构建产物，普通构建不需要 Node.js；前端源码改变后可手动执行 `make webui`。

```bash
make build VERSION=v0.1
./bin/agentsqlctl init-config -o config.yaml
export AGENTSQL_SECRET='0123456789abcdef0123456789abcdef'
export AGENTSQL_ADMIN_USER='admin'
export AGENTSQL_ADMIN_PASSWORD='CHANGE_ME_AgentSQL_Admin_2026!'
./bin/agentsql serve -c config.yaml
```

示例密钥仅用于说明 32 字节长度，生产部署必须替换并妥善备份。

## MCP 客户端配置

- [Claude Desktop stdio](examples/mcp/claude_desktop_config.json)
- [Cursor stdio](examples/mcp/cursor_mcp.json)
- [Streamable HTTP](examples/mcp/streamable_http_mcp.json)

## 文档

- [部署、升级、备份与 systemd](docs/DEPLOY.md)
- [产品与工程规范](docs/SPEC.md)

## 开发约束

`docs/SPEC.md` 是最高权威。AgentSQL 的 PostgreSQL parser 使用 `pg_query_go`，必须启用 cgo；不要使用 `CGO_ENABLED=0` 或 Alpine/musl 构建。
