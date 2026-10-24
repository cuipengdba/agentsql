# AgentSQL v3.9.0 Release Notes

**Language:** [English](release-notes-v3.9.0.md) | [Simplified Chinese](../release-notes-v3.9.0.md)

Status: candidate acceptance in progress; see the [release evidence](v3.9.0-release-evidence.md) for the formal outcome.

## Capabilities

1. The specified Oracle 26ai Free 23.26.3.0.0 single service supports `verify-full` TCPS. Administrators provide an absolute CA path and a host name matching the certificate. The gateway rejects unknown authorities, wrong names, and verification bypasses; every new physical pooled connection independently verifies the certificate. The driver moves to go-ora v3.0.1 with its cross-account fast-login cache disabled.
2. Administrators can explicitly set Oracle `read_control_json` to `{"oracle":{"explain":true}}`. This mode requires verified TLS and a dedicated non-system read-only account. Authorized narrow SELECT statements obtain a bounded `PLAN_TABLE` plan for R004/R005 cost checks before delivery. Plan permission, shape, timeout, or query failures never deliver raw rows. Oracle plan budgets are not claimed when this mode is off.
3. The specified SQL Server 2025 17.0.5015 single instance has been checked for strict certificate rotation, wrong authorities/names, SHOWPLAN revocation, controlled queries, and audit. This path retains existing read-only controls; a single-instance check does not establish Always On, read-only replica, or vendor certification.

## Security and compatibility boundaries

Authorization, exact table/column policy, source masking, and durable terminal audit remain on the MCP delivery path. Oracle plan mode covers only the verified narrow SELECT subset. TCPS wallets, mTLS, RAC, commercial Oracle editions, SQL Server Always On, and other topologies have not been verified. Neither database is declared to have complete production support; business writes remain closed, and the public Demo still uses synthetic PostgreSQL/MySQL data.

This release adds no metadata/audit schema migration. Upgrade and rollback require a paired backup of metadata, separate audit, and the secret, plus handling of the new Oracle configuration in the [upgrade guide](v3.9.0-upgrade.md). See the [acceptance record](v3.9.0-acceptance.md) and [release evidence](v3.9.0-release-evidence.md) for real-environment and formal-asset results.
