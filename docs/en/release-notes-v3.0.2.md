# AgentSQL v3.0.2 Release Notes

**Language:** [English](release-notes-v3.0.2.md) | [Simplified Chinese](../release-notes-v3.0.2.md)

## change

1. The left navigation of the web console switches with light and dark themes; the menu continues to scroll independently, with bottom entries accessible.
2. GitHub README, documentation, and official website provide complete English corresponding content; chapters, tables, and executable examples are retained. Added the current document index and operation and maintenance manual, and historical records retain the current version facts.
3. SQLBot v1.10.2 PostgreSQL adapter providing fixed upstream version and source code hash. Table field discovery and SQL execution go through AgentSQL MCP, and field types are only output after authorization pruning.
4. SQLBot mapping binds the workspace to the data source. Rejection, approval required, unknown decision, no audit ID, missing mapping and unavailable gateway will not deliver query results; direct connection to the underlying business will be blocked and SQL will not be automatically retried.
5. GitHub and the official website have removed the WeChat group QR code, and retained the author's personal WeChat and WeChat public account QR codes.

6. Added unified AgentSQL Logo: security shield, database and Agent connection point, official website, GitHub README and Web share SVG master.

## Compatible and Upgradeable

Metadata combined database schema 23, sub-database metadata schema 22, and audit schema 6 remain unchanged. Back up the configuration, keys and status before upgrading; see the acceptance of this version for actual evidence of upgrade and recovery from the previous official version. The database support scope follows the original version of the evidence and will not be expanded due to document and client access.

SQLBot first phase authenticates PostgreSQL and specifies simple identifiers; deployment must use a dedicated Agent Key, server-side static mapping, and valid HTTPS. SQLBot's own metadata storage does not go through the business adapter. Model generation, all natural language scenarios, and all chart types are not included in execution layer acceptance.

See [SQLBot access guide ](SQLBOT.md), [ current operation and maintenance manual ](OPERATIONS.md), [ acceptance record ](v3.0.2-acceptance.md).
