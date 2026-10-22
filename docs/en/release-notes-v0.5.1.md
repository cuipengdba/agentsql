# AgentSQL v0.5.1 Release Notes

**Language:** [English](release-notes-v0.5.1.md) | [Simplified Chinese](../release-notes-v0.5.1.md)

## This update

- MCP stdio uses standard line-by-line JSON messages by default for standard MCP clients to connect; existing clients with a custom length prefix can explicitly use `agentsql mcp --sealed-stdio`. HTTP MCP allows two verified stateful handshake versions, `2025-06-18` and `2025-11-25`, and rejects unknown versions and conflicting version claims.
- Agent identities for both HTTP and stdio are bound to their persistence tenants. Authoritative auditing continues to retain the tenant scope after the request is cancelled.
- Updated Claude Desktop and Dify self-hosted access steps. Windows Claude Desktop uses `mcp-remote` to forward stdio to AgentSQL's Streamable HTTP endpoint; Dify 1.17.1 requires the `MCP-Protocol-Version: 2025-06-18` request header to be explicitly added to the MCP service configuration.
- CI fixes prefetching of modules required for offline compilation testing, and aligns the Go build and vulnerability scanning toolchain to Go 1.26.9.
- Dameng and KingbaseES real database verification scripts fix MCP session header processing and add synthetic object cleanup on failure.
- Fixed an error in the number of fields when reading masking rules from PostgreSQL control plane snapshots, and limited the rule query to the current tenant; the quick-start mirror reference was synchronized to v0.5.1, and the PostgreSQL extended initialization verification of new data volumes was fixed.
- The Linux dual-architecture distribution package and GHCR image build incorporate SHA-256 and architecture checks of the independent YashanDB C client 23.4.7.100; the HTTP/MCP and persistence audit combination of the YashanDB database has not yet completed the real database verification.

## Validation scope

- DM8 Pack3, `COMPATIBLE_MODE=0`: Parameter binding, read-only account write rejection, controlled HTTP MCP SELECT, lineage, R010 table permission denial, mobile phone number masking, resolution failure shutdown and persistence audit passed.
- KingbaseES V9R1C10, PG mode: real license startup, CRUD and paging, identifiers, pgx parameter binding and SCRAM, read-only account write denial, AgentSQL controlled query, column lineage, masking, R006 denial and persistence audit passed.
- Use the MCP official Go client to verify standard stdio through real pipes; use `mcp-remote@0.14.3` to verify Windows-style bridging; use Dify 1.17.1's `MCPClient` code to verify initialization, tool discovery, and tool invocation with Agent Key.
- Use the v0.5.0 container to create the Agent and MCP Key. After stopping the service, copy the SQLite data volume and compare it with SHA-256; v0.5.1 successfully read the original identity and Key using the original volume. After the backup volume was restored, v0.5.0 also successfully started and verified the same identity and Key.

The above database verification only covers the specified version, mode and synthesis scenario, and does not represent all dialects, deployment forms or complete production support of Dameng or KingbaseES. The client protocol paths of Claude Desktop and Dify have been verified; the manual operations of the desktop UI and Dify's fully self-hosted UI are not included in this automation evidence. The support range of each database is subject to the [README matrix ](../../README.en.md).

## Upgrade and access

Stop the service and back up the control plane database and configuration before upgrading, and retain the Agent Key and `AGENTSQL_SECRET`. After the upgrade, check `/healthz`, management login and MCP tool discovery. The newly deployed standard stdio client uses the default `agentsql mcp`. When the existing custom frame protocol client is upgraded, `--sealed-stdio` is added. For detailed configuration, see [MCP Access Guide ](INTEGRATIONS.md).

The release package, signature and verification method are subject to the officially released assets of [GitHub Releases](https://github.com/cuipengdba/agentsql/releases/tag/v0.5.1).
