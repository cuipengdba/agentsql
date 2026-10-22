# NoSQL 数据源支持范围

**语言 / Language:** [简体中文](nosql-support.md) | [English](en/nosql-support.md)

注册表 `internal/model/datasource_types.go` 共登记 8 类 30 款：关系型 9 款，以及下表 7 类 21 款 NoSQL / 向量数据源。多数仍仅有**连接级/低阶支持**；MongoDB 7.0.43、Redis 8.2.9 / Valkey 8.1.10、ClickHouse 26.8.31.2 和 Milvus 2.6.25 的指定单实例另有受限 Agent MCP 只读路径。适配器实现了连接、健康检查、元数据枚举和受控命令子集；各能力受服务版本、凭证权限、请求边界及适配器白名单限制。这里的“支持”不代表所有原生命令、检索语法、管理操作或厂商认证均可用。

| 类别 | 数据源 | 适配器协议 | 默认端口 | 低阶能力 |
| --- | --- | --- | ---: | --- |
| 键值 | Redis | RESP | 6379 | 连接、健康、元数据、受控命令 |
| 键值 | Valkey | RESP | 6379 | 同上 |
| 键值 | Memcached | 文本协议 | 11211 | 同上 |
| 文档 | MongoDB | MongoDB 驱动协议 | 27017 | 同上 |
| 文档 | Couchbase | HTTP 管理与查询接口 | 11210 | 同上 |
| 文档 | CouchDB | HTTP API | 5984 | 同上 |
| 宽列 | Cassandra | CQL | 9042 | 同上 |
| 宽列 | ScyllaDB | CQL | 9042 | 同上 |
| 宽列 | HBase | ZooKeeper 引导 + HBase RPC | 16020 | 同上；默认端口例外见下 |
| 图 | Neo4j | Bolt | 7687 | 同上 |
| 图 | JanusGraph | Gremlin Server | 8182 | 同上 |
| 图 | NebulaGraph | nGQL 客户端协议 | 9669 | 同上 |
| 时序 | InfluxDB | HTTP API | 8086 | 同上 |
| 时序 | Prometheus | HTTP API | 9090 | 同上 |
| 时序 | TimescaleDB | PostgreSQL 协议 | 5432 | 同上 |
| OLAP | ClickHouse | 原生客户端协议 | 9000 | 连接级预览保留原默认端口；指定 26.8.31.2 的 `query_analytics` 使用原生 TLS 端口 9440 |
| OLAP | Doris | MySQL 协议 | 9030 | 同上 |
| OLAP | StarRocks | MySQL 协议 | 9030 | 同上 |
| 向量 | Milvus | Milvus 客户端协议 | 19530 | 旧连接级预览保留；指定 2.6.25 TLS 单机可用 `search_vectors` 受控检索 |
| 向量 | Qdrant | Qdrant 客户端协议 | 6334 | 同上 |
| 向量 | Weaviate | HTTP API | 8080 | 同上 |

上表 21 个名称、类别和数字端口已逐项对照 `internal/model/datasource_types.go`。另 9 个关系型注册项为 `postgres:5432`、`vastbase:5432`、`highgo:5866`、`opengauss:5432`、`mysql:3306`、`dm:5236`、`oracle:1521`、`yashan:1688`、`sqlserver:1433`；合计 30 款。`timescaledb` 在注册表中归于时序类，仍使用 PostgreSQL 协议。注册端口是默认配置值，不保证该值就是适配器实际连接的端口：

- HBase 注册端口为 `16020`，但当前 `gohbase` 适配器将它作为 region-server 端口拒绝；需显式配置 ZooKeeper bootstrap 端口（代码提示通常为 `2181`），不能直接用注册默认值完成连接。
- Couchbase 注册端口为 `11210`；适配器在该默认值下分别使用 HTTP 管理端口 `8091` 和查询端口 `8093`，配置 TLS 时改用 `18091` / `18093`。覆盖注册端口时按 `couchbaseURLs` 的映射规则计算，不能把 `11210` 当成 HTTP 端口。

## 命令边界

适配器按数据源分别执行命令白名单和参数校验。例如 Redis/Valkey 的只读子集包括 `GET`、`MGET`、`SCAN`、`INFO`；Memcached 包括 `GET`、`MGET`、`STATS`；Cassandra/Scylla 包括受限的 `SELECT`、`LIST TABLES`；ClickHouse、Doris、StarRocks 包括受限的 `SELECT`、`SHOW`、`DESCRIBE`。Milvus、Qdrant、Weaviate 有各自受限的 `GET`、`DESCRIBE`、`SEARCH` 等命令。图、文档、时序适配器还有各自的语法与边界，具体以对应 `businessdb` 适配器代码为准。

写命令如 Redis `SET`、Qdrant `UPSERT` 在只读连接中拒绝。白名单之外、超限、不安全的语法也拒绝。适配器中的原生查询审计仅记录命令决定，不记录键、参数值或凭证。

## Redis / Valkey 的 Agent MCP 受控路径

v2.6.0 新增的 `read_key` 与上面的管理员原生命令预览分离。只在 Redis 8.2.9 或 Valkey 8.1.10、严格 TLS、ACL 账号和指定单实例上开放；每次请求固定数据库与当前连接配置，并重验 Agent/租户和 `key` 策略。策略 `dbN:exact-key` 或 `dbN:bounded-prefix*` 与 SQL 表权限互不替代，拒绝优先。请求只接受精确 ASCII 键名，固定调用 `TYPE` 与有界 `GETRANGE`；超过 64 KiB、非 UTF-8、非字符串、通配符、危险命令和跨库操作均拒绝。按键值来源应用脱敏，持久预检与终态审计使用 HMAC 键标识。独立审计库或业务库故障时不交付值；详情和未测范围见[v2.6.0 验收](v2.6.0-acceptance.md)。

## ClickHouse 的 Agent MCP 受控路径

v2.7.0 的 `query_analytics` 与管理员原生命令预览分离，只针对 ClickHouse 26.8.31.2、严格 TLS、`readonly=1` 最小权限角色和指定单节点。网关逐次核验精确 `database.table` 表授权及显式列白名单（包含过滤列），只读取 MergeTree 表的显式列和 String 等值过滤，返回 1–100 行；服务器生成参数化 SQL，附加查询时间、读行/字节、内存、输出行及线程上限，结果还受 1 MiB 和 UTF-8 边界约束。脱敏和持久预检/终态审计成功后才交付。任意 SQL、视图、Distributed、表函数、JOIN、聚合、写入、跨库和完整生产支持未开放；证据见[v2.7.0 候选验收](v2.7.0-acceptance.md)。

## Milvus 的 Agent MCP 受控路径

v2.8.0 的 `search_vectors` 与管理员原生命令预览分离，只针对 Milvus 2.6.25、`default` 数据库、严格 TLS、已认证的非管理员账号和指定单节点。网关逐次核验精确 `default.collection` 表授权及向量字段、全部输出字段的列白名单。请求只接受一个有限 FloatVector、1–20 的 `top_k` 与显式标量字段；服务器核对维度与 COSINE 索引，固定调用无过滤表达式的 Search。结果受时限、命中数和 1 MiB 边界约束，来源字段脱敏及持久预检/终态审计成功后才交付。混合检索、任意表达式、写入、集群与完整生产支持未开放；证据见[v2.8.0 候选验收](v2.8.0-acceptance.md)。

## 控制台与 HTTP API

- `GET /api/v1/datasource-types` 返回 30 款类型及能力档位；NoSQL 为 `nosql-connect`。
- 保存 NoSQL 数据源后，可用 `POST /api/v1/datasources/{id}/native-ping` 测试连接。响应提供成功状态、耗时；连接成功且适配器能读取版本时，`version` 填入服务端版本，否则为空。
- `GET /api/v1/datasources/{id}/native-schema` 通过只读网关调用适配器的 `Discover`，最多返回 100 个命名空间，并限制字段与响应字节数；截断时 `truncated=true`。无该能力返回 `NATIVE_SCHEMA_UNSUPPORTED`，连接或枚举失败返回 `NATIVE_SCHEMA_FAILED`。
- `POST /api/v1/datasources/{id}/native-query` 只开放适配器白名单内的只读命令，默认 `read_only=true`。预览最多返回 100 行、64 KiB 数据；截断时 `truncated=true`。写命令和 `read_only=false` 均返回 403 并写入管理审计；未知命令、超限参数在执行前拒绝。适配器仍执行自己的参数及语法校验。查询审计只记录命令和决定，不保存键、参数值或凭证。

本地验证示例：登录控制台后创建 `redis` 数据源，主机填写本地可达地址，端口默认 `6379`，保存后点击“测试连接”。连接结果取决于实际服务与凭证；无需真实容器也可通过 `go test -short ./internal/adminapi/...` 的 mock HTTP 测试验证路由行为。
