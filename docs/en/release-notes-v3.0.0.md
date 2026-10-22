# AgentSQL v3.0.0 Release Notes

**Language:** [English](release-notes-v3.0.0.md) | [Simplified Chinese](../release-notes-v3.0.0.md)

Status: Officially released. For the release of access control and reviewable results, see [ acceptance record ](v3.0.0-acceptance.md) and [ release evidence ](v3.0.0-release-evidence.md).

## Scope of this edition

- The Agent capabilities of 30 registered databases are divided into four layers: controlled SQL, bounded SQL, independent controlled reading tools, connection or management preview, and are uniformly presented from the management API, console and authorized MCP data source list. Each item is accompanied by tool entry, limited scope and evidence documentation. The statement does not grant permissions. Actual calls still go through identity, object policy, rules, execution, masking and persistent auditing.
- Added read-only `agentsqlctl doctor --config ... --format json|text` to check configuration and configured metadata/audit migration history. Exit code 0 means ready, 2 means pending migration, and 1 means configuration or storage blocked. It does not run DDL, create missing SQLite files, or output credentials.
- Added `scripts/mcp-acceptance.py` for explicitly tagged synthetic environments, checking tool discovery, allow, rule deny, mask and audit IDs. The script only reads the API Key from the environment and does not print the data line or key.
- The official package, GHCR and CI use Go 1.26.10 uniformly, and the reachable risk report of 1.26.9 `crypto/x509` in the Go vulnerability database is fixed.

## Upgrades and boundaries

v3.0.0 does not add schema migration; the merged metadata version 23, separated metadata version 22 and independent audit version 6 of v2.9.0 remain unchanged. It is still necessary to backup the complete set according to [ deployment guide ](DEPLOY.md) and verify the recovery in an isolated environment; parallel contention between new and old gateways is not allowed during the B2 lease survival period. This version does not add database drivers, write entries, or new vendor full production support statements. For the acceptance scope, see [ design ](v3.0.0-design.md) and [ release evidence ](v3.0.0-release-evidence.md).
