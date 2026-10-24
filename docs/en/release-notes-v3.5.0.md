# AgentSQL v3.5.0 release notes

**Language:** [Chinese](../release-notes-v3.5.0.md) | [English](release-notes-v3.5.0.md)

Status: release candidate. The [acceptance record](v3.5.0-acceptance.md) and GitHub Release determine formal delivery.

## Changes

| Area | Change |
| --- | --- |
| Three-gateway deployment | The Kubernetes sample fixes three worker nodes, three independent RWO PVCs, and stable B2 instance IDs. Preflight, read-only schema checks, anti-affinity, readiness probes, and disruption budget match that topology. |
| Cross-gateway audit events | PostgreSQL audit writes allocate a tenant-local commit-order cursor in the same transaction. Management SSE reads bounded pages from the separate audit database and supports tenant-local `Last-Event-ID` resume. The Web client preserves the frame cursor rather than the audit record ID. Out-of-range cursors, gaps, audit unavailability, and excess connections fail explicitly. |
| Failure and capacity bounds | Allow, deny, masking, durable audit, one-node failure, both control-database outages, and recovery were checked in isolation. Each node limits Agent QPS to 10 and management SSE connections to 50. The aggregate configuration budget is not a production throughput guarantee. |
| B5 identity fix | Standard Agent explicit-session sealing now uses the authenticated tenant, matching the repository tenant and avoiding a valid continuation denial caused by mismatched AAD. The three-replica release sample still disables B5 sessions and transactions; this fix does not enable cross-instance database transaction migration. |

## Upgrade and compatibility boundary

Shared PostgreSQL metadata remains at schema version 23; separate audit advances to version 7. Back up both databases as a pair, stop all old B2 gateways, wait for their leases to expire, and run migration and read-only checks before starting three new replicas. Rollback likewise stops all replicas, restores the paired backup, and uses the previous signed image; reverting only the image is insufficient when schemas differ. The isolated v3.4.1 two-replica to v3.5.0 three-replica and backup-restored v3.4.1 rehearsal is in the [acceptance record](v3.5.0-acceptance.md).

The public Demo may use a single instance or simpler topology and must state its actual configuration. This release does not claim zero-downtime mixed-version B2 rolling upgrades or silent B5 stateful-session recovery on another node. Production operators must validate their own PostgreSQL, network, storage, TLS, and workload.
