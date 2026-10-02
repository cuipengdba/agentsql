-- AgentSQL x HighGo scoped demonstration SQL
--
-- Scope:
--   * Tested table: public.agentsql_v05_customers
--   * Synthetic columns: id, name, phone, email
--   * AgentSQL datasource type: postgres (there is no highgo alias)
--   * The evidence applies only to the documented third-party SEE image.
--
-- Run each scenario separately. Scenarios A and C are inputs to the MCP
-- `query` tool. For scenario B, pass the SELECT body to `explain_query`; the
-- explicit EXPLAIN form below is for direct database verification by the
-- table-owning test role. Do not use production data or a privileged account.

-- Scenario A: authorized read-only query.
-- Expected through AgentSQL: allow, 2 synthetic rows, phone/email masking,
-- and an allow audit record.
SELECT id, name, phone, email
FROM public.agentsql_v05_customers
WHERE id > 0
ORDER BY id
LIMIT 10;

-- Scenario B: JSON execution plan.
-- Expected on the documented HighGo path: a JSON plan. Plan costs and nodes
-- are observations, not performance guarantees.
EXPLAIN (FORMAT JSON)
SELECT id, name
FROM public.agentsql_v05_customers
WHERE id = 1;

-- Scenario C: AgentSQL R006 rule interception.
-- Submit only this statement through the MCP `query` tool.
-- Expected: deny with R006 before business-query execution, plus a deny audit.
-- A direct psql session will not demonstrate AgentSQL rule enforcement.
SELECT /* joint-case-r006 */ id, name
FROM public.agentsql_v05_customers
WHERE id = 1
LIMIT 1;
