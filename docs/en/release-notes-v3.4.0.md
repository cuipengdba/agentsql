# AgentSQL v3.4.0 release notes

**Language:** [English](release-notes-v3.4.0.md) | [Chinese](../release-notes-v3.4.0.md)

Status: release candidate. The [acceptance record](v3.4.0-acceptance.md) and GitHub Release determine formal delivery.

## Changes

| Area | Change |
| --- | --- |
| Kingbase column authorization | On the pinned single-instance KingbaseES V9R1C10 PG-mode build, the PostgreSQL-protocol B2 catalog-closed binder passed restricted single-table `SELECT`, live column authorization, allowed-column and denied-column checks. This does not qualify B5 writes. |
| Kingbase write boundary | Live B5 DML produced an affected-row count inconsistent with the PostgreSQL contract. Sessions, write transactions, direct execution, and B5 DML binding are closed for a detected KingbaseES kernel. A failed product identity probe closes these paths as well. |
| Vastbase query cost | The G100 V3.0 Build 9, commit 30156 identity check handles the real multiline version string. Under `LIMIT`, scan estimates aggregate known physical scan nodes; unknown structures and invalid estimates deny execution. |
| Vastbase types and operations | Live checks covered integers, exact numerics, text, date and time, booleans, binary data, JSONB and NULL on the pinned single instance. Verified TLS, certificate hot reload, credential rotation, permission revocation and connection recovery were exercised. |

## Connection and upgrade

Both vendor conclusions cover only the pinned builds, PG mode, single instances and synthetic data in the acceptance record. Kingbase B2 opens only for a proven restricted `SELECT` on one base table; expressions with uncertain lineage, views, joins, functions, subqueries and wildcards remain denied. Kingbase writes stay closed. The dedicated Vastbase read-only adapter retains table permissions, masking and audit. Its live B2 column-authorization probe did not pass, so B2 remains fail closed and does not inherit PostgreSQL binder evidence.

Back up metadata, audit data and configuration together before upgrading, and retain the previous signed image. In isolation, recheck version, license, mode, least-privilege account, CA and hostname, then use real MCP calls to verify allow, deny, masking and durable audit. Restore the previous binary and backup on failure. Other vendor builds, cluster topology, HA switchover and production load require separate acceptance.
