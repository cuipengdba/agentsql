# AgentSQL v1.4.0 Release Notes

**Language:** [English](release-notes-v1.4.0.md) | [Simplified Chinese](../release-notes-v1.4.0.md)

## range

Console passwords and LDAP logins in PostgreSQL metadata deployments use a shared attempt budget instead. Up to 10 times per normalized tenant/account within 60 seconds from the first attempt, the two gateways jointly arbitrate in the database transaction; the budget key is the HMAC calculated by the deployment key, and no account plaintext is stored. Returns 503 for metadata failure and 429 for budget exhaustion. SQLite single node still uses local budget.

This version uses two gateways and independent business databases that share PostgreSQL metadata/audit for fault drills: one node exits, the audit library is temporarily unavailable, result delivery after recovery, chain verification, and management outbox status. The original authorization, masking, approval and audit failure closure contracts remain unchanged, and the MySQL column policy is still limited to the v1.3.0 verified 8.0/8.4 single-table InnoDB SELECT subset; the support levels of other databases will not be improved due to this version.

## Deployment and Compatibility

Added login budget table. SQLite and merged PostgreSQL metadata increased from 21 to 22, standalone PostgreSQL metadata increased from 20 to 21, audit-only remains at 6. Both nodes must use the same keys and configuration, but different instance identities. The upgrade requires stopping the flow and backing up the entire set; the v1.3.0 and v1.4.0 nodes cannot accept logins at the same time. For details, see [ deployment instructions ](DEPLOY.md).

## Release acceptance

Shared budget concurrency/failure, dual-node audit recovery, previous version official package upgrade/backup recovery, Go/Web/security CI, Ed25519 signature dual-architecture assets, and anonymous dual-architecture pull of three precise GHCR tags have all passed; for details, see [ acceptance record ](v1.4.0-acceptance.md) and [ official Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.4.0). The official website, public network Demo and GHCR `latest` will maintain v1.0.0 according to the phased plan, and will be updated uniformly after the completion of v1.5.0.
