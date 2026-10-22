# AgentSQL v0.8.0 Release Notes

**Language:** [English](release-notes-v0.8.0.md) | [Simplified Chinese](../release-notes-v0.8.0.md)

v0.8.0 Change the task-level temporary read-only Key to "Application → A different person will check the accurate SQL and approve it → The original applicant will receive it in one go". The existing v0.7.0 Key still takes effect according to the expiration and revocation rules; the old direct issuance API returns HTTP 410. The new version also improves client portability, console audit correlation, and access troubleshooting of the MCP tool schema.

## Functional and safety boundaries

| Ability | Behavior of this version |
| --- | --- |
| Mission authorization request | Users of the same tenant with `query.execute` submit an Agent, a data source, an exact simple `SELECT`, a validity period of up to 24 hours, and an idempotent key. SQL is stored encrypted with a pairing key; list and admin audits only show SHA-256. |
| Approval and collection | Another user of the same tenant who has both `strategy.manage` and `query.execute` can view the original SQL text in the details and approve or reject it. After approval, only the original applicant can receive the Key in one go. No permission or revoked Key rejection; receiving does not expand Agent, object, rule or database account permissions. |
| Audit correlation | Authorization IDs are associated with application, approval, issuance, and execution or rejection after entering the SQL pipeline. When metadata/audit is divided into databases, the management actions and outbox are submitted within the metadata transaction, and the relay is then delivered to the independent audit database; there is a window to be delivered during the failure, and the backlog should be monitored and backed up in a complete set. |
| MCP access | The tool schemas of `/mcp` and `/mcp/2026-07-28` have been verified with MCP Inspector `tools/list --strict`. Both entrances continue to abide by their existing protocol boundaries. |
| console | Two-person process for task authorization, one-time Key display, authorization ID audit screening and authentication, permissions, protocols, databases, and audit fault location. |

## Upgrade and rollback

Before upgrading from v0.7.0, stop writing and back up metadata, audit (if branched) and the paired `AGENTSQL_SECRET` in a set. SQLite uses consistent backups and cannot just copy the main database files that have not yet merged the WAL. Run the new version of `agentsqlctl migrate --check` first, and the migration manager explicitly executes `migrate`, then start the gateway and check `/healthz`, `/readyz`, MCP allow/deny, masking and auditing. The production configuration remains `auto_migrate: false`. If you want to roll back, first stop the new version, restore the database and pairing key before the upgrade, and then run v0.7.0; the old version cannot directly read the new version schema.

The isolation walkthrough covers SQLite metadata schema 18 → 19 → recovery 18, and PostgreSQL metadata/audit branch 17/6 → 18/6 → recovery 17/6. The target production environment must still independently exercise backup, recovery, and permission configuration. Read operations of existing task keys remain available after upgrade and recovery.

## Validation scope

- Go full warehouse short, `go vet`, 54 front-end Vitests, type checks and production builds passed; closure submission and simultaneous submission CI can be found in the GitHub Release text.
- Isolation Dify 1.17.1 workflow is compatible with `/mcp`'s allow, R010 deny, masking and persistent audit; MCP Inspector 2.11.0 completes real tool discovery, allow, deny and `tools/list --strict` for the old and new entrances.
- DM8 Pack3, KingbaseES V9R1C10 and Vastbase G100 Build 9 complete bounded revalidation within specified instances, protocol modes, restricted accounts and synthetic data. Results do not extrapolate to other versions, production loads, full dialects, or vendor certifications. The database support quantity and production support level remain unchanged.
- The dual-account console inspection and task Key real link have been completed; the outbox delivery results of the independent audit library shutdown and recovery can be found in the [ acceptance record ](v0.8-acceptance.md). Claude Desktop did not perform actual GUI testing after login.

Formal assets, three signatures, GHCR dual architecture and official website smoke test are subject to the actual release evidence of [v0.8.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.8.0). For detailed operations, see [ task authorization instructions ](task-read-grants.md), [MCP access guide ](INTEGRATIONS.md), and [ deployment guide ](DEPLOY.md).
