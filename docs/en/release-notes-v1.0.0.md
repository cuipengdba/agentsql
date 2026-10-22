# AgentSQL v1.0.0 Release Notes

**Language:** [English](release-notes-v1.0.0.md) | [Simplified Chinese](../release-notes-v1.0.0.md)

v1.0.0 Tightened the closure of level authorization failure, added optional HTTP MCP personnel delegation identity, and fixed design, verification, release, official website and Live Demo as each version delivery process. This version does not add database types, nor does it expand the production support statement for domestic databases.

## New additions and changes

- SELECT with column-level policy no longer falls back to the table-level authorization path. PostgreSQL must be validated by an enabled and available B2 binder for column references such as projections, predicates, JOINs/self-joins, etc.; pre-execution rejection and write auditing when B2 is unavailable. Dialects such as MySQL and Vastbase that do not have equivalence proofs still do not support column strategies.
- Added optional `mcp.delegation.require_human_token: true`. HTTP MCP requests use both the Agent API Key and the short-lived, agent-specific human delegation token. Console login tokens cannot be used directly with MCP; delegation tokens cannot call console APIs. After the user logs out, deactivates or loses `query.execute`, the delegation becomes invalid immediately.
- The "Task Authorization" page of the console provides personnel token issuance and one-time display when this mode is enabled; the issuance is written for management audit, and the token will not be returned when the audit is unavailable. `details_json.delegated_user_id` for MCP traffic audit indicates the verified user ID; existing Agent and task authorization IDs are retained.
- The stateful MCP session binds the dual identities of Agent and person, and replacement must be reinitialized; the stateless entrance is verified on a request-by-request basis. In dual-identity mode, the B5 tool, `request_approval` and `execute_approved` do not currently have complete personnel attribution links, so they are closed. Normal mode maintains the original tool behavior.
- The permissions page shows the execution boundaries of B2 and dialects when the column policy exists; offline preflight still only gives denial or unknown, not proof of allowed execution.

## Upgrade and Compatibility

- There are no new metabase migrations in this version. It is still necessary to back up the metadata, audit and paired `AGENTSQL_SECRET` first, and use `agentsqlctl migrate --check` to check and upgrade; when restoring, use the complete backup set before the upgrade.
- Personnel delegation is turned off by default and does not affect old clients. After enabling, each HTTP MCP request must add `X-AgentSQL-User-Authorization: Bearer <人员委托令牌>`; the console must be opened. First, the logged-in user with `query.execute` calls `POST /api/v1/auth/delegation-token` and submits `{"agent_id":"..."}` to obtain the token. Tokens are valid for up to 30 minutes and cannot be renewed; they will be reissued after expiration. The client needs to be able to send custom HTTP headers.
- If the column policy is set in the old deployment but PostgreSQL B2 is explicitly shut down, the related SELECT will be changed from possible table-level release to failed shutdown. Column policies, B2 readiness status, and target queries should be checked before upgrading; necessary column constraints cannot be removed to restore the query.

## clear boundaries

- Personnel delegation only proves the identity and current query permissions of the person in the HTTP MCP request; Agent Key, database account minimum permissions, SQL policy, masking and auditing are still in effect respectively. It is not equivalent to OAuth third-party token exchange. Delegation tokens go into the trusted client credential configuration and cannot be put into logs or public demos.
- Stateful sessions are reinitialized after a switch; cross-request approval executions and B5 plan transactions are closed in delegated mode until their full dual-identity audit contract is verified. stdio inherits the local Agent Key contract.
- The SQL shapes available for PostgreSQL B2 are still limited to the capabilities of existing binders. MySQL's column-level binding, domestic database complex JOIN/TLS/HA/load and real customer environment have not yet obtained new version evidence; the database support level remains the same.
- The evidence of formal CI, asset signature, GHCR summary, real MCP client, official website and Live Demo shall be subject to [v1.0.0 acceptance record ](v1.0-acceptance.md) and GitHub Release text. A gate that has not passed cannot be written as delivered.
