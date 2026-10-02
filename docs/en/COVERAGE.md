# English Documentation Coverage Inventory

This inventory records the Markdown documentation present under `docs/` after the v0.5 English-documentation pass. Image files under `docs/images/` are assets for the Chinese Live Demo guide, not standalone documents.

Status meanings:

- **Full**: a dedicated English counterpart exists.
- **Core**: the release-critical facts are covered in a broader English guide, but the source is not translated line by line.
- **English source**: the document is already written in English.
- **Gap**: no English counterpart exists yet.

| Document and title | Purpose | English coverage |
| --- | --- | --- |
| `docs/AUDIT_REPORTING.md`<br>审计查询与合规报表（v0.5 规划/新增） | v0.5 audit query/report CLI contract | Core: [Administration](ADMIN.md) |
| `docs/b2-s8-release-gate.md`<br>B2 S8 release reliability gate | B2 release reliability gates and fallback drill | English source |
| `docs/B5_S10_RELIABILITY.md`<br>B5 S10 executable reliability evidence | B5 fault matrix and soak evidence | English source |
| `docs/DEMO.md`<br>AgentSQL 本地 Live Demo | Local synthetic Live Demo, scenarios, reset, and exposure checklist | Gap; quick-start coverage only in [Quick Start](QUICKSTART.md) and [Getting Started](GETTING_STARTED.md) |
| `docs/DEPLOY.md`<br>AgentSQL 部署指南 | Compose, binary, systemd, PostgreSQL control plane, upgrade, rollback, and backup | Core installation/operations coverage; full translation remains a gap |
| `docs/DISCOVERY.md`<br>AgentSQL 敏感列发现指南 | Sensitive-column discovery, sampling, drafts, and limitations | Gap |
| `docs/ecosystem-db-compat-research.md`<br>国产数据库适配预研 | Chinese-database compatibility research and test records | Core status matrix in [root English README](../../README.en.md); research detail remains a gap |
| `docs/ecosystem-partnership-outline.md`<br>国产数据库联合案例大纲 | Joint vendor case-study and partnership outline | Gap |
| `docs/GETTING_STARTED.md`<br>AgentSQL 快速上手 | Installation, own-database flow, first query, and acceptance | Full: [Getting Started](GETTING_STARTED.md); concise path: [Quick Start](QUICKSTART.md) |
| `docs/INTEGRATIONS.md`<br>AgentSQL MCP 接入指南 | MCP transports, tools, clients, session handling, and troubleshooting | Full: [MCP Integration](INTEGRATIONS.md) |
| `docs/mcp-session-fix.md`<br>MCP Streamable HTTP 会话修复设计 | Streamable HTTP session model, errors, and compatibility | Core: [MCP Integration](INTEGRATIONS.md) |
| `docs/NOTIFICATIONS.md`<br>AgentSQL 通知外发指南 | Webhook/Syslog configuration, security, and delivery behavior | Gap |
| `docs/perf/README.md`<br>T25 网关开销基线 | Performance evidence index and reproduction entry points | Gap |
| `docs/perf/b2-column-auth-s5-20260925.md`<br>AgentSQL v0.4 B2 S5 列级 SELECT 负载观测 | B2 column-authorization load observations | Gap |
| `docs/perf/chain-load-s5c-20260924.md`<br>AgentSQL v0.4 B6 S5c 正式容量基线 | Audit-chain capacity baseline | Gap |
| `docs/perf/t25_bench_result.md`<br>T25 网关只读路径开销基线（实测存档） | Gateway read-path benchmark archive | Gap |
| `docs/rbac-multitenant-mvp.md`<br>RBAC / 多租户 / SSO MVP 设计（v0.5） | v0.5 RBAC, tenant isolation, API, and OIDC boundary | Core: [Administration](ADMIN.md) |
| `docs/release-notes-v0.2.0.md`<br>AgentSQL v0.2.0 | v0.2.0 release notes and checksums | Gap; historical, not part of the current core path |
| `docs/SPEC.md`<br>AgentSQL 架构与工程规格 | Architecture, contracts, rule model, compatibility, and quality gates | Core: [root English README](../../README.en.md) and [Security](SECURITY.md); full engineering translation remains a gap |
| `docs/USER_GUIDE.md`<br>AgentSQL 使用手册 | Full console, policy, rules, masking, configuration, and operations guide | Core: [Administration](ADMIN.md) and [Security](SECURITY.md); full page-by-page translation remains a gap |
| `docs/v0.5-known-issues-task7.md`<br>v0.5 已知问题修复（任务 #7） | v0.5 task-specific fixes and verification notes | Gap; engineering history |
| `docs/en/README.md`<br>AgentSQL | Earlier long-form English README translation and navigation | English source |
| `docs/en/GETTING_STARTED.md`<br>Getting Started with AgentSQL | Detailed English getting-started guide | English source |
| `docs/en/INTEGRATIONS.md`<br>AgentSQL MCP Integration Guide | Detailed English MCP guide | English source |
| `docs/en/QUICKSTART.md`<br>AgentSQL Five-Minute Quick Start | Concise five-minute English path | English source |
| `docs/en/ADMIN.md`<br>AgentSQL Administration Guide | English administration, v0.5 RBAC, audit, and operations | English source |
| `docs/en/SECURITY.md`<br>AgentSQL Security Model | English threat model, rules, masking, audit, and reporting | English source |
| `docs/en/COVERAGE.md`<br>English Documentation Coverage Inventory | This source-to-English inventory and gap analysis | English source |

## Core Gap Decision

The release-critical user journey is covered in English:

```text
install/start -> configure a data source -> authorize and query -> inspect audit
              -> connect through MCP -> operate within documented security boundaries
```

The remaining gaps are specialized references: full deployment internals, discovery, notifications, the complete console/configuration manual, detailed engineering specifications, performance evidence, and historical notes. They were intentionally not expanded during this core-only pass.

## Known Source Discrepancies

- The repository is now licensed under Apache-2.0 (see `LICENSE` and `NOTICE`), with a separate commercial license (`COMMERCIAL-LICENSE.md`). Historical release notes for v0.2.0 retain the AGPLv3 wording that applied at that point in time.
- Externally published assets remain v0.4.0 until release day; `server.json` and the repository release-preparation material identify pending v0.5.0. The audit CLI itself still says “planned for v0.5,” which is tracked as wording debt rather than evidence of a published release.
- The Chinese security policy lists 0.3.x as the supported series while current release-preparation material identifies pending v0.5.0. The English security page flags this for maintainer confirmation rather than guessing.
- RBAC user/role/tenant APIs exist, but the current frontend source has no dedicated pages for them. The English administration guide documents them as APIs, not console screens.
