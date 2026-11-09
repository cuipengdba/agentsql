# AgentSQL

**Language:** [Simplified Chinese](README.md) | [English](README.en.md)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/agentsql-wordmark-dark.svg">
  <img src="brand/agentsql-wordmark-light.svg" width="360" height="80" alt="AgentSQL">
</picture>

![Version v4.10.0](https://img.shields.io/badge/Version-v4.10.0-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)

**AgentSQL = an independent AI data Q&A agent plus an MCP Server database security gateway.** Users can ask natural-language questions in AgentSQL's own UI; other AI agents can use AgentSQL as an independent MCP Server. Both entry points share current identity and grants, controlled SQL execution, result masking, and durable audit. AgentSQL native Q&A does not depend on SQLBot. SQLBot is an optional client through a fixed-version adapter.

[Website](https://agentsql.cn/en/) · [Chinese Q&A screenshots and concept images](https://agentsql.cn/en/#qa-gallery) · [v4.10.0 downloads and release scope](https://github.com/cuipengdba/agentsql/releases/tag/v4.10.0) · [Three usage modes](docs/en/PRODUCT_MODES.md) · [Documentation index](docs/en/README.md)

## Choose a usage mode

| Mode | What you do | Start here |
| --- | --- | --- |
| **Native Q&A** | Sign in to AgentSQL's own UI, choose a bound datasource, and review generated SQL, results, and audit | [Q&A operation and setup](docs/en/PRODUCT_MODES.md#mode-one-agentsql-native-qa-only) |
| **Independent MCP gateway** | Give an external AI agent an Agent API Key and let MCP tools query within its grants | [MCP Server operation](docs/en/PRODUCT_MODES.md#mode-two-agentsql-mcp-server-security-gateway-only) |
| **Both together** | Employees use AgentSQL Q&A and external agents use MCP; grant each path separately and share the security audit | [Combined use](docs/en/PRODUCT_MODES.md#mode-three-native-qa-and-mcp-server-together) |

```text
AgentSQL native UI → Go Q&A agent ───────┐
                                       ├→ AgentSQL security gateway → currently authorized database
External AI agent → AgentSQL MCP Server ┘
```

## v4.10.0 capabilities and limits

| Capability | Current status | Key limit |
| --- | --- | --- |
| Native AI data Q&A | **Verified bounded path** | Native Go/Web Q&A with local models, operator-registered PostgreSQL gateway aliases, and current grants; **no SQLBot runtime** |
| Authorized vector prewarm and Q&A | **Verified bounded load** | Fixed ONNX, local Qwen3-4B, real PostgreSQL, 128 synthetic authorized tables, and four single-table query types were exercised. The four-question batch took about 3 minutes 34 seconds including cold indexing and inference, not a production p95 or SLA |
| Private datasource binding rotation | **Verified bounded path** | Operators preflight a new gateway binding and current grants, then commit against the old binding ID and store revision; the browser cannot change targets or credentials |
| MCP Server security gateway | **Independently usable** | stdio, Streamable HTTP, and independent stateless HTTP; an Agent Key is not an administrator token, and each request follows current grants |
| Basic assistant and Type 4 page embed | **Verified bounded path** | One-source basic guest assistant and specified page embed; origin, identity, and revocation controls remain in force |
| SQLBot integration | **Optional fixed baseline** | A verified adapter can route specified SQLBot execution through AgentSQL MCP; no blanket zero-change integration claim |
| Type 1 advanced assistant | **Inherited v4.7 bounds** | An operator-registered external directory maps users to currently authorized gateway aliases. The exact v4.9 signed package did not rerun this path, so its support claim is not expanded. |
| Active data permissions | **Verified bounded shape** | Explicit current workspace users; built-in administrator/member role qualifiers; nonempty whitelist metadata never exempts row or column filters |
| Broader compatibility | **Some read-only loading; other paths unverified** | Community management-page lists loading does not validate create/edit/import/export. Real cloud inference, arbitrary role expansion, and direct original-policy DB migration remain unverified. |

Review generated SQL and business meaning before relying on a natural-language answer. v4.10.0 **has not been validated in the user's real business system**; it does not claim 100% compatibility or zero-change cutover. The [official release notes](https://github.com/cuipengdba/agentsql/releases/tag/v4.10.0) identify the exact commit, acceptance cases, performance measurements, and limits. Under the [fixed 25-scenario scoring contract](docs/en/sqlbot-community-chatbi-score-v1103.md), three scenarios passed the full strict acceptance bar; unverified scenarios score zero, and this is not total code-feature coverage. The public Demo's Q&A model is a **simulation**.

## Quick start

Linux `amd64` / `arm64` native packages require glibc ≥ 2.28 and systemd as PID 1. Verify the official package signature and hashes against the [release notes](https://github.com/cuipengdba/agentsql/releases/tag/v4.10.0) first. This command installs the gateway; native Q&A also requires the Q&A component, a local text model, and personal datasource bindings as described in [three usage modes](docs/en/PRODUCT_MODES.md).

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v4.10.0/install.sh | sudo sh -s -- install --version v4.10.0
```

Docker uses the official multi-architecture image. The quickstart script binds only to loopback and creates a persistent volume:

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/v4.10.0/scripts/quickstart.sh -o quickstart.sh && sh quickstart.sh --version v4.10.0
```

The gateway console defaults to `http://127.0.0.1:7780`; after the Q&A component is installed, its native UI defaults to `http://127.0.0.1:8000`. The installer generates a persistent administrator password. Non-interactive logs do not display it; root can inspect `/etc/agentsql/agentsql.env`. For remote access, use an SSH tunnel or controlled HTTPS reverse proxy rather than exposing the management port. See the [deployment guide](docs/en/DEPLOY.md) for offline installation, keys, backups, and upgrades.

## Find the right guide

| Task | Document |
| --- | --- |
| Three product modes and acceptance steps | [Three usage modes](docs/en/PRODUCT_MODES.md) |
| Native Q&A model and personal binding | [Native Q&A configuration](docs/en/CHATBI.md) |
| MCP clients, protocols, and tools | [MCP integration guide](docs/en/INTEGRATIONS.md) |
| Console grants, rules, masking, and audit | [User guide](docs/en/USER_GUIDE.md) · [Security boundaries](SECURITY.en.md) |
| Fixed-version SQLBot adapter | [SQLBot integration](docs/en/SQLBOT.md) · [Type 1 advanced assistant deployment](docs/en/v4.6.0-design.md#production-deployment-boundary) |
| Database capability tiers and evidence | [Website compatibility matrix](https://agentsql.cn/en/#compatibility) · [Database research and acceptance](docs/en/ecosystem-db-compat-research.md) |
| Offline install, upgrade, backup, and recovery | [Deployment guide](docs/en/DEPLOY.md) · [Operations guide](docs/en/OPERATIONS.md) |

“Registered,” “connection level,” “specified-version controlled read,” and “specified-scenario complete protection” are different database capability tiers. The website matrix retains per-database versions, topologies, and acceptance boundaries. A successful connection does not establish safe Q&A or vendor certification.

## Community and project

[Historical Releases](https://github.com/cuipengdba/agentsql/releases) · [CHANGELOG](CHANGELOG.en.md) · [Roadmap](ROADMAP.en.md) · [Issues](https://github.com/cuipengdba/agentsql/issues) · [Discussions](https://github.com/cuipengdba/agentsql/discussions) · [Contributing](CONTRIBUTING.en.md)

Current source is released under [Apache-2.0](LICENSE) with a [NOTICE](NOTICE). The license does not grant rights to the AgentSQL name or logo. Report unfixed vulnerabilities privately through [SECURITY.en.md](SECURITY.en.md); do not put real credentials or connection strings in public issues.
