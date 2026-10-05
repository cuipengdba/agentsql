# Web UI gap inventory (2026-10-05)

Scope: `internal/adminapi` routes, `web/src/api`, `web/src/pages`, and the embedded `internal/webui/dist` snapshot. The workspace is on `feature/v0.4`, while `web/package.json` and `internal/version` report v0.5.0. The committed `dist` is a separate build artifact and was not regenerated in this change.

| Priority | Capability | API evidence | UI evidence | Status / next step |
| --- | --- | --- | --- | --- |
| P1 | Show running version and safe upgrade boundary | `GET /api/v1/system/release` returns the running version and read-only upgrade mode, protected by `audit.view` | `web/src/pages/Upgrade.tsx` and `/settings/upgrade` show the value and CLI preflight commands | Source implementation complete. Embedded console needs a separately reviewed `vite build` and `dist` update before deployment. |
| P1 | Human user and role administration | `/api/v1/users` and `/api/v1/roles` support list and mutation routes; `GET /api/v1/permissions` lists permissions | No user or role page or API client in `web/src` | Open. Add tenant-scoped lists and edit forms with RBAC visibility and authorization failure states. |
| P2 | Tenant administration | `GET/POST/PUT/DELETE /api/v1/tenants` exist | No tenant page or API client | Open. Add restricted tenant administration after user and role flows are clear. |
| P2 | Audit archive verification in console | `agentsqlctl audit verify-archive` exists as an offline command; export API serves ZIP/PDF | Audit page offers ZIP/PDF export, but no verification guidance or import workflow | Open. Prefer local CLI guidance; a server-side upload would introduce sensitive archive handling and needs a separate design. |

The new release endpoint is deliberately read-only. It does not fetch user-supplied URLs from the server, download binaries, or attempt installation. The source UI can be typechecked without touching `internal/webui/dist` using `tsc --noEmit`.
