> Sources: [Chinese User Guide](../USER_GUIDE.md), [v0.5 RBAC design](../rbac-multitenant-mvp.md), [v0.5 audit reporting](../AUDIT_REPORTING.md), and the current API/CLI implementation.

# AgentSQL Administration Guide

## Current Status

The current tree is prepared for the pending v0.5.0 release and contains the RBAC APIs and `agentsqlctl audit` implementation. The CLI help still labels audit query/report commands as “planned for v0.5,” which is known wording debt. Externally published installation assets remain v0.4.0 until the v0.5.0 release-day gates pass.

The embedded web console manages overview, audit, Agents, data sources, policies, rules, approvals, masking, B5 operations, and notification settings. User, role, permission, and tenant management is currently available through `/api/v1` management APIs; the current `web/src` tree does not contain dedicated user/role/tenant pages. This distinction prevents an API capability from being presented as an existing console screen.

## Sign In and Console Scope

Open <http://127.0.0.1:7780>. On first startup, AgentSQL bootstraps the default tenant, built-in roles, and an administrator from `AGENTSQL_ADMIN_USER` (default `admin`) and the required `AGENTSQL_ADMIN_PASSWORD`.

Console users and MCP Agents are separate identities:

- console users authenticate at `POST /api/v1/auth/login` and receive a signed access Bearer token plus a rotating refresh token; only the refresh-token SHA-256 hash is persisted;
- MCP Agents authenticate with an Agent API Key whose plaintext is shown only when created or rotated;
- an administrator token cannot be used as an Agent API Key, and an Agent Key cannot administer the console.

The main console sections have these purposes:

| Section | Purpose |
| --- | --- |
| Overview | Decision totals, trends, and the live SSE audit-event stream |
| Audit | Search, inspect, and export application-level audit events |
| Agents | Create machine identities, capability tiers, expiry, and API-Key rotation |
| Data Sources | Store PostgreSQL/MySQL connection settings and execution limits |
| Permissions | Bind an Agent to allowed or denied data-source objects and columns |
| Rules | Enable/disable built-in rules and store custom rule definitions |
| Approvals | Record a human decision for pending requests; approval does not execute SQL |
| Masking Rules | Configure result-level `mask`, `hash`, `block`, or `range` transformations |

## Data Source Administration

Create a dedicated, least-privilege database account before adding a data source. The console/API fields and defaults are:

| Field | Contract |
| --- | --- |
| `id` | Required and immutable after creation |
| `db_type` | `postgres` or `mysql` |
| `host`, `port`, `database`, `username` | Business-database connection target |
| `password` | Required on create; omitted/empty on edit preserves the stored value; never returned by the API |
| `conn_limit` | Default 5; minimum 1 |
| `stmt_timeout_ms` | Default 5000 |
| `row_limit` | Default 1000 |

Data-source passwords are encrypted with the exactly 32-byte `AGENTSQL_SECRET`. Keep that secret stable and back it up together with the control-plane database. Changing or losing it makes existing passwords undecryptable.

The current form has no SSL mode, CA, client-certificate, connection-timeout, or arbitrary DSN fields. Do not claim that database-link TLS validation can be configured in the console.

After adding a data source:

1. Test the connection.
2. Create an Agent with `readonly`, `dml`, or `ddl` capability.
3. Create a least-privilege policy for the Agent and data source. No policy means deny.
4. Avoid broad `*` or `schema.*` grants unless every column in the matching tables is intentionally authorized.

## v0.5 Users, Roles, Tenants, and RBAC

### Model and isolation

```text
tenant 1--N user N--N role N--N permission
                    |
                    +--N parent role (same tenant only)
```

Every user and role lookup includes a non-empty tenant ID. Disabled/missing users or tenants, missing roles or permissions, cross-tenant associations, and role-inheritance cycles fail closed. Tokens contain user and tenant identity but not a permission snapshot; each request reloads the principal and role graph so disabling a user or removing a permission takes effect immediately.

Existing Agent, data-source, policy, and audit tables predate tenant ownership. Until they are migrated, only `tenant_default` may access those shared gateway-resource routes. Other tenants can use tenant-native user/role/tenant endpoints, but receive HTTP 403 for shared gateway resources.

### Built-in roles

| Role | Permissions |
| --- | --- |
| `admin` | All permissions |
| `auditor` | `audit.view` |
| `operator` | `datasource.manage`, `strategy.manage`, `query.execute` |
| `viewer` | `audit.view` |

Multiple roles contribute the union of their permissions. Child roles inherit parent-role permissions. A role name alone does not grant system-wide authority.

The stable permissions are:

- `datasource.manage`: Agents and data sources;
- `strategy.manage`: policies, rules, masking, discovery, and notifications;
- `query.execute`: evaluation/execution operations, approvals, and B5 administration;
- `audit.view`: audit, dashboard, event stream, and audit-chain views;
- `user.manage`: users and their role assignments in the current tenant;
- `role.manage`: roles, permissions, and inheritance in the current tenant;
- `tenant.manage`: tenant management, with cross-tenant actions restricted to the bootstrap administrator.

### Management API

All endpoints except login and refresh require a Bearer console token and return the existing `{code,msg,data}` envelope. Logout persistently revokes the current access-token jti and its refresh family. Token-state read failures are rejected rather than bypassed.

| Endpoint | Required access |
| --- | --- |
| `POST /api/v1/auth/login` | Public; `tenant_id` defaults to `tenant_default` |
| `POST /api/v1/auth/refresh` | Public endpoint authenticated by a rotating refresh token |
| `GET /api/v1/auth/me` | Authenticated principal |
| `GET/POST /api/v1/users` and item/role-assignment routes | `user.manage` |
| `GET/POST /api/v1/roles` and item/permission routes | `role.manage` |
| `GET /api/v1/permissions` | `role.manage` |
| `GET/POST /api/v1/tenants` and item routes | `tenant.manage` |

Local passwords are stored as bcrypt hashes and never returned. A created user's password must contain at least 12 characters. Built-in roles cannot be deleted, the current user cannot delete itself, and the default tenant cannot be deleted. OIDC is not implemented in this batch; do not configure or advertise it.

## Audit Search and Export

### Console export

The Audit page filters by time, Agent, data source, session, MCP tool, decision, statement type, risk, keyword, and object. It exports up to 10,000 matching events as:

- NDJSON/JSONL for machines and SIEM ingestion;
- UTF-8-BOM CSV with spreadsheet formula-injection neutralization for Excel/WPS.

An over-limit export returns HTTP 422 instead of silently truncating. Successful console exports append a best-effort `audit.export` management record. The export is not a transactional snapshot.

### v0.5 audit CLI

`agentsqlctl audit` reads the configured audit store without running migrations. Query output defaults to a terminal table; `--format json` returns a structured object. Query limits range from 1 to 1,000 and default to 100.

```powershell
agentsqlctl audit query -c config.yaml `
  --since 2026-09-01T00:00:00Z `
  --until 2026-10-01T00:00:00Z `
  --action query --actor agent-prod-1 --db finance `
  --status success --limit 200 --format json
```

Compliance reports require a new output path and support `csv` or `jsonl`. The default safety ceiling is 10,000 matches and the maximum explicit limit is 100,000. If matches exceed the limit, the command fails instead of creating a truncated report.

```powershell
agentsqlctl audit report -c config.yaml `
  --since 2026-09-01T00:00:00Z `
  --until 2026-10-01T00:00:00Z `
  --status error --format csv `
  --out .\reports\audit-errors-2026-09.csv
```

Every successful report also creates `<out>.summary.json` with the exported time range, action/status distribution, top error codes, top actors, and the fixed column contract. Existing report or summary files are never overwritten.

The CLI derives `status=success` from `allow`, `deny`, `approve`, and `warn`; only `decision=error` maps to `status=error`. A policy denial is a successfully processed security decision, not a system failure.

## Operations

Use both process and storage checks:

```bash
agentsqlctl health --url http://127.0.0.1:7780/healthz --timeout 3s
agentsqlctl health --config /etc/agentsql/config.yaml --timeout 3s
```

`/healthz` reports process health and `/readyz` reports readiness. Before upgrades or control-plane migration, stop writers and back up the control-plane database together with `AGENTSQL_SECRET`. The detailed PostgreSQL control-plane, migration, backup, restore, upgrade, and rollback procedures currently live in the [Chinese deployment guide](../DEPLOY.md).

## Administrative Boundaries

- Application audit is not immutable WORM storage; privileged storage administrators can alter it.
- Approval records human intent but does not execute or exempt SQL.
- Custom rule records are not currently assembled into the execution engine; only built-in rule enablement affects runtime behavior.
- `row_filter` is stored and displayed but is not enforced. Use database RLS or security views instead.
- The console's data-source form does not configure end-to-end database TLS.
