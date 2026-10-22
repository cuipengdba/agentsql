# AgentSQL v0.9.0 Release Notes

**Language:** [English](release-notes-v0.9.0.md) | [Simplified Chinese](../release-notes-v0.9.0.md)

v0.9.0 focuses on temporary task Key usage budget, management audit delivery observability and offline explanation before policy changes. Database adaptation binning follows the evidence boundary of v0.8.0; this version does not upgrade any protocol compatibility paths to full production support.

## New additions and changes

- A newly applied task-level read-only key returns a result at most once by default, and can be set 1–10 times when applying. The application, approval, issuance, review and cancellation processes display the budget and usage times. Authorization checks the margin before execution, updates atomic deductions based on metadata database conditions after persistent auditing, and before results are returned; concurrently cross-instances release results within the budget at most. The number of times already deducted will not be refunded if the transmission fails or the process is abnormal. Authorizations created by v0.8.0 and earlier and without a budget maintain the original validity period and revocation semantics.
- The overview page of the sub-database audit deployment displays the number of pending deliveries, the number of retries, and the oldest waiting time of the current tenant management audit outbox. If it exceeds 60 seconds or there is a retry delay, it will display a serious backlog; if it exceeds 5 minutes, it will display a serious backlog; if the status reading fails, an error will be clearly reported. It does not replace the traffic audit library's readiness checks and persistent audit gating.
- Added SQL policy offline preflight on the permissions page: read the currently saved Agent, data source, and table policies and reuse static rules. Only return provable rejection or "Cannot confirm permission yet" and list relevant policy IDs; do not connect to the business database, do not execute SQL, and do not write traffic auditing. Column-level bindings, dynamic rules, view/function side effects, and business database status must still be accepted by the real gateway and MCP client.
- The PostgreSQL/MySQL sensitive derived output negative use case extends to expressions such as `COALESCE`, `NULLIF`, `MD5`, and continues to cover implemented source tracking and failure closure paths for functions, aggregates, CTEs, UNIONs, and views. Result layer masking is still not complete DLP; database account minimum permissions, column-level authorization and security views are still necessary boundaries.
- The public demo's health probe no longer echoes the demo administrator login credentials. The login configuration interface for the isolated demo environment continues to provide the public demo account to the login page to preserve auto-population; non-Demo deployments do not return this field.

## Upgrade and Compatibility

- The SQLite merged library metadata schema is upgraded from `19` to `20`; the independent metadata SQLite/PostgreSQL is upgraded from `18` to `19`; the audit-only schema remains `6`. The new fields are `max_uses` (can be empty) and `used_count` (not empty, default 0). The empty budget of the old authorization means unlimited times to avoid unexpected failure during upgrade.
- Stop writing before upgrading, and back up the complete set of metadata, audit (such as sub-library) and paired `AGENTSQL_SECRET`; run `agentsqlctl migrate --check` and check again after upgrading. To roll back, you must restore the complete set of backups before the upgrade, and you cannot use the old program to directly open the new version of the schema.
- v0.8.0 If `max_uses` is omitted when the client creates a new application, v0.9.0 will be assigned the default value 1. Replaying an old request with no budget set in the v0.8.0 era with the same idempotent key may get content conflicts; applications should read the original request and submit a new one with a budget using the new idempotent key.

## clear boundaries

- The budget limits the number of times results are returned to the MCP client. It does not prevent concurrent requests within the budget from initiating read-only queries first; atomic deductions are made before the final return. An audited result may consume the budget once after the network is disconnected.
- Overview outbox statistics are isolated by management login tenant, and only cover the management audit to be delivered, and do not represent the delivery status of external notification Webhook/Syslog.
- Offline pre-check does not produce a "permit execution" conclusion, nor does it include policy changes to be submitted; the actual permissions, execution, masking and auditing are still based on the current status of the gateway when it is executed.
- Capabilities of DM8, KingbaseES V9, and Vastbase G100 are still limited to specified instances, schemas, synthetic data, and bounded use cases that have been documented and do not represent vendor certification, cross-version compatibility, or production load support.

The acceptance results of formal assets, same-submission CI, dual-architecture GHCR, official website and demo are based on [v0.9.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.9.0) and [ acceptance records ](v0.9-acceptance.md).
