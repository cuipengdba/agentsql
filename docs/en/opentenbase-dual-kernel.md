# OpenTenBase dual-kernel deepening record (v0.5 batch 12)

**Language:** [English](opentenbase-dual-kernel.md) | [Simplified Chinese](../opentenbase-dual-kernel.md)

> Actual measurement date: 2026-10-02. The "pass" in this article only covers the listed images, topologies, versions, and synthetic use cases, and does not constitute manufacturer certification or production compatibility statement. Paths for which no real environmental evidence has been obtained are clearly marked as "pending official environmental testing/not implemented".

## 1. Conclusion and boundaries

| path | Conclusion of this batch | AgentSQL access boundary |
| --- | --- | --- |
| OpenTenBase PostgreSQL kernel | Under the **designated v2.5.0 container and stand-alone GTM/CN/DN topology, the minimum safety closed loop actual test passed** | Reuse `postgres` dialect; enable limited plan normalization only for exact `server_version = 10.0 OpenTenBase V2`; unknown version, node, or structure fail-closed |
| TXSQL/MySQL kernel | **is to be tested in the official environment, and the product-specific code** has not been implemented. | TXSQL is an independent MySQL kernel/repository, not another startup mode of the PG image; the candidate path is to reuse the `mysql` dialect, but it must first pass the real TXSQL version, directory, plan, cancellation and complete security closed-loop verification |

`executor.go` has not been modified in this batch: there is no evidence to support the registration of the new `txsql` data source type, nor to pass off ordinary MySQL 8.0 test results as TXSQL results.

## 2. PostgreSQL kernel environment and original evidence

The test image is third-party `domainlau/opentenbase:v2.5.0`. The measured processes and ports in container `otb-v05` are as follows:

- GTM：`20001`；
- CN：SQL `11000`、pooler `11001`；
- DN：SQL `21000`、pooler `21001`；
- `pgxc_node`：`gtm/G/20001`、`cn001/C/11000`、`dn001/D/21000`；
- `version()`：`PostgreSQL 10.0 OpenTenBase V2 ... 64-bit`；
- `SHOW server_version`：`10.0 OpenTenBase V2`。

The existing composite table `public.agentsql_v05_customers` has four columns, two rows of fictitious data and primary keys of `id/name/phone/email`, and `\d+` clearly shows:

```text
Distribute By: SHARD(id)
Location Nodes: ALL DATANODES
```

The `EXPLAIN (FORMAT JSON)` root node of the remote query is `Remote Fast Query Execution`, and the real output still lacks the only comma between the following two items:

```text
"Node/s": "dn001"
"Remote plan": [
```

CN local query does not use remote packaging. For example, `SELECT 1` returns legal standard PostgreSQL JSON, and the root node is `Result`; `SELECT pg_sleep(1) LIMIT 1` returns legal `Limit -> Result`. Therefore, after selecting the OpenTenBase parser by version, the parser must accept both **and** verified forms, and cannot mistakenly reject all legitimate local plans.

The official OpenTenBase document states that the standard cluster consists of GTM, Coordinator and DataNode, and the client connects to the Coordinator; this single-container topology is just a compatibility smoke, not a production deployment recommendation. Basis: [OpenTenBase official repository ](https://github.com/OpenTenBase/OpenTenBase), [ official Quick Start](https://docs.opentenbase.org/en/guide/01-quickstart/).

## 3. PostgreSQL plan adaptation design and implementation

The adaptation entry remains at `PostgresExecutor.explainWithRunner`, and only `SHOW server_version` that exactly matches `10.0 OpenTenBase V2` enters the OpenTenBase branch. Normal PostgreSQL and unknown OpenTenBase versions do not do text fixes.

This batch adds the following boundaries to the existing missing commas:

1. The planned `Plan Rows` and `Total Cost` are changed to required evidence; missing, negative, non-limited values or out-of-bounds are rejected.
2. For remote plans, only a missing comma in a real sample is allowed; it must be fixed into valid JSON.
3. The remote wrapper must be `Remote Fast Query Execution`, must contain a legal `Node/s`, exactly one `Remote plan`, and cannot be mixed in with a normal `Plans`.
4. The CN local plan must be legal JSON from the beginning and cannot be repaired by borrowing missing commas; the local root node and sub-nodes enter the same node whitelist and estimate verification.
5. Nested `Remote plan`, unknown nodes, more than 1024 nodes, more than 512 KiB, illegal UTF-8, unknown version all fail-closed.
6. `ExplainInfo.Raw` only saves the summary and does not return the conditions or object text in the remote plan.

New test coverage: real missing comma sample, OpenTenBase native standard PG JSON, missing estimate, missing `Node/s`, illegal nested remote plan, two missing comma candidates, unknown node, unknown version, oversized payload, and deep plan exceeding node limit. Existing plain PostgreSQL JSON tests continue to cover the standard parsing path.

## 4. PostgreSQL kernel real closed loop

Use the current source code to build a temporary gateway image, connected to an independent gateway container and an independent data volume`otb-v05`. Create a temporary minimum read-only role on the database side and only grant the target database connection,`public`usage and target table select; CN HBA only adds the Docker host address`172.17.0.1/32`role-specific`md5`Rules, the network segment is not open.

| steps | real results |
| --- | --- |
| Ping | Passed, 33 ms |
| discovery | Passed; scanned 1 table, 4 columns, 4 sample values, resulting in 3 findings; Management Audit ID 1 |
| R006 | Passed; query with `/* compat-b12 */` annotation rejected; Audit ID 2 |
| Remote EXPLAIN + Allow query | Passed; missing comma plan is strictly normalized, `est_scan_rows=120`, 2 rows returned, database query takes 8 ms; audit ID 3, `decision=allow`, `rows_returned=2` |
| masking | Passed; a total of 4 cells of phone/email were processed, and the results were `138****8000`, `139****9000`, `a***@example.com`, and `b***@example.com` |
| Local standard JSON | Passed; `SELECT 1 AS local_probe` returned 1 row, `est_scan_rows=1`; Audit ID 6 |
| audit | Passed; discovery, deny, allow, and cancel errors can be read from the management audit interface |
| Cancellation semantics | Passed; short-term `ACCESS EXCLUSIVE` lock manufacturing controllable wait, the client received `TaskCanceledException` at 795 ms after 750 ms cancellation; the number of active backends for this role after 1.2 seconds is 0; audit ID 5 is `decision=error`, `error_code=DB_QUERY_TIMEOUT` |

The cancellation use case validates the current link from the request context to PG CancelRequest/connection handling. It is not equivalent to network partition, CN failure, GTM failure or multi-DN failure recovery testing.

## 5. TXSQL/MySQL kernel research

### 5.1 Actual form

Official information releases the two kernels separately: the download page lists `OpenTenBase` and `TXSQL` respectively; there are also independent `OpenTenBase/OpenTenBase` and `OpenTenBase/TXSQL` warehouses under the organization. [ official download page ](https://docs.opentenbase.org/en/download/), [TXSQL official repository ](https://github.com/OpenTenBase/TXSQL).

The TXSQL official repository currently states that it is based on MySQL 8.0.30; the official overview calls it an enterprise-class MySQL kernel compatible with native MySQL. The official deployment page still shows the 8.0 branch build and the `8.0.22-txsql-...` example, indicating that there are version differences between the repository and the document example. The target submission/version must be fixed when accessing, and you cannot just write "TXSQL 8.0". [TXSQL Overview ](https://docs.opentenbase.org/en/guide/16-txsql_quickstart/), [TXSQL Deployment ](https://docs.opentenbase.org/en/guide/18-txsql_deploy/).

The official base uses connecting via `mysql -h host -P port -u user -p`, demonstrating its use of the MySQL client protocol; the documentation does not promise a dedicated fixed network port for TXSQL, and the deployment example even uses mtr to connect via a Unix socket after startup. Therefore the port must come from the target environment configuration and cannot be hardcoded in AgentSQL. [TXSQL basically uses ](https://docs.opentenbase.org/guide/20-txsql_basic/).

### 5.2 Conclusion on local operability

In this batch of environments:

- `domainlau/opentenbase:v2.5.0`Not included`mysqld`、`mysql`or`txsql`executable file;
- There is only a normal MySQL 8.x image locally, but no TXSQL image;
- The official deployment document requires building from TXSQL source code. The listed target environment is CentOS 7.8/7.9, TencentOS Server or Galaxy Kirin V10, and mtr is used as the experience startup method;
- The official GitHub page does not provide this batch of container releases that can be directly pulled up and bound to the document version.

Therefore, this batch marks TXSQL as **to be tested in the official environment/** has not been implemented. Ordinary `mysql:8` can only verify the existing MySQL path of AgentSQL and cannot be used as evidence of passing TXSQL; this batch does not write adaptation code for products that are not running.

### 5.3 Candidate access paths and fail-closed list

After the manufacturer or official reproducible environment is in place, priority is given to verifying and reusing the existing `mysql` executor instead of adding new registration items in advance:

1. Fixed TXSQL version/commit, deployment method, listening address and network port; save capability evidence as `SELECT VERSION()`, but relax parsing not only based on version string.
2. Use existing MySQL drivers for TLS, authentication, ping, context cancellation, connection pooling and read-only session validation.
3. Verify `information_schema.tables/columns/statistics`, `DATABASE()`, table row number estimation and minimum read-only permissions item by item; any directory differences are individually adapted and version thresholds are added.
4. Captures the true `EXPLAIN` column name, type and value. Reuse only if it contains the `type/key/rows` required by the existing standard path and the row width, row count, estimate and load cap are legal; any TXSQL-specific formats must be branched with real fixtures, exact versions and node whitelists.
5. Run Ping → discovery → R006 → allow query → masking → audit → cancel the same closed loop; then cover the impact of transactions, character set/collation rules, error codes, XA/strong synchronization features on session semantics.
6. Unknown versions, insufficient directory permissions, unknown plan columns/nodes, estimated overflows, and unknown connection status after cancellation will all fail-closed; execution after EXPLAIN risk control must not be skipped.

Only if the above link is completed on real TXSQL, will it be considered whether to add an explicit `txsql` capability/alias. If it's just the standard MySQL protocol and directory, continuing to use `db_type=mysql` is less likely to introduce unverified forks.

## 6. Realized, unrealized and follow-up suggestions

Already implemented:

- OpenTenBase V2 remote comma missing plan and strict dual-form parsing of CN local standard JSON;
- Required estimation, remote wrapper, number of nodes, nested remote plan and node whitelist verification;
- Unit tests and full PG minimal safety loop on specified containers.

Not implemented/to be tested:

- TXSQL local instance, TXSQL dedicated version detection or plan adaptation;
- Multi-CN, multi-DN, replication, failover, scaling, and production TLS;
- OpenTenBase plan form that is not whitelisted for nodes; when present, masked fixtures should be saved first and then expanded in small batch reviews.

It is recommended that the next small batch of OpenTenBase/TXSQL officials provide fixed version binaries or images, checksums, startup configurations and support matrices; first only do read-only synthetic data closed loops. In the next step of the PG path, add multiple DN real plan samples and fault injection. Do not predict the format by relaxing the current whitelist.
