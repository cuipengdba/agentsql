# AgentSQL v3.4.1 security fix

**Language:** [English](release-notes-v3.4.1.md) | [Chinese](../release-notes-v3.4.1.md)

v3.4.1 fixes tenant isolation in the management audit SSE stream. Previously, an authenticated user with audit-view permission could receive historical and live events from other tenants in the process-local event cache through `/api/v1/stream`. The authoritative audit query API continued to filter by tenant.

The stream now requires an authenticated tenant and checks each historical and live event's tenant before sending it. Events for other tenants or without tenant identity are not emitted. Regression coverage includes cross-tenant history, live delivery and normal delivery to the current tenant.

Deployments using the multi-tenant management console and audit SSE should upgrade to v3.4.1 and recheck audit-view permissions and the stream. No database schema change is required. The Kingbase and Vastbase support boundaries from v3.4.0 are unchanged; see the [v3.4.0 release notes](release-notes-v3.4.0.md) and [live database acceptance](v3.4.0-acceptance.md).

Release status, signed assets, images and public acceptance will be recorded in the [v3.4.1 release evidence](v3.4.1-release-evidence.md).
