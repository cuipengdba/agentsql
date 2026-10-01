> Source: [../GETTING_STARTED.md](../GETTING_STARTED.md)

# Getting Started with AgentSQL

> Release status: This document applies to AgentSQL v0.4.0. The one-command installer and the `ghcr.io/cuipengdba/agentsql:v0.4.0` image are available for deployment. You can also build and try AgentSQL locally by following the “Source / Live Demo” paths in this guide. The source repository is `github.com/cuipengdba/agentsql`.

AgentSQL is a database security gateway and production-grade MCP Server placed between AI Agents and business databases. It checks identity, permissions, rules, and decisions before executing each SQL statement, then executes it within an auditable boundary.

```text
人 → AI Agent / LLM → MCP → AgentSQL → PostgreSQL / MySQL 业务库
```

AgentSQL is not BI, an ORM, or Text2SQL. It does not convert natural language into SQL, nor does it replace database accounts and permission systems. A least-privilege runtime account on the database remains the final line of defense.

## What You Get

- An independent API Key and a `readonly`, `dml`, or `ddl` capability tier for each Agent.
- Default-deny authorization policies scoped by Agent × data source × object.
- Parsing, rule evaluation, controlled execution, result row limits, and basic data masking for each single SQL statement.
- An evidence trail and application-level SQL audit for `allow`, `deny`, `warn`, `approve`, and `error` decisions in the console.
- Seven fixed base tools for MCP clients over Streamable HTTP or stdio, plus eight tools for the PostgreSQL B5 session/planned-transaction feature that is enabled by default.

## Explicit Non-Goals

- AgentSQL does not replace database accounts, network isolation, TLS termination, backups, monitoring, or change management.
- It does not protect requests that bypass AgentSQL and connect directly to a database.
- PostgreSQL B2 column-level authorization and B5 cross-request logical sessions and planned transactions are enabled by default. MySQL does not enter the PostgreSQL B2 path and does not support B5 cross-request transactions. Each operation still accepts only one top-level SQL statement and remains subject to the preflight plan and session security boundaries.
- An approved request does not automatically execute SQL. The audit trail is not regulatory-grade WORM storage.
- Complete DLP, enterprise identity, high availability, and Kubernetes orchestration are not currently supported.

## Prerequisites and Compatibility Matrix

| Scope | Current support | Notes |
| --- | --- | --- |
| One-command installation hosts | `linux/amd64`, `linux/arm64`; glibc 2.28+, systemd | Native linux/arm64 glibc packages are available starting with v0.4.0; `x86_64` / `amd64` maps to amd64, and `aarch64` / `arm64` maps to arm64 |
| Other hosts | Docker / Docker Compose | The GHCR tag provides a `linux/amd64` + `linux/arm64` multi-arch manifest and selects the matching architecture automatically; native packages are not provided for musl / Alpine, CentOS 7, or architectures other than those two |
| Business databases | MySQL 8; PostgreSQL 14–18 | Create a dedicated, least-privilege runtime account for AgentSQL |
| Control-plane storage | SQLite; PostgreSQL 15+ | SQLite is the default; PostgreSQL can use separate metadata and audit databases |
| Browser console | `http://127.0.0.1:7780` | Binds to the loopback address by default |
| Local Live Demo | Docker Engine / Docker Desktop, Docker Compose v2 | Self-contained; no clone or build required; images are hosted on public GHCR |

## Path A: See It in Action in Five Minutes with Zero Configuration (One Command)

This dedicated Demo mode uses synthetic data and fixed demo identities. You do not need to clone the source, edit any configuration, or build images. All three images are hosted on public GHCR; the first startup pulls them and seeds fixed demo data automatically.

You only need one `docker-compose.yml`. Run the following in any empty directory:

```bash
# 1) 新建一个空目录并进入
mkdir agentsql-demo && cd agentsql-demo

# 2) 下载自包含 compose（仅这一个文件，约 2KB）
curl -fsSL -o docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/main/deploy/quickstart/docker-compose.yml

# 3) 一条命令拉起全部容器（postgres + mysql + seed + gateway）
docker compose up -d
```

Use these equivalent commands on Windows PowerShell:

```powershell
New-Item -ItemType Directory -Force agentsql-demo | Out-Null; Set-Location agentsql-demo
Invoke-WebRequest -OutFile docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/main/deploy/quickstart/docker-compose.yml
docker compose up -d
```

Wait about one to two minutes. After all four containers are ready, open <http://127.0.0.1:17880>. The demo credentials are prefilled on the sign-in page, so click “登录” (“Sign in”). You can also check progress in the terminal:

```bash
docker compose ps            # gateway 状态变为 healthy 即就绪
docker compose logs -f seed  # 看到 DEMO_SEED_OK 表示播种完成
```

In “演示台” (“Demo”), try the five scenario cards in order. Each card contains fixed, accurate SQL and runs it to show the real result:

1. **Column-level authorization + data masking**: allows a sealed JOIN query and masks the returned `phone` and `email` columns.
2. **Missing column authorization**: denies a query for an unauthorized column before it reaches the business database.
3. **Unauthorized object access**: explicitly denies access to `internal_notes` without returning table contents.
4. **Unauthorized DDL**: blocks `DROP TABLE`.
5. **Write routed to human approval**: does not execute an `UPDATE` directly and returns `approve` for human review.

When you finish, remove the containers and demo data volumes completely:

```bash
docker compose down -v
```

> Demo note: All credentials are fixed demo values stored in the Compose file. Use them only for local evaluation, and never connect this setup to real data or databases. The Demo site binds only to the loopback address `127.0.0.1` by default and is not exposed to the LAN.

## Path B: Connect Your Own Database (Real Production Flow)

Complete the following steps in order. Creating a data source and an Agent is not enough to access a table until an authorization policy exists.

### 1. Install and Start

**Native linux/arm64 glibc packages are available starting with v0.4.0:** the one-command installer supports `linux/amd64` (`uname -m` is `x86_64` / `amd64`) and `linux/arm64` (`aarch64` / `arm64`). Both require glibc 2.28+, systemd, and root privileges:

```bash
# 适用前提：v0.4.0 起；linux/amd64 或 linux/arm64 + glibc 2.28+ + systemd
curl -fsSL https://github.com/cuipengdba/agentsql/releases/latest/download/install.sh | sudo sh -s -- install
```

Instead of the one-command installer, you can build from source or use Docker Compose as described in the [Deployment Guide](../DEPLOY.md). You can also run `docker pull ghcr.io/cuipengdba/agentsql:v0.4.0` to pull the multi-arch image that automatically matches `linux/amd64` or `linux/arm64`. The GitHub Releases page is authoritative for release assets.

### 2. Sign In to the Console

Open <http://127.0.0.1:7780>. The default administrator username is `admin`. A non-interactive installation does not print the random password to the terminal. The root user can read `/etc/agentsql/agentsql.env`, or you can reinstall with `--show-password` explicitly.

### 3. Add a Data Source

First create a dedicated, least-privilege AgentSQL runtime account in the business database. Never use a `superuser`, the database owner, or MySQL `root`. In “数据源” (“Data Sources”), enter `host`, `port`, `database`, `username`, and `password`; grant only the table permissions required for actual queries; then click “测试连接” (“Test Connection”).

The current data source form has no fields for SSL mode, a CA, client certificates, or extra DSN parameters. If a remote connection requires TLS, terminate it within a controlled network or proxy layer and verify the end-to-end configuration.

### 4. Create an Agent

Create an identity in “Agent” and select its capability tier:

- `readonly`: read-only queries.
- `dml`: queries plus `INSERT`, `UPDATE`, and `DELETE`.
- `ddl`: includes schema-changing operations.

The API Key is shown only once after creation. Put it into the client’s secret store immediately; do not write it to a repository or chat history.

### 5. Create a Least-Privilege Allow Policy

> **Critical step: AgentSQL denies access when no authorization exists. Creating only a data source and an Agent still does not grant access to any table.**

Under “权限” (“Permissions”), create an `allow` policy binding the Agent to the data source. Start with a single table that the Agent genuinely needs, such as `public.customers` or `customers`. Do not use `*` or `schema.*` in an ordinary example: although valid, both grant all columns of matching tables and make a column allowlist ineffective.

### 6. Connect an MCP Client

When the installer or systemd already runs the service, prefer Streamable HTTP. Merge the following minimal skeleton into the client configuration as described in the [MCP Integration Guide](INTEGRATIONS.md). Use the client’s official MCP documentation for its exact configuration path and HTTP transport field names:

```json
{
  "mcpServers": {
    "agentsql": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <Agent API Key>"
      }
    }
  }
}
```

After restarting or reloading the client, call `list_datasources` and `list_schema` first. Then use `query` to submit one authorized, single `SELECT` with a `WHERE` clause:

```sql
-- 适用前提：替换为已授权的真实表和列；通过 MCP query 工具提交
SELECT id, name FROM public.customers WHERE id = 1 LIMIT 10;
```

Confirm that an `allow` record appears under “审计” (“Audit”). Next, use a `readonly` Agent to send an update without a `WHERE` clause through `execute_write`, and provide a `reason`:

```sql
-- 适用前提：仅用于验证拦截；通过 MCP execute_write 工具提交并填写 reason
UPDATE public.customers SET name = 'blocked-test';
```

R002 (unconditional bulk write) and/or R003 (a write from a read-only Agent) should deny the request before it reaches the business database. Confirm the `deny` record on the Audit page. A tool-level preflight rejection can occur before entering the pipeline, so errors such as a missing `reason` are not guaranteed to create an audit record. Always provide `reason` during acceptance testing.

## Five-Minute Acceptance Checklist

- `GET /healthz` returns HTTP 200, with `status` set to `ok` and a `version` field in the JSON response.
- `GET /readyz` returns HTTP 200.
- The console’s “审计” (“Audit”) page contains at least one `allow` and one `deny` record.
- The `deny` details show the matched rule, and the target business table was not updated.
- The API Key, database password, and `AGENTSQL_SECRET` do not appear in shell history, the repository, or public logs.

```bash
# 适用前提：AgentSQL 已在本机 127.0.0.1:7780 运行
curl -fsS http://127.0.0.1:7780/healthz
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:7780/readyz
```

## Next Steps

- [User Guide](../USER_GUIDE.md): learn each console feature and its boundaries.
- [Sensitive Column Discovery Guide](../DISCOVERY.md): explicit table scope, sampling safety, confidence, and masking-draft boundaries.
- [MCP Integration Guide](INTEGRATIONS.md): two transports, seven base tools, B5 session/transaction tools, call sequences, and client configuration.
- [Deployment Guide](../DEPLOY.md): source, Compose, systemd, upgrades, backups, and control-plane migration.
- [Local Live Demo](../DEMO.md): full demo credentials, reset scripts, and security checklist.
