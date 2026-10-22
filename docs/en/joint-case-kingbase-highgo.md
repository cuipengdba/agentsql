# KingbaseES KingbaseES + HighGo HighGo joint case verification record

**Language:** [English](joint-case-kingbase-highgo.md) | [Simplified Chinese](../joint-case-kingbase-highgo.md)

> Record date: 2026-10-02 (Asia/Shanghai)
>
> Nature of Document: A reviewable engineering verification record, not a manufacturer certification, manufacturer endorsement, compatibility certificate, or production support commitment.
>
> Conclusion principle: The PostgreSQL protocol path in the specified environment has passed the actual test, which does not mean that the complete dialect is compatible, nor does it mean that the corresponding commercial version has passed the final test.

## 1. Summary of conclusions

| product line | Status of this batch | can be expressed publicly | Cannot be extrapolated |
| --- | --- | --- | --- |
| HighGo HighGo / SEE | **The PostgreSQL protocol path of the third-party SEE image has been tested** | In the following isolation test configuration that specifies mirroring, synthetic tables, and turns off B2, there are actual test records for connections, column discovery, R006 rule rejection, read-only queries, result masking, and audit closed loops. | It does not represent HighGo manufacturer certification; it does not mean that HighGo commercial version, other versions, Oracle compatibility mode, TLS, production topology or B2 column-level authorization has been passed |
| IvorySQL | **is a reference for the same source community version and does not replace HighGo. Conclusion** | IvorySQL is the HighGo community product line and has an independent community version protocol path record. | Do not replace HighGo commercial version final verification with IvorySQL results |
| CETC KingbaseES KingbaseES | **has not been tested, and it is waiting for the manufacturer's environment** | The existing third-party old image is blocked by the expired license; the V9R1C10 target image is not yet in place. | It must not be stated that the connection, discovery, query or AgentSQL security closed loop has passed; nor can the failure of the old image be interpreted as KingbaseES product incompatibility |

AgentSQL currently does not have a `highgo` or `kingbase` data source type. The HighGo testing in this recording was done via the existing `db_type=postgres` path. This fact only shows that this protocol path can work, not that it adds native dialect or vendor identification capabilities.

## 2. Environment and evidence caliber

### 2.1 Current container snapshot

2026-10-02 Execution:

```powershell
docker ps -a --format "table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"
```

Summary of output relevant to this case:

```text
hg-v06-user   qiuchenjun/hgdb-see:4.5.10.3       Up ...   127.0.0.1:5866->5866/tcp
kingbase-v06  chyiyaqing/kingbase:v8r6           Up ...   127.0.0.1:54321->54321/tcp
```

`Up` only means that the main process of the container is still running, but does not mean that the database in the container is available. The KingbaseES database status must be based on the database startup log in Section 2.3.

The read-only inspect summary for HighGo containers is:

```text
image sha256:e4dd0ac4877c3a8818ec4cef68108be8d54c410782a6248a01257ea66dd49725
command docker-entrypoint.sh postgres
mapping 127.0.0.1:5866 -> 5866/tcp
```

This image comes from a Docker Hub personal account and is not a manufacturer-delivered image. The image tag `4.5.10.3` and the version text returned by the database should be recorded separately. The tag cannot be regarded as the complete version number confirmed by the manufacturer.

### 2.2 HighGo’s current review evidence

The following command uses the existing UID 999 and local `sysdba` trusted connections within the container and does not read, export, or modify any database passwords:

```powershell
docker exec --user 999 hg-v06-user /opt/highgo/hgdb-4.5/bin/psql `
  -w -X -v ON_ERROR_STOP=1 -p 5866 -U sysdba -d highgo `
  -c "SELECT version();" `
  -c "SELECT current_user, current_database();"
```

Output:

```text
HighGo Database Management System 4.5 on x86_64,build on 20250227
current_user = sysdba
current_database = highgo
```

System directory and plan format review commands:

```sql
SELECT table_schema, table_name
FROM information_schema.tables
WHERE table_schema = 'public'
ORDER BY table_name
LIMIT 10;

SELECT ordinal_position, column_name, data_type
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'agentsql_v05_customers'
ORDER BY ordinal_position;

EXPLAIN (FORMAT JSON)
SELECT table_name
FROM information_schema.tables
WHERE table_schema = 'public'
ORDER BY table_name
LIMIT 10;
```

`public.agentsql_v05_customers` is visible in the output, and the list of contents returns `id integer`, `name character varying`, `phone character varying`, and `email character varying`; `EXPLAIN (FORMAT JSON)` returns a legal JSON plan. The root node is `Limit` and contains nodes such as `Sort`. The container log records both the database listener `0.0.0.0:5866` and the Unix socket `/tmp/.s.PGSQL.5866`.

The separation of powers is the known boundary of this environment. During the current review, `sysdba` directly reads the synthetic business table and returns:

```text
ERROR: You have no right to select it.
```

This is not a protocol failure: the synthetic table was created and held by the dedicated test role `agentsql_ro`, which was used by the AgentSQL closed loop on 2026-10-01. The current review does not detect, export, or reset its password, nor does it elevate `sysdba`'s privileges.

### 2.3 KingbaseES currently blocks evidence

View existing container logs read-only:

```powershell
docker logs --tail 200 kingbase-v06
```

Key original text:

```text
waiting for server to start.... stopped waiting
FATAL:  XX000: License file expired.
LOCATION:  PostmasterMain, postmaster.c:659
sys_ctl: could not start server
Examine the log output.
```

Therefore, even if `docker ps` displays `kingbase-v06` as `Up`, it cannot be determined that the KingbaseES database has been started. This batch does not restart, replace licenses, bypass restrictions, or download other images. The V9R1C10 target image will be tested after the manufacturer provides it.

## 3. Joint verification matrix

### 3.1 HighGo HighGo / SEE

The AgentSQL end-to-end record on 2026-10-01 follows the repository pre-research baseline: third-party image `qiuchenjun/hgdb-see:4.5.10.3`, port 5866, library `highgo`, dedicated test role `agentsql_ro`, synthetic table `public.agentsql_v05_customers`. `column_authorization.enabled=false`, so there is no B2 level authorization passed conclusion.

| Verification items | Status | command/input | Reviewable output | border |
| --- | --- | --- | --- | --- |
| connect | **has been measured** | AgentSQL data source `db_type=postgres` executes Ping; execute another `SELECT version()` in this round | AgentSQL Ping 74 ms; this round returns `HighGo Database Management System 4.5 ... build on 20250227` | Unofficial mirror; untested TLS, cancellation, connection pool failover |
| Column discovery | **has been measured** | The management terminal performs discovery on the data source; another query for this round is `information_schema.tables` | Found 1 target table, 4 columns, and 4 sample values; `phone` / `email` each had 2/2 hits, and `name` was a low-confidence generic candidate; discovery audit ID 4 | The sample is synthetic data; there is no replay of account-level discovery in this round; B2 is not enabled |
| read-only query | **has been measured** | `SELECT id,name,phone,email FROM public.agentsql_v05_customers WHERE id > 0 ORDER BY id LIMIT 10` | Return 2 rows; a total of 4 cells of mobile phone and mailbox have been masked, such as `138****8000`, `a***@example.com`; audit ID 12, `rows_returned=2` is allowed | Only prove this single table, these types and this version path |
| execution plan | **has measured the subset** | This round executes `EXPLAIN (FORMAT JSON)` system directory query in the real database | Return legal JSON, root node `Limit` | An example to prove that the plan format can be parsed, but does not represent coverage of all HighGo plan nodes. |
| Rule interception | **has been measured** | Submit controlled `SELECT` with block comments via AgentSQL | Hit built-in R006 and reject, reject audit ID 11; business SQL should not be executed | R006 is an AgentSQL capability, not a native database capability. |
| Wrong caliber | **has measured the subset** | `sysdba` without permission to read business table | Database original text `ERROR: You have no right to select it.` | Only necessary error classifications are recorded externally; passwords, connection strings or internal sensitive details are not disclosed |
| audit | **has been measured** | View audit records for discovery, deny, and allow requests | discovery ID 4; deny/allow ID 11/12 | ID is only valid on the local control plane and is not a stable identifier across environments. |

The above end-to-end evidence and wider compatibility pre-research record can be found in [, domestic database adaptation pre-research ](ecosystem-db-compat-research.md#062-HighGo-see-与KingbaseES旧镜像闭环). This paper converges on the subset required for joint cases without expanding the original conclusion.

### 3.2 CETC KingbaseES KingbaseES

| Verification items | Status | current evidence | Action after reaching the position |
| --- | --- | --- | --- |
| Database startup/connection | **did not pass the prerequisites and is waiting for the manufacturer** | After the third-party old image is initialized, it reports `License file expired` and the database service is not started. | The manufacturer provides the V9R1C10 target image, legal license, target compatibility mode and recommended connection method before performing Ping |
| Column discovery | **is not executed** | No database connection available | Verify `information_schema` / `pg_catalog`, identifier case, type and OID |
| read-only query | **is not executed** | No database connection available | Use the same synthetic table, minimum privilege account and limited row number query consistent with HighGo |
| execution plan | **is not executed** | No database connection available | Verify the syntax, structure, estimated fields and incorrect behavior of `EXPLAIN (FORMAT JSON)` |
| Rule interception/masking/auditing | **is not executed** | No AgentSQL request or audit ID | Rerun permission, rejection, masking, error and audit closed loop |
| Wrong caliber | **only records environmental blocking** | `FATAL: XX000: License file expired.` | Do not describe the error as a product incompatibility; the error code in the vendor environment is registered separately |

The result after KingbaseES is in place must list the target version, compatibility mode, driver, permission, TLS/authentication, catalog, type, `EXPLAIN`, timeout/cancellation and fail-closed behavior. Any untested items are marked as "to be verified" and cannot be deduced from the PostgreSQL protocol compatibility statement.

## 4. Three real database demonstration scenarios

The replicable SQL is located at [`examples/joint-case-highgo.sql`](../../examples/joint-case-highgo.sql). The demonstration uses only fictitious data and does not record real passwords or connection strings.

### Scenario A: Controlled read-only query and result masking

Submit via MCP `query`:

```sql
SELECT id, name, phone, email
FROM public.agentsql_v05_customers
WHERE id > 0
ORDER BY id
LIMIT 10;
```

Expected: Rules and Object Grant allowed; 2 rows returned; `phone` and `email` masked by enabled rules; results in allow audit. 2026-10-01 has been passed according to the above input.

### Scenario B: JSON execution plan

Pass in the `SELECT` of scenario A through MCP `explain_query`; you can execute the following when directly connecting to the real database for review:

```sql
EXPLAIN (FORMAT JSON)
SELECT id, name
FROM public.agentsql_v05_customers
WHERE id = 1;
```

Expected: Return JSON plan. Planned nodes and costs are for demonstration purposes only and should not be written as performance promises. This round has used the system catalog query to review the JSON plan format in the same real database; the business table plan needs to be executed using the test role holding the `SELECT` permission for the table.

### Scenario C: R006 comment rule interception

Only submit through MCP `query`, do not use `psql` direct connection to judge AgentSQL rules:

```sql
SELECT /* joint-case-r006 */ id, name
FROM public.agentsql_v05_customers
WHERE id = 1
LIMIT 1;
```

Expectation: AgentSQL rejects with R006 and writes deny audit before reaching the business query. 2026-10-01 has been run through with the same type of annotation `SELECT`; this result proves that the AgentSQL rules are effective, but does not prove that the database itself will reject the annotation.

## 5. Connect to existing quickstart/demo

It is recommended to reuse [ to quickly get started with "Path B: Connect to your own library" ](GETTING_STARTED.md#path-b-connect-to-your-own-library-real-production-closed-loop) in 5 minutes. Do not modify the Live Demo of the fixed PostgreSQL / MySQL synthetic data source:

1. Create a new HighGo data source in the isolation environment, select `postgres` as the type, and fill in the host, 5866 port, library name and minimum permission test account of the test instance; do not add the `highgo` type alias.
2. Create a dedicated readonly Agent that authorizes only `public.agentsql_v05_customers` and the four columns required for the demo.
3. Configure masking rules accurate to table columns for `phone` and `email`.
4. Run scenarios A, B, and C in sequence, and save tool responses and corresponding audits; passwords, API Keys, or complete DSNs must not be saved.
5. To strictly reproduce the protocol path results of 2026-10-01, `column_authorization.enabled=false` can only be recorded explicitly in an isolated demonstration environment. This does not equate to B2 validation and should not be recommended as a production configuration.

The existing quickstart's `demo.enabled` path is bound to a fixed data source and a fixed synthesis script and should not be directly transformed into the manufacturer's commercial version conclusion. The joint demonstration recommends taking the common data source access path to avoid mixing the security boundary of the Live Demo with the real database verification.

## 6. Manufacturer’s final inspection checklist

- The full name of the product delivered by the manufacturer, complete version, image or instance source, license validity period, deployment topology and compatibility mode.
- The manufacturer recommends drivers/protocols, TLS and authentication combinations, connection parameters, and minimum privilege account design.
- Connections, catalog, column discovery, types/OIDs, identifiers, allow/deny queries, JSON plans, timeouts/cancellations, connection pooling, and error codes.
- AgentSQL object/column authorization, rule denial, result masking, auditing, failover, and if B2 capabilities apply.
- Technical review and public release permission from both parties. Before completion, only use expressions such as "adaptation verification in progress" and "pending final verification in the manufacturer's environment".

## 7. Public caliber

Recommended sentence:

> The first joint case verification of AgentSQL focuses on CETC's KingbaseES KingbaseES and HighGo HighGo; the PostgreSQL protocol path experiment of the designated third-party HighGo SEE image has been completed, and the commercial version of KingbaseES and HighGo is still to be finalized by the manufacturer's target environment. The above results are not manufacturer certification or production support commitments.

For complete templates for public accounts, technical groups and English channels, see [ joint case community operation caliber ](joint-case-operations.md).
