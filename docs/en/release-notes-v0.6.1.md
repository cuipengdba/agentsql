# AgentSQL v0.6.1 Release Notes

**Language:** [English](release-notes-v0.6.1.md) | [Simplified Chinese](../release-notes-v0.6.1.md)

v0.6.1 repairs the management audit routing of v0.6.0 in the PostgreSQL metadata/audit branch: both management operations and traffic operations are written to the `traffic` chain of the audit library, and the metadata library only saves the `management_audit_outbox` record to be delivered. The management API and `agentsqlctl chain --domain management` query the `traffic` chain under the sub-library configuration; the merged library still uses the `management` chain of the metadata library.

This time there are no new migrations and no changes to the control plane schema. The PostgreSQL branch version of v0.6.1 is still metadata `16` and audit `6`. Except for branch audit routing and version identification, v0.6.0’s database support or feature commitments are not expanded. The isolated PostgreSQL sub-database environment has been used to verify service startup, manage audit persistence, and backup, recovery, and rollback of both databases.

## Update from v0.6.0 to v0.6.1

1. Stop all AgentSQL instances and control plane writing, back up the actual metadata and audit databases respectively, and retain the `AGENTSQL_SECRET`, configuration and old programs paired with the backup. The two backups of the branch database should have coordinated recovery points.
2. Use read-only SQL to confirm that the maximum versions of the two `schema_migrations` are metadata `16` and audit `6`. If versions don't match, check deployment history first; don't execute DDL just to pass the check.
3. Replaced with a complete set of v0.6.1 CLI and service programs, keeping the original DSN, SECRET and `store.auto_migrate: false`. After startup, `/readyz` is required to be ready, and a management audit is actually triggered, which is checked in the `audit_logs` and `traffic` chains in the audit library; the metadata library should not carry `audit_logs`. Operations involving asynchronous delivery should also check the persistent outbox of the metadata library, and delivery failures cannot be silently regarded as audit completion.

## As the v0.7 sub-library fallback baseline

fromv0.7When rewinding, stop firstv0.7Serve,**At the same time restore the metadata 16/audit 6 database backup and pairing SECRET before the upgrade**, and then start using the original branch library configurationv0.6.1. Only fallback binary is not available:v0.6.1exist`auto_migrate: false`Will refuse next timev0.7metadata 17 schema,v0.7Written data will not be automatically migrated in the reverse direction. After recovery, check the two version tables again,`/readyz`, Manage audit inventory and audit chain. Rolling back will lose new writes after the backup time point, and must be processed according to the business recovery plan during the maintenance window.

The original v0.6.0 sub-database service will write the wrong metadata library when starting the B2 management audit and exit due to the lack of `audit_logs`. It cannot be used as a verified service fallback target in the PostgreSQL sub-database scenario. Successful restoration of the old library or readability of the schema by the old CLI must not be regarded as successful rollback of the old service.
