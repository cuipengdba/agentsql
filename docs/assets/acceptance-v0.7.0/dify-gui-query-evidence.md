# AgentSQL v0.7.0 — Dify 1.17.1 GUI query evidence

- Client: Dify 1.17.1, isolated local browser GUI and an existing `query` workflow tool node.
- Source package: candidate commit `9429b25f800de1333d63feab72cc550fdb23f54f`, Linux amd64 package SHA-256 `4c14eea7a0133d795cbe976179d820aca37fdae816d28bb13fa9e712cf10ca28`.
- Transport: Dify API container to AgentSQL compatibility endpoint `/mcp` on the isolated network; request headers included Dify's own Bearer key and `MCP-Protocol-Version: 2025-06-18`. Credential values are omitted.
- The copied workflow retained its prior internal MCP server identifier `agentsql-v060` so the existing node would stay linked. Its display name, endpoint and credentials were updated in this isolated Dify copy for the v0.7.0 test.
- Fixture datasource: `gui-client-pg` on isolated PostgreSQL 16 with synthetic data.

| GUI tool-node operation | SQL | Decision | Audit ID | Observed result |
| --- | --- | --- | --- | --- |
| Allowed query | `SELECT id, phone FROM public.customers WHERE id = 1 LIMIT 1` | `allow` | `3` | Node `SUCCESS`; one row, `phone=138****5678`, `MaskedCells=1` |
| Denied query | `SELECT id, note FROM public.internal_notes WHERE id = 1 LIMIT 1` | `deny` | `4` | Node `SUCCESS` with a business-level deny result; `RuleID=R010`; no private row |

Screenshots contain no API key:

- [Allowed result JSON](./dify-query-allow-result-v070.png) and [Dify GUI context](./dify-query-allow-gui-v070.png)
- [Denied result JSON](./dify-query-deny-result-v070.png) and [Dify GUI context](./dify-query-deny-gui-v070.png)

The server-side audit query independently matched all four GUI call IDs (Inspector `1`/`2`, Dify `3`/`4`) to their separate Agent identities and decisions. See [safe audit summary](./client-gui-v070gui20261016-audit-summary.json). This verifies the compatibility endpoint used by these client versions; the new `/mcp/2026-07-28` endpoint has separate SDK protocol tests.
