# AgentSQL v3.7.0 Release Notes

**Language:** [English](release-notes-v3.7.0.md) | [Simplified Chinese](../release-notes-v3.7.0.md)

Status: released. See [release evidence](v3.7.0-release-evidence.md) for formal assets, GHCR, website, and public Demo results.

## Capabilities

1. Controlled `find_documents` now covers the specified MongoDB 7.0.45 three-member replica set. Administrators explicitly approve members and collection indexes. The gateway rejects unknown members before connecting and verifies each member's TLS certificate. Before reading, it checks the approved index and actual scan count with `executionStats`, then uses the same index hint for the read.
2. Controlled `read_hash_fields` now covers exact keys and fields on the specified Redis 8.2.9 and Valkey 8.1.10 Sentinel topologies. Every field needs an exact grant. A fixed read-only script limits type, field presence, and bytes on the server. Sentinel and data nodes use separate ACL identities; discovered masters must pass an exact address allowlist and TLS verification.
3. The console and API persist `read_control_json`, containing topology and index metadata only. Operations provision the Sentinel password in a gateway environment variable whose name is derived from the tenant and datasource IDs. New migrations cover combined and separated SQLite/PostgreSQL metadata stores.
4. Reads recheck Agent, tenant, policy, and datasource state. Masking and durable terminal audit precede delivery. Audit writes have a two-second limit; failed audit prevents data delivery.

## Compatibility and limits

Existing controlled standalone MongoDB, Redis, and Valkey reads remain available. New topologies require explicit administrator configuration. Support conclusions cover only the tested image versions, TLS/ACL configuration, and synthetic data in the acceptance record. MongoDB sharding, Redis/Valkey Cluster, arbitrary aggregation/commands/writes, vendor certification, and a complete production SLA are outside this release. The public Demo still presents only its deployed synthetic PostgreSQL/MySQL scenarios.

See the [upgrade guide](v3.7.0-upgrade.md) for configuration and rollback, and the [acceptance record](v3.7.0-acceptance.md) for actual tests and unverified scope.
