# AgentSQL v3.6.0 Release Notes

**Language:** [English](release-notes-v3.6.0.md) | [Simplified Chinese](../release-notes-v3.6.0.md)

Status: release candidate; the final acceptance record and GitHub Release are authoritative.

## Changes

| Area | Change |
| --- | --- |
| Cross-environment policy transfer | `agentsqlctl policy-transfer` signs a source tenant's simple table rules with Ed25519. A target operator uses a trusted public key, explicit resource mapping, and a target policy snapshot to prepare a proposal. The existing preflight, separate-person review, digest check, and atomic apply remain required. Column permissions, relationship bindings, row filters, wildcard objects, and legacy rules that cannot be represented are rejected. |
| Reliable webhooks | Per-tenant, per-channel durable cursors, stable Delivery IDs, leased claims, bounded retries, and dead letters consume final outcomes from the committed audit stream. Receivers must deduplicate Delivery IDs. The console shows backlog and oldest pending time. Replaying a dead letter requires permission and a management audit record first. Syslog and ordinary webhooks retain best-effort delivery. |
| Long-term audit verification | `agentsqlctl audit checkpoint-chain-verify` verifies ordered archives and Ed25519 checkpoints against an externally pinned predecessor, chain ID, public key, final digest, and age limit. Signing keys and trust anchors stay outside the gateway. |

## Upgrade and limits

Metadata gains a reliable-channel flag and notification cursor table. The combined schema reaches version 26; separate metadata reaches version 24, while audit remains at version 7. Back up metadata and audit together before the migration. Follow the [release acceptance routine](RELEASE_ROUTINE.md) for shutdown, migration, verification, and restore rehearsal. Restore the matching backups to roll back; older binaries cannot read the new schema.

Reliable webhooks provide **at-least-once** delivery. A crash after remote success but before local acknowledgement can resend an event. Use a new channel ID when changing the endpoint, credentials, or filter, and disable the old channel after its backlog drains. Audits committed before first enablement are not automatically replayed. The initial cursor is read before configuration publication; a cross-database gap may replay a small number of events. Audit database failure still closes the execution path, while notification target failure does not reclassify a committed audit.

Checkpoint-chain verification does not prove complete source-database export and does not provide WORM or third-party compliance certification. Operators must independently verify immutable storage, window coverage, and backup recovery. Database support levels do not change automatically with this release.
