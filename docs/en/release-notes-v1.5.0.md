# AgentSQL v1.5.0 Release Notes

**Language:** [English](release-notes-v1.5.0.md) | [Simplified Chinese](../release-notes-v1.5.0.md)

## range

This version has completed in-depth acceptance of designated instances of Dameng DM8 Pack3 (`dm8_20241022_x86_rh6_64_single`, COMPATIBLE_MODE=0), KingbaseES KingbaseES V9R1C10 (PG mode) and Vastbase G100 V3.0 Build 9 commit 30156 (PG mode). Each uses the least privilege read-only account and synthetic data to verify controlled SELECT, table-level authorization, mobile phone number masking, rule rejection, persistent auditing, policy and business account permission revocation, and failed shutdown and recovery of database disconnection.

The actual `MCPClient` of MCP Inspector 0.16.8 CLI and Dify 1.17.1 completed allow and deny calls to the three databases respectively, and checked the masking, rule ID and audit ID. Dify results come from client SDK calls and do not include visual workflow editor operations; Claude Desktop has not been tested. Vastbase uses a temporary license and no official production license can be inferred from this.

## Upgrades and Releases

fromv1.4.0The official package verifies SQLite schema 22 and PostgreSQL metadata/audit sub-library schema 21/6 tov1.5.0Migration check, existing Agent/approval data retention and complete backup recovery.v1.5.0Without changing these schemas,v1.4.0package read-only`migrate --check`Still reports current. The actual rollback still requires stopping the flow, backing up and confirming that the configuration is compatible, and old and new nodes should not be mixed without a plan.

Official releases include linux/amd64 and linux/arm64 installers, SPDX SBOM, provenance, SHA256SUMS and Ed25519 detached signatures. The main image, Demo image and quickstart image provide v1.5.0 precise label and dual-architecture manifest. The official website, download information, GHCR `latest` and public network demo are directly updated from v1.0.0 to v1.5.0; for evidence of issuance and public network acceptance, see [ acceptance record ](v1.5.0-acceptance.md).

## Support boundaries

Conclusions only cover the above database versions, schemas, topologies, synthetic data, and the tested controlled read-only subset of SQL. Verification of database-by-database real query timeout/cancellation, independent audit storage failure, vendor HA, TLS, long-term load, cross-version compatibility, formal production license and real customer environment has not yet been completed. This release does not claim full production support or vendor certification for these databases; claims of full database support are subject to proof. v1.4.0's shared Pipeline audit failure testing is not a substitute for independent results for each vendor instance.
