# AgentSQL v1.3.0 Release Notes

**Language:** [English](release-notes-v1.3.0.md) | [Simplified Chinese](../release-notes-v1.3.0.md)

## range

v1.3.0 Adds a single-table InnoDB `SELECT` column-level authorization subset that is turned off by default for MySQL 8.0 and 8.4. Only explicit projections and restricted `WHERE`, restricted listing `ORDER BY`, bounded `LIMIT` will enter the new path. JOINs, views, generated columns, functions, subqueries, etc. have unproven syntax and are rejected before execution. MySQL queries with column-less strategies still follow the original table-level path; the support for PostgreSQL B2 and other databases remains unchanged.

The new path uses a new read-only business connection, a single transaction and a MySQL metadata lock in each request, and the fixed query is bound to the basic table; the independent PROCESS-only account reads InnoDB `TABLE_ID` and checks it with the instance UUID of the business connection. AgentSQL verifies column permissions and constructs a masking plan based on the real database column serial number and type fingerprint; the original SQL is executed in a lock-holding transaction, the transaction is ended after Fpost review, and then the persistent audit is written, the control plane fence is verified, and the output is sealed. If any link is not established, no data will be delivered.

## Policy registration and deployment

Deployers need to explicitly set `column_authorization.enabled: true`, `mysql_single_table_enabled: true`, and configure `mysql_metadata_accounts` for each MySQL data source. The metadata account should only have instance-level PROCESS; the business account should only have SELECT. Metadata passwords are read through environment variables and are not put into YAML. The administrator uses policy management permissions to create table allow and column allow policies for the same object, and then calls `POST /api/v1/policies/{id}/bind-mysql` on the column policy, carrying `If-Match` and the output/reference purpose of the column. AgentSQL submits the object ID, directory fingerprint, and column permissions after the second discovery of the real database and the second lock-holding review. Re-registration is required after policy revision or table reconstruction.

For complete configuration and calling examples, see [ deployment document ](DEPLOY.md). This release does not call MySQL all SELECTs, all versions, or column policy writes full production support.

## Acceptance and release cadence

MySQL 8.0.46/8.4.11 has passed the requests, masking, rejection, auditing, DDL competition and fault closure of the isolated real database; Go/Web/govulncheck has also passed the CI submission, v1.2.0 official package to this version of SQLite and PostgreSQL sub-library upgrade/backup recovery, 15 Ed25519 signature assets and dual-architecture OCI archiving have all passed. Official [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.3.0) has been released; GHCR's dual-architecture manifest of three precise version tags and six anonymous pulls have passed. For details, see [ acceptance record ](v1.3.0-acceptance.md). The official website, public network Demo and GHCR `latest` will maintain v1.0.0 as agreed, and will be updated uniformly by v1.5.0.
