# NoSQL 数据源支持范围

AgentSQL 对以下 21 款 NoSQL 数据源提供**连接级/低阶支持，非完整检索/管理能力**。适配器实现了连接、健康检查、元数据枚举和受控命令子集；各能力受服务版本、凭证权限、请求边界及适配器白名单限制。这里的“支持”不代表所有原生命令、检索语法、管理操作或厂商认证均可用。

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
| 宽列 | HBase | HBase 客户端协议 | 16020 | 同上 |
| 图 | Neo4j | Bolt | 7687 | 同上 |
| 图 | JanusGraph | Gremlin Server | 8182 | 同上 |
| 图 | NebulaGraph | nGQL 客户端协议 | 9669 | 同上 |
| 时序 | InfluxDB | HTTP API | 8086 | 同上 |
| 时序 | Prometheus | HTTP API | 9090 | 同上 |
| 时序 | TimescaleDB | PostgreSQL 协议 | 5432 | 同上 |
| OLAP | ClickHouse | 原生客户端协议 | 9000 | 同上 |
| OLAP | Doris | MySQL 协议 | 9030 | 同上 |
| OLAP | StarRocks | MySQL 协议 | 9030 | 同上 |
| 向量 | Milvus | Milvus 客户端协议 | 19530 | 同上 |
| 向量 | Qdrant | Qdrant 客户端协议 | 6334 | 同上 |
| 向量 | Weaviate | HTTP API | 8080 | 同上 |

端口取自 `internal/model/datasource_types.go`，创建连接时可覆盖。`timescaledb` 在注册表中归于时序类，仍使用 PostgreSQL 协议。

## 命令边界

适配器按数据源分别执行命令白名单和参数校验。例如 Redis/Valkey 的只读子集包括 `GET`、`MGET`、`SCAN`、`INFO`；Memcached 包括 `GET`、`MGET`、`STATS`；Cassandra/Scylla 包括受限的 `SELECT`、`LIST TABLES`；ClickHouse、Doris、StarRocks 包括受限的 `SELECT`、`SHOW`、`DESCRIBE`。Milvus、Qdrant、Weaviate 有各自受限的 `GET`、`DESCRIBE`、`SEARCH` 等命令。图、文档、时序适配器还有各自的语法与边界，具体以对应 `businessdb` 适配器代码为准。

写命令如 Redis `SET`、Qdrant `UPSERT` 在只读连接中拒绝。白名单之外、超限、不安全的语法也拒绝。适配器中的原生查询审计仅记录命令决定，不记录键、参数值或凭证。

## 控制台与 HTTP API

- `GET /api/v1/datasource-types` 返回 27 款类型及能力档位；NoSQL 为 `nosql-connect`。
- 保存 NoSQL 数据源后，可用 `POST /api/v1/datasources/{id}/native-ping` 测试连接。响应提供成功状态、耗时；连接成功且适配器能读取版本时，`version` 填入服务端版本，否则为空。
- `GET /api/v1/datasources/{id}/native-schema` 通过只读网关调用适配器的 `Discover`，最多返回 100 个命名空间，并限制字段与响应字节数；截断时 `truncated=true`。无该能力返回 `NATIVE_SCHEMA_UNSUPPORTED`，连接或枚举失败返回 `NATIVE_SCHEMA_FAILED`。
- `POST /api/v1/datasources/{id}/native-query` 只开放适配器白名单内的只读命令，默认 `read_only=true`。预览最多返回 100 行、64 KiB 数据；截断时 `truncated=true`。写命令和 `read_only=false` 均返回 403 并写入管理审计；未知命令、超限参数在执行前拒绝。适配器仍执行自己的参数及语法校验。查询审计只记录命令和决定，不保存键、参数值或凭证。

本地验证示例：登录控制台后创建 `redis` 数据源，主机填写本地可达地址，端口默认 `6379`，保存后点击“测试连接”。连接结果取决于实际服务与凭证；无需真实容器也可通过 `go test -short ./internal/adminapi/...` 的 mock HTTP 测试验证路由行为。
