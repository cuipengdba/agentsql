# RBAC/Multi-tenancy/SSO MVP design (v0.5)

**Language:** [English](rbac-multitenant-mvp.md) | [Simplified Chinese](../rbac-multitenant-mvp.md)

## 1. Current situation survey

### 1.1 Authentication and user status

- The console login is at `internal/adminapi/handler.go`. Account from
  `AGENTSQL_ADMIN_USER` (default `admin`) and required
  `AGENTSQL_ADMIN_PASSWORD`, the startup entry is called at `cmd/agentsql/main.go`
  `AdminCredentialsFromEnv` and `config.ValidateAdminPassword`.
- The console token implementation is located at `internal/adminapi/auth_token.go`. It's not a three-part JWT,
  but expiring HMAC-SHA256 tokens signed with a key derived from `AGENTSQL_SECRET`; existing
  Valid for 12 hours. Bearer parsing and verification is located at `Handler.adminAuth`.
- Agent (machine identity) authentication is located at `internal/auth/auth.go`: plain text API Key is only created or
  Returned once on rotation, only the SHA-256 digest is saved in the database and compared in constant time. it works with the console
  Human user authentication are two security domains, and this design does not mix the two.
- v0.4 There is no console user, tenant, role, permission, or user role association table. All console APIs
  It only checks "whether it holds a valid administrator token", and there is no route-by-route authorization.

### 1.2 Current status of routing and database

- The console routing is uniformly registered in `http.ServeMux` of `adminapi.NewHandler`, and then
  `mcpserver.WithAdminAPI` mount; existing path prefix is `/api/v1/`.
- `internal/store/migrate.go` Embed migration via `embed.FS` to
  `schema_migrations` record version, executed within the transaction. Metadata supports both SQLite and
  PostgreSQL has two migration flows: "metadata/audit same database" and "audit sub-database".
- v0.5 follows this mechanism: new version 11 for the same library stream, new version 10 for the independent metadata stream; audit sub-library
  It does not carry identity data, so the RBAC table is not added.

## 2. Data model and isolation rules

The relationship is as follows:

```text
tenant 1──N user N──N role N──N permission
                    │
                    └──N parent_role (inherited from the same tenant)
```

| table | Key fields | constraint |
|---|---|---|
| `tenants` | `id`, `name`, `status` | ID and name are unique; status is active/disabled |
| `users` | `id`, `tenant_id`, `username`, `password_hash`, `status`, `auth_provider`, `external_subject` | `(tenant_id, username)` unique; only bcrypt hash stored |
| `roles` | `id`, `tenant_id`, `name`, `builtin` | `(tenant_id, name)` only |
| `permissions` | `code`, `description` | Permission code is globally stable and read-only |
| `user_roles` | `tenant_id`, `user_id`, `role_id` | Composite foreign keys ensure that users and roles are in the same tenant |
| `role_permissions` | `tenant_id`, `role_id`, `permission_code` | Roles are only authorized in their own tenants |
| `role_inheritance` | `tenant_id`, `role_id`, `parent_role_id` | Inherit from the same tenant and prohibit self-inheritance; runtime detection loop |

All user and role queries require explicit non-empty `tenant_id`, and SQL must filter this field at the same time; empty
Tenant, user does not exist/disabled, tenant does not exist/disabled, role is missing, inheritance loop, and permission is missing.
Refuse to process. The token contains `sub` (user ID), `tid` (tenant ID) and username, but not embedded
Permissions; each request checks the user, tenant and role graph in real time, so that disabling and revoking permissions take effect immediately.

Batch 36 has completed the tenantization of all existing business metadata: Agent, data source, policy and column permissions,
Rules, masking and keys, notifications, approvals, audit logs, management audit outboxes, audit chains and B5
Durable business entities all have `tenant_id`. There is no trusted attribution signal for historical data, so the migration is unified and
Backfill to `tenant_default` deterministically and never speculate about other tenants. Console binds from token `tid`
Warehousing context: The list only returns the current tenant row. Details, modifications, and deletions of other tenant resources are processed as 404.
Resource existence is not leaked; non-existent or disabled tenants are still rejected by real-time authentication checks. The RBAC table itself continues
Using the original tenant constraints, the platform running table does not increase business tenant ownership.

## 3. Permissions and preset roles

| Permissions | meaning |
|---|---|
| `datasource.manage` | Agent and data source management |
| `strategy.manage` | Policy, rules, masking, discovery, notification management |
| `query.execute` | Query evaluation/execution, approval and B5 actions |
| `audit.view` | Audit, dashboard, event flow, audit chain view |
| `user.manage` | Current tenant user and user role management |
| `role.manage` | Current tenant roles, inheritance and permission management |
| `tenant.manage` | Tenant management; cross-tenant operations are limited to bootstrap administrators |

Default tenant provisioning:

- `admin`: full permissions;
- `auditor`：`audit.view`；
- `operator`：`datasource.manage`、`strategy.manage`、`query.execute`；
- `viewer`：`audit.view`。

The permissions of multiple roles are combined; the child role inherits the permissions of the parent role. Any unknown roles or inheritance rings will cause the entire
If the authorization fails, the exception will not be ignored and the authorization will continue.

## 4. API

Responses continue to use the existing `{code,msg,data}` envelope. The list body is an array; the password hash never goes in
response. A Bearer token is required for all but login.

| methods and paths | Permissions | Request/Response Points |
|---|---|---|
| `POST /api/v1/auth/login` | public | Request `{tenant_id?,username,password}`; compatible with reserved `{token,expires_at}`, and return `{refresh_token,refresh_expires_at}`; tenant defaults to the default tenant |
| `POST /api/v1/auth/refresh` | Public (refresh token authentication) | Request `{refresh_token}`; rotate refresh token and return new access/refresh token; replay will revoke the entire refresh family |
| `GET /api/v1/auth/me` | Certified | Return users, tenants, roles and real-time permissions |
| `POST /api/v1/auth/logout` | Certified | Keep the original compatible response; persist the current access jti and its refresh family and invalidate them immediately. |
| `GET/POST /api/v1/users` | `user.manage` | List or create; create a password of at least 12 characters, and can also give `role_ids` |
| `GET/PUT/DELETE /api/v1/users/{id}` | `user.manage` | Current tenant CRUD; deletion of current user is prohibited |
| `PUT /api/v1/users/{id}/roles` | `user.manage` | Request `{role_ids}`, overall replacement |
| `GET/POST /api/v1/roles` | `role.manage` | List or create, support permissions and parent roles |
| `GET/PUT/DELETE /api/v1/roles/{id}` | `role.manage` | Role CRUD; built-in roles cannot be deleted |
| `PUT /api/v1/roles/{id}/permissions` | `role.manage` | Request `{permissions}`, overall replacement |
| `GET /api/v1/permissions` | `role.manage` | Return to stable permissions directory |
| `GET/POST /api/v1/tenants` | `tenant.manage` | Ordinary tenant administrators only see themselves; creating only guides administrators |
| `GET/PUT/DELETE /api/v1/tenants/{id}` | `tenant.manage` | Cross-tenant and deletion only guide the administrator; the default tenant cannot be deleted |

Cross-tenant management is the only explicit system boundary: only the default tenant, fixed boot users and still hold
When `tenant.manage` is specified, other `tenant_id` can be specified. Merely having a role named admin does not constitute system permissions.

## 5. Compatibility and initialization

The first startup performs idempotent initialization after existing database migration:

1. Create `tenant_default`;
2. Create four preset roles and permissions;
3. Create a boot manager using existing `AGENTSQL_ADMIN_USER/PASSWORD`;
4. Passwords are stored with bcrypt, and plain text is not written in logs or responses;
5. Bind the built-in `admin` role to the boot administrator.

The existing console path remains unchanged, and the default tenant is automatically used when the original administrator login request does not pass `tenant_id`.
Token signing still reuses existing key derivation and strict Bearer parsing, and adds identity and tenant fields to the payload.
Agent API Key authentication is not affected.

## 6. MVP and OIDC boundary

This batch implements local accounts, password hashes, signature tokens, user/role/tenant CRUD, multiple roles, and roles
Inheritance, route-by-route permissions, and tenant isolation. All capabilities are open to the outside world, without license or version access control.

OIDC is not implemented in this batch because the repository does not have issuer/client/redirect/claim mapping configuration.
Contract, and there is no provider available for real joint debugging. Follow-up needs to be implemented: PKCE authorization code stream, one-time
state/nonce storage, issuer discovery/JWKS verification, strict redirect allow-list,
`(issuer,sub)` unique mapping, explicit tenant and role claim mapping, and local account binding/recovery
strategy. OIDC must be turned off by default; it must not fall back to trusting unverified claims when the configuration is incomplete or verification fails.

This batch has delivered server token revocation for local users on the management side, as well as short-term access + long-term refresh tokens. The refresh token only has the SHA-256 hash and is rotated every time it is used; reusing a rotated or revoked token will revoke the entire refresh family and fail-closed when the store status is unreadable. Batch Thirty Six further delivers tenant ownership migration of all existing business metadata, and allows management audit outboxes and final audit events to carry tenant context. The follow-up will still include login speed limit and lock, as well as the above-mentioned identity capabilities such as OIDC/LDAP/MFA.
