# AgentSQL v3.3.0 release notes

**Language:** [English](release-notes-v3.3.0.md) | [Chinese](../release-notes-v3.3.0.md)

Status: **formally released**. Delivery is documented in the [acceptance record](v3.3.0-acceptance.md) and [release evidence](v3.3.0-release-evidence.md).

## Changes

| Area | Change |
| --- | --- |
| Verified Dameng transport | A new DM datasource can use `verify-full`, a gateway-local CA file, and the expected certificate name. The TLS handshake verifies the chain, validity period, and name. The connection fails if continued encrypted transport cannot be established. Bypassed verification, missing CAs, relative paths, and unsupported modes are rejected. |
| Dameng query cost | Recognizes the `TOPN2` operator for `LIMIT` on the pinned DM8 Pack3 build. Scan estimates aggregate physical scan nodes with overflow checks instead of confusing returned rows with scanned rows. Unknown plan operators still fail closed. |
| Console | New DM connections expose the verified transport fields and explain the boundary for older connections without TLS settings. |
| Driver provenance | Pins a local MIT-licensed copy of `godoes/gorm-dameng` v0.7.2 with limited changes to connection properties, negotiated encryption checks, and certificate verification. See `third_party/gorm-dameng/README.agentsql.md`. |

## Connection and upgrade

Enable encrypted communication on the DM server and install a valid certificate. Put the issuing CA at an absolute path readable by the gateway and supply a DNS name or IP present in the certificate. Only explicit `verify-full` connections establish verified server identity. Existing DM datasources without TLS configuration keep their compatibility behavior; operators should migrate them to verified connections.

The live database conclusion covers only the DM8 Pack3 build, `COMPATIBLE_MODE=0`, a single instance, and synthetic data described in the acceptance record. It does not establish support for other DM builds, standby failover, mutual authentication, or complete production support. Back up metadata and audit data before upgrading, then recheck identity, permission, allow/deny, masking, and failure recovery.

Live allowed query examples are limited to direct column projection, equality filters, and bounded `LIMIT`/`OFFSET` on an authorized single table. JOINs, complex functions, and expressions with uncertain column lineage are outside the new acceptance scope; writes, multiple statements, and ungranted objects remain denied. R004 scan estimates rely on known plan nodes from this build, and unknown nodes or estimate overflow prevent execution.
