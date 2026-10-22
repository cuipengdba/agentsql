# AgentSQL v1.9.0 Release Notes

**Language:** [English](release-notes-v1.9.0.md) | [Simplified Chinese](../release-notes-v1.9.0.md)

v1.9.0 Improved the operation path for administrators to access Dify and MCP Inspector for the first time. The console "Access Wizard" puts Agent, data source, connection pre-check, target SQL offline policy interpretation and audit positioning after real calls in the same process. This release does not change the gateway's authorization, enforcement, masking, or auditing contracts, nor does it expand database support.

## New additions and improvements

- Generate `/mcp` Streamable HTTP access instructions for Dify 1.17.1 and MCP Inspector 0.16.8. The administrator must fill in the gateway root address that is reachable by the client; the page rejects addresses containing accounts, passwords, paths, query strings or fragments, and prompts the meaning of the loopback address in the Dify container.
- Prompt Agent Key transmission risk for remote plaintext HTTP; HTTPS should be used or limited to an isolated intranet.
- The access description only contains the `<Agent API Key>` placeholder and does not collect or echo the real Key on the console. Dify instructions explicitly list `MCP-Protocol-Version: 2025-06-18`; Inspector 0.16.8 does not require manual addition of this request header.
- Use the Agent and data source selected above to call the existing strategy offline preflight to interpret a SQL. List static rules, objects, and policy IDs when explicitly denied; other results will always display "Cannot confirm permission yet" and require real MCP call verification.
- The audit ID of the real MCP response can be entered to directly access the audit record. Supplementary troubleshooting guidance for authentication failure, database unreachability, timeout, and read-only rejection.

## Compatibility and boundaries

- The data plane, control plane schema, existing MCP entry, identity and permission models remain unchanged. Connection pings, policy presence, and offline interpretation are not a substitute for real authorization or execution acceptance.
- Client instructions are for the verified version; other Dify, Inspector, or MCP protocol versions must be verified separately. The public network Live Demo only uses synthetic data and preset permissions.
- The real customer pilot has not yet started, and the first access completion rate for external users is not provided. Database support continues to be limited to each version's verified vendor version, schema, and topology; temporary licensing and single-instance testing do not equal full production support.

For design, negative use cases and formal delivery evidence, see [ design description ](v1.9.0-onboarding-design.md) and [ acceptance record ](v1.9.0-acceptance.md).
