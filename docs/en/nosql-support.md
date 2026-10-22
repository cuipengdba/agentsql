# NoSQL data source support scope

**Language:** [English](nosql-support.md) | [Simplified Chinese](../nosql-support.md)

The registry `internal/model/datasource_types.go` registers a total of 30 models in 8 categories: 9 relational models, and 21 NoSQL/vector data sources in 7 categories in the table below. Most still only have **connection level/low-level support for**; designated single instances of MongoDB 7.0.43, Redis 8.2.9 / Valkey 8.1.10, ClickHouse 26.8.31.2 and Milvus 2.6.25 also have restricted Agent MCP read-only paths. The adapter implements connections, health checks, metadata enumeration, and a subset of controlled commands; each capability is restricted by service version, credential permissions, request boundaries, and adapter whitelist. "Supported" here does not mean that all native commands, search syntax, management operations, or vendor certifications are available.

| Category | data source | adapter protocol | Default port | low-level abilities |
| --- | --- | --- | ---: | --- |
| key value | Redis | RESP | 6379 | Connection, health, metadata, controlled commands |
| key value | Valkey | RESP | 6379 | Same as above |
| key value | Memcached | text agreement | 11211 | Same as above |
| Documentation | MongoDB | MongoDB driver protocol | 27017 | Same as above |
| Documentation | Couchbase | HTTP management and query interface | 11210 | Same as above |
| Documentation | CouchDB | HTTP API | 5984 | Same as above |
| wide column | Cassandra | CQL | 9042 | Same as above |
| wide column | ScyllaDB | CQL | 9042 | Same as above |
| wide column | HBase | ZooKeeper Boot + HBase RPC | 16020 | Same as above; see below for default port exceptions |
| Figure | Neo4j | Bolt | 7687 | Same as above |
| Figure | JanusGraph | Gremlin Server | 8182 | Same as above |
| Figure | NebulaGraph | nGQL client protocol | 9669 | Same as above |
| Timing | InfluxDB | HTTP API | 8086 | Same as above |
| Timing | Prometheus | HTTP API | 9090 | Same as above |
| Timing | TimescaleDB | PostgreSQL protocol | 5432 | Same as above |
| OLAP | ClickHouse | native client protocol | 9000 | Connection-level preview retains the original default port; `query_analytics` specifying 26.8.31.2 uses native TLS port 9440 |
| OLAP | Doris | MySQL protocol | 9030 | Same as above |
| OLAP | StarRocks | MySQL protocol | 9030 | Same as above |
| Vector | Milvus | Milvus client protocol | 19530 | Old connection level preview retained; specify 2.6.25 TLS standalone available `search_vectors` controlled retrieval |
| Vector | Qdrant | Qdrant client protocol | 6334 | Same as above |
| Vector | Weaviate | HTTP API | 8080 | Same as above |

The 21 names, categories and number ports in the above table have been compared to `internal/model/datasource_types.go` one by one. The other 9 relational registration items are `postgres:5432`, `vastbase:5432`, `highgo:5866`, `opengauss:5432`, `mysql:3306`, `dm:5236`, `oracle:1521`, `yashan:1688`, `sqlserver:1433`; a total of 30 models. `timescaledb` is classified in the registry as Time Series and still uses the PostgreSQL protocol. The registered port is the default configuration value, and there is no guarantee that this value is the port to which the adapter is actually connected:

- The HBase registered port is `16020`, but the current `gohbase` adapter rejects it as the region-server port; the ZooKeeper bootstrap port needs to be configured explicitly (the code prompt is usually `2181`), and the connection cannot be completed directly with the registered default value.
- The Couchbase registered port is `11210`; the adapter uses the HTTP management port `8091` and query port `8093` respectively under this default value, and uses `18091` / `18093` when configuring TLS. When overwriting the registered port, it is calculated according to the mapping rule of `couchbaseURLs`. `11210` cannot be regarded as the HTTP port.

## command boundaries

The adapter performs command whitelisting and parameter verification based on data sources. For example, the read-only subset of Redis/Valkey includes `GET`, `MGET`, `SCAN`, and `INFO`; Memcached includes `GET`, `MGET`, and `STATS`; Cassandra/Scylla includes restricted `SELECT`, `LIST TABLES`; ClickHouse, Doris, StarRocks including restricted `SELECT`, `SHOW`, `DESCRIBE`. Milvus, Qdrant, and Weaviate have their own restricted commands such as `GET`, `DESCRIBE`, and `SEARCH`. Diagrams, documents, and timing adapters also have their own syntax and boundaries. For details, please refer to the corresponding `businessdb` adapter code.

Write commands such as Redis `SET`, Qdrant `UPSERT` are rejected on read-only connections. Syntax that is outside the whitelist, exceeds limits, and is unsafe will also be rejected. Native query auditing in the adapter only logs command decisions, not keys, parameter values, or credentials.

## Agent MCP controlled path for Redis/Valkey

v2.6.0 The new `read_key` is separated from the administrator native command preview above. Only open on Redis 8.2.9 or Valkey 8.1.10, strict TLS, ACL account and designated single instance; fix the database and current connection configuration for each request, and recheck the Agent/tenant and `key` policies. Policy `dbN:exact-key` or `dbN:bounded-prefix*` and SQL table permissions do not replace each other, and denial takes precedence. Requests only accept exact ASCII keynames, fixed calls `TYPE` and bounded `GETRANGE`; requests over 64 KiB, non-UTF-8, non-strings, wildcards, dangerous commands, and cross-library operations are rejected. The source of the key value should be masked, and the persistent pre-check and final audit should use HMAC key identification. No value will be delivered when the independent audit library or business database fails; for details and untested scope, see [v2.6.0 and acceptance ](v2.6.0-acceptance.md).

## ClickHouse's Agent MCP controlled path

v2.7.0of`query_analytics`Separated from admin native command preview, only for ClickHouse 26.8.31.2, strict TLS,`readonly=1`Least privilege roles and designated single nodes. The gateway verifies accuracy one by one`database.table`Table authorization and explicit column whitelist (including filter columns), only read explicit columns of the MergeTree table and String equal value filtering, return 1-100 rows; server generates parameterized SQL, additional query time, read rows/bytes, memory, output rows and thread limit, the results are also subject to 1 MiB and UTF-8 boundaries. Delivery after successful masking and persistent pre-check/final audit. Arbitrary SQL, views, distributed, table functions, JOIN, aggregation, write, cross-database and full production support are not open; for evidence, see[v2.7.0Candidate for acceptance](v2.7.0-acceptance.md)。

## Milvus' Agent MCP controlled path

v2.8.0of`search_vectors`Separated from administrator native command preview, only for Milvus 2.6.25,`default`Database, strict TLS, authenticated non-admin accounts, and designated single nodes. The gateway verifies accuracy one by one`default.collection`Table authorization and vector fields, column whitelist of all output fields. The request only accepts a limited FloatVector, 1–20`top_k`with explicit scalar fields; the server checks dimensions and COSINE indexes, and always calls Search without filter expressions. Results are subject to time limits, hit counts, and 1 MiB boundaries, and are delivered only after source field masking and persistent preflight/final state auditing are successful. Hybrid retrieval, arbitrary expressions, writing, clustering, and full production support are not yet available; see for evidence[v2.8.0Candidate for acceptance](v2.8.0-acceptance.md)。

## Console and HTTP API

- `GET /api/v1/datasource-types` returns 30 types and capability levels; NoSQL is `nosql-connect`.
- After saving the NoSQL data source, you can use `POST /api/v1/datasources/{id}/native-ping` to test the connection. The response provides success status and time consumption; when the connection is successful and the adapter can read the version, `version` is filled in with the server version, otherwise it is empty.
- `GET /api/v1/datasources/{id}/native-schema` `Discover` when calling the adapter through a read-only gateway, returning up to 100 namespaces and limiting the number of fields and response bytes; truncated to `truncated=true`. If this capability is not available, `NATIVE_SCHEMA_UNSUPPORTED` will be returned. If connection or enumeration fails, `NATIVE_SCHEMA_FAILED` will be returned.
- `POST /api/v1/datasources/{id}/native-query` only enables read-only commands in the adapter whitelist. The default is `read_only=true`. Preview returns up to 100 rows, 64 KiB of data; truncated at `truncated=true`. Both write commands and `read_only=false` return 403 and are written to the management audit; unknown commands and over-limit parameters are rejected before execution. The adapter still performs its own parameter and syntax validation. Query auditing only records commands and decisions; it does not save keys, parameter values, or credentials.

Local verification example: Create the `redis` data source after logging in to the console, fill in the local reachable address for the host, and the default port is `6379`. After saving, click "Test Connection". Connection results depend on the actual service and credentials; routing behavior can be verified without a real container with `go test -short ./internal/adminapi/...`'s mock HTTP test.
