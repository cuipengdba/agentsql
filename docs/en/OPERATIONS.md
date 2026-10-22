# AgentSQL current version operation and maintenance manual

**Language:** [English](OPERATIONS.md) | [Simplified Chinese](../OPERATIONS.md)

Applicable versions: v3.0.2 and compatible 3.0.x maintenance versions. The specific release submission, tool chain and acceptance results are subject to the corresponding official Release. This article replaces the v0.7 operation and maintenance manual in the daily portal; the historical report still retains its current version and facts.

## On-duty inspection and version verification

Execute under the same configuration, running account and environment variables used for deployment:

```bash
agentsqlctl version
agentsqlctl doctor --config /etc/agentsql/config.yaml --format json
curl --fail http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

`doctor` Read-only checks the configuration, connections, and configured migration flows. It does not perform DDL, create missing SQLite files, or check the consistency of all table data. The exit code `0` means ready, `2` means pending migration, and `1` means blocking error. `2` or `1` cannot be regarded as online. `/healthz` indicates survival, and `/readyz` also checks service dependencies; locates the problem with the actual returned subsystem status.

v3.0.2 There is no new schema: the latest version of the SQLite merge library is 23, the PostgreSQL metadata is 22, and the independent audit is 6. The current binary `doctor`/`migrate --check` output shall prevail, and the migration version table shall not be modified manually.

## Backup, upgrade and recovery

1. Check target release, Ed25519 public key, signature list, and package hash. Retain the previous version of the program, configuration and complete recovery path.
2. Stop writing during the maintenance window, and back up a complete set of metadata, audit (if branched), configuration, `AGENTSQL_SECRET` used for data source encryption, as well as masking keys, OIDC/LDAP and other related external configurations. The key is stored independently and encrypted, and cannot be made public or submitted with the source code.
3. SQLite Backs up the database and necessary WAL state using SQLite backup API/`.backup` or after all processes have been completely stopped. Only the main `.db` file cannot be copied during service writing.
4. The PostgreSQL sub-library must make a supporting backup of metadata/audit and restore and verify it in an independent database; use the account of the migration manager to execute DDL, and the service running account must maintain the minimum permissions specified in the document.
5. Run a read-only check on the quarantined recovery copy with the last official program and the target program. When there is indeed a migration, the migration owner explicitly executes the following command before checking again:

```bash
agentsqlctl migrate --check --config /etc/agentsql/config.yaml
agentsqlctl migrate --config /etc/agentsql/config.yaml
agentsqlctl doctor --config /etc/agentsql/config.yaml --format json
```

6. After startup, check version, health, readiness, administrator login, and allow, override denial, masking, and persistent auditing of a minimally privileged Agent. Preserve audit IDs, times, actual commits, and topology without pasting keys or raw sensitive results on tickets.
7. If you must roll back, stop the new version first, restore the pairing database, configuration and keys, and then run the corresponding old program; it is prohibited to allow the old program to directly read the newer schema. Restoring to backup will lose writes after the backup time, and will be handled according to the recovery plan of the maintenance window.

For deployment and PostgreSQL permissions and backup commands, see [ Deployment Guide ](DEPLOY.md#upgrades-and-rollbacks). For signature and package verification, see [ issuing asset ](RELEASE_ASSETS.md).

## Audit failure and unknown commit status

When `AUDIT_UNAVAILABLE`, `AUDIT_OVERLOADED`, or readiness probe 503 occurs, check the audit connections, disk space, connection pools, write latency, and management audit outbox delivery status. Keep the fault closed and then confirm recovery with allow/deny requests and persistent auditing after recovery.

If the write request returns `COMMIT_OUTCOME_UNKNOWN`, it cannot be automatically retried; first manually check the actual status of the business database, transaction status and audit facts. Read-only requests also need to confirm that the original request has ended before retrying with a new request.

## MCP, identity and permissions troubleshooting

| entrance | current behavior | Troubleshooting points |
| --- | --- | --- |
| `/mcp` | Stateful Streamable HTTP, retains session ID after initialization | Each time it carries Agent Bearer and negotiates the protocol; subsequent requests carry `Mcp-Session-Id`. Works with existing Dify, MCP Inspector and SQLBot adapters. |
| `/mcp/2026-07-28` | Stateless entry, independent authentication each time | Protocol version, `Mcp-Method`, `Mcp-Name` when applicable, and request `_meta` are consistent; there is no legacy portal B5 session facility. |
| Optional OAuth/human delegation | Enable by deployment explicit configuration | Check the Agent, personnel, delegation token, tenant and revocation status respectively; the administrator login token cannot replace the Agent Key. |

`401` checks the Key and session status, `429` checks the Agent current limit, checks the request header and request body for protocol errors, and does not bypass restrictions by switching entrances. After the permissions are changed, re-execute the exact object/column request that should have been rejected, and confirm the rejection and audit facts before execution. For detailed examples, see [MCP Access Guide ](INTEGRATIONS.md).

## Database, B2/B5 and masking failure

The type, version, protocol mode, TLS, account permissions and SQL subset of the business database must meet the corresponding adaptation evidence. The registered type does not equal full production support. For details, please refer to the [ capability catalog ](USER_GUIDE.md) and each database acceptance record.

When the B2 column binding, B5 session/planned transaction or masked dependency is not ready, the current rejection behavior will be maintained based on the reason and audit position of `/readyz`. You must not turn off column policies, expand table wildcard permissions, or turn off auditing to allow requests to pass. B5 Unknown commit final status, lease and planned recovery need to be manually checked based on the actual session and transaction ID.

## Interface, language and client integration

The console login page and the top can be switched between Chinese and English, and you can choose to save it in the current browser; `?lang=en` or `?lang=zh` can set the initial language. Theme buttons control the entire interface, including the left navigation. Navigation scrolls independently within the short active viewport, and bottom options can be accessed using the scroll wheel, trackpad, or keyboard.

SQLBot adaptation is installed and verified separately according to [SQLBot access guide ](SQLBOT.md). AgentSQL must be kept as the query execution entry. SQLBot's own workspace permissions and AgentSQL's Agent/tenant permissions are configured separately.

## historical information

[v0.7 Operation and maintenance report ](v0.7-operations-runbook.md) and historical version acceptance are records of the environment at that time. To troubleshoot the current version, give priority to using this article, the current deployment guide and the corresponding official Release; historical passing conclusions cannot be used as acceptance of this customer environment.
