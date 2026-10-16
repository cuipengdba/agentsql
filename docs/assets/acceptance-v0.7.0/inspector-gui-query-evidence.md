# AgentSQL v0.7.0 — MCP Inspector GUI query evidence

- Client: MCP Inspector 0.16.8, local browser GUI
- Transport: Streamable HTTP, `http://127.0.0.1:61781/mcp`
- Authentication: AgentSQL Bearer key supplied through Inspector's custom Authorization header; secret value omitted
- Fixture datasource: `gui-client-pg`

| GUI operation | SQL | Decision | Audit ID | Observed result |
| --- | --- | --- | --- | --- |
| Allowed query | `SELECT id, phone FROM public.customers WHERE id = 1 LIMIT 1` | `allow` | `1` | One row; `phone` displayed as `138****5678`; `MaskedCells=1` |
| Denied query | `SELECT id, note FROM public.internal_notes WHERE id = 1 LIMIT 1` | `deny` | `2` | Policy hit `RuleID=R010`; no result rows |

Screenshots:

- [Allowed result and masked phone](./inspector-query-allow-result-v070.png)
- [Allowed result audit ID](./inspector-query-allow-audit-v070.png)
- [Denied policy hit and audit ID](./inspector-query-deny-rule-v070.png)
- [Denied overall decision](./inspector-query-deny-decision-v070.png)

The server-side audit check matched Inspector IDs `1` and `2` and Dify IDs `3` and `4` to their separate Agent identities and decisions. See the [safe audit summary](./client-gui-v070gui20261016-audit-summary.json).
