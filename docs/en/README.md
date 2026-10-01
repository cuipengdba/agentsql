> Source: [../README.md](../../README.md)

> Documentation navigation: start with the [root English README](../../README.en.md), then follow [Quick Start](QUICKSTART.md), [Administration](ADMIN.md), [MCP Integration](INTEGRATIONS.md), and [Security](SECURITY.md). The root English README carries the complete status-qualified database ecosystem matrix; this page is the earlier long-form translation of the Chinese README.

See the [English documentation coverage inventory](COVERAGE.md) for the complete `docs/` audit, source-to-English mapping, remaining specialized gaps, and known source discrepancies.

# AgentSQL

![Release](https://img.shields.io/badge/Release-v0.4.0-blue)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](../../go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](../../LICENSE)
[![Commercial License](https://img.shields.io/badge/License-Commercial-orange)](../../COMMERCIAL-LICENSE.md)

Ecosystem listing: [Glama](https://glama.ai/mcp/servers?query=AgentSQL)

AgentSQL is listed in the official MCP Registry and can be found by searching for AgentSQL there.

AgentSQL (Chinese brand name: 智盾, shown in the console as “AgentSQL 智盾控制台” / “AgentSQL Zhìdùn Console”) is a database security gateway and production-grade MCP Server for AI Agents. Before any PostgreSQL/MySQL request from a model reaches the database, it passes through authentication, SQL parsing, authorization and rule evaluation, controlled execution, result data masking, and SQL audit.

```text
AI Agent -> LLM / MCP Client -> AgentSQL gateway -> PostgreSQL / MySQL
                                      |
                                      +-- auth, policy, rules, approval, masking, audit
```

## Core Features

- Dual MCP transports: local `stdio` and Streamable HTTP `/mcp`, with seven base database tools. The PostgreSQL B5 session/planned-transaction feature, enabled by default, adds eight tools.
- Default-deny security path: API Key authentication, Agent capability tiers, object/column authorization, SQL AST rules, and fail-closed error handling.
- Controlled reads and writes: read-only protection, dangerous SQL blocking, Explain risk evaluation, timeouts, connection/QPS/result-row limits, and human approval.
- Basic data protection: column-level data masking provides four algorithms—partial `mask` for six types; keyed HMAC `hash` fingerprints for all nine types; keyless `block`, which replaces every non-null value with `***`; and keyless `range`, available only for `number` / `date`, which preserves coarse distributions through numeric buckets or date truncation. Data source passwords are stored encrypted with a 32-byte `AGENTSQL_SECRET`.
- Traceable operations: zero-configuration SQLite by default; optional PostgreSQL 15+ control plane (PostgreSQL 18 baseline, with metadata and audit optionally separated); SQL audit exports as machine-readable JSONL or Chinese CSV that opens directly in Excel/WPS and protects against formula injection; approval decision workflows; Prometheus metrics; and health/readiness probes.
- Security-event notifications: filter by decision and forward events out-of-band through Webhook or Syslog. Notifications are disabled by default and omit SQL by default; delivery failures do not affect SQL audit or SQL decisions.
- Embedded web console: overview, audit, Demo, Agent, data source, permission, rule, approval, and masking-rule management.

## Ecosystem and Compatibility

AgentSQL is a database-neutral security gateway. It currently supports PostgreSQL and MySQL natively and is planning compatibility work and ecosystem collaboration for major Chinese database products.

电科金仓 KingbaseES · 瀚高 HighGo · openGauss（GaussDB）· IvorySQL · TiDB · OceanBase · TDSQL · OpenTenBase · 崖山数据库 YashanDB · 达梦数据库 DM

> This list describes planned ecosystem collaboration and compatibility evaluation; it does not mean the current release supports these databases. Database vendors are welcome to contact us about compatibility certification and collaboration. To request compatibility certification or a joint case study for your database, contact us through the WeChat community groups or the repository Discussion area.

## Five-Minute Quick Start

All three paths below listen only on the host loopback address by default. The automatic installer creates a persistent **administrator password**, not a one-time password. For remote access, use SSH local forwarding such as `ssh -L 7780:127.0.0.1:7780 user@server`; never expose port 7780 directly to the public internet.

### 1. One-Line Installation on Bare-Metal Linux (Recommended)

Starting with v0.4.0, native glibc packages are available for `linux/amd64` and `linux/arm64`. Use the amd64 package when `uname -m` is `x86_64` / `amd64`, and the arm64 package when it is `aarch64` / `arm64`. Both require glibc 2.28+ and systemd as PID 1. The installer selects the local architecture automatically, verifies both outer and in-package hashes, and installs AgentSQL as a systemd service. musl / Alpine and CentOS 7 remain unsupported:

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/latest/download/install.sh | sudo sh -s -- install
```

Non-TTY output in pipelines and CI does not show the generated administrator password. The root user can read it from `/etc/agentsql/agentsql.env`. An interactive terminal displays it once when credentials are first created; you can also add `--show-password` explicitly. In an offline environment, obtain the tarball and its same-named `.sha256` file, then verify, extract, and install from the package root:

The following uses an existing amd64 asset as an example. Starting with v0.4.0, arm64 hosts use the corresponding `-linux-arm64` asset name.

```bash
sha256sum -c agentsql-v0.4.0-linux-amd64.tar.gz.sha256
tar -xzf agentsql-v0.4.0-linux-amd64.tar.gz
cd agentsql-v0.4.0-linux-amd64
sudo ./install.sh install
```

### 2. Start with One Docker Command

By default, the script pulls the exact GHCR tag for the latest stable GitHub Release. That tag is a `linux/amd64` + `linux/arm64` multi-arch image, and Docker automatically selects the matching architecture. The script creates a `.env` file with `0600` permissions, uses the `agentsql-data` named volume, and binds to the loopback address:

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/main/scripts/quickstart.sh -o quickstart.sh && sh quickstart.sh
```

The publisher must make the GHCR package public for anonymous pulls to work. You can also pull it directly; Docker selects `linux/amd64` or `linux/arm64` from the manifest list automatically:

```bash
docker pull ghcr.io/cuipengdba/agentsql:v0.4.0
```

The equivalent single `docker run` command follows. Random values are passed only through the current shell environment and are not written into command-line arguments:

```bash
export AGENTSQL_SECRET="$(openssl rand -base64 24)" AGENTSQL_ADMIN_USER=admin AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 18)"
docker run -d --name agentsql --restart unless-stopped --security-opt no-new-privileges:true -p 127.0.0.1:7780:7780 -e AGENTSQL_SECRET -e AGENTSQL_ADMIN_USER -e AGENTSQL_ADMIN_PASSWORD -v agentsql-data:/var/lib/agentsql ghcr.io/cuipengdba/agentsql:v0.4.0
```

### 3. Local Live Demo (One Command, No Source Required)

This self-contained environment connects only to synthetic PostgreSQL/MySQL demo data and never to a real database. There is no need to clone the source, edit configuration, or build an image. Download one Compose file and start it:

```bash
mkdir agentsql-demo && cd agentsql-demo
curl -fsSL -o docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/main/deploy/quickstart/docker-compose.yml
docker compose up -d
```

Wait about one to two minutes. When all four containers are ready, open <http://127.0.0.1:17880>. The sign-in page has prefilled demo credentials; click “登录” (“Sign in”). “演示台” (“Demo”) provides five real scenarios: column-level authorization + data masking, missing column authorization, unauthorized object access, unauthorized DDL, and routing a write to human approval. To clean up completely:

```bash
docker compose down -v
```

All Demo credentials are fixed demo values stored in the Compose file. Use them only for local evaluation, and never connect this setup to real data. See [Getting Started](GETTING_STARTED.md) and the [Live Demo Guide](../DEMO.md) for details.

## MCP Integration

First create an Agent in the console, configure a data source and authorization policies, and save the `asql_...` API Key, which is shown only once.

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

The local endpoint is `http://127.0.0.1:7780/mcp`. A non-local deployment must place an HTTPS reverse proxy in front of it.

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

The gateway always provides seven base MCP tools: `list_datasources`, `list_schema`, `explain_query`, `query`, `execute_write`, `request_approval`, and `get_approval_result`. PostgreSQL B5 sessions/planned transactions, enabled by default in v0.4.0, also register eight tools: `open_session`, `close_session`, `get_session_status`, `begin_transaction`, `execute_transaction_statement`, `commit_transaction`, `rollback_transaction`, and `get_transaction_status`. If `mcp.sessions.enabled` is explicitly disabled, only the base tools remain.

## Documentation and Community

- [Getting Started](GETTING_STARTED.md)
- [User Guide (Chinese)](../USER_GUIDE.md)
- [MCP Integration Guide](INTEGRATIONS.md)
- [Sensitive Column Discovery and Masking Draft (Chinese)](../DISCOVERY.md)
- [Webhook / Syslog Notifications and Security Configuration (Chinese)](../NOTIFICATIONS.md)
- [Deployment, PostgreSQL Control Plane, Upgrades, Backups, and systemd (Chinese)](../DEPLOY.md)
- [Local Live Demo, Scheduled Reset, and Public-Deployment Security Checklist (Chinese)](../DEMO.md)
- [Product and Engineering Specification (Chinese)](../SPEC.md)
- [Security Policy and Private Vulnerability Reporting (Chinese)](../../SECURITY.md)
- [Changelog (Chinese)](../../CHANGELOG.md)
- [Issue Tracker](https://github.com/cuipengdba/agentsql/issues)
- [Commercial Licensing and Enterprise Edition Collaboration (Chinese)](../../COMMERCIAL-LICENSE.md)

Do not submit real keys, passwords, connection strings, or details of an unpatched vulnerability in a public Issue. Report unpatched vulnerabilities privately as described in [SECURITY.md](../../SECURITY.md).

### Join the Community on WeChat (微信)

| 交流群 #2 (Community Group #2) | 交流群 #3 (Community Group #3) | 公众号 (Official Account) | 个人微信 (Personal WeChat) |
| --- | --- | --- | --- |
| ![PG x AgentSQL 交流群 #2](../../website/public/assets/community/wechat-group-2.png) | ![PG x AgentSQL 交流群 #3](../../website/public/assets/community/wechat-group-3.png) | ![CP 的 PostgreSQL 厨房](../../website/public/assets/community/wechat-official-account.png) | ![崔鹏 个人微信](../../website/public/assets/community/wechat-personal.png) |
| 扫码进群 (scan to join) | 扫码进群 (scan to join) | 扫码关注 (scan to follow) | 扫码加好友 (scan to add) |

> **交流群 #1 (Community Group #1) has more than 200 members and cannot be joined by scanning a QR code.** Add the author’s personal WeChat account (崔鹏 / Cui Peng) first, include “AgentSQL” in the note, and ask the group owner for an invitation.
> Group QR codes are valid for seven days. If one expires, add the personal WeChat account or follow 公众号 “CP 的 PostgreSQL 厨房” (the “CP’s PostgreSQL Kitchen” official account) for the latest way to join.
