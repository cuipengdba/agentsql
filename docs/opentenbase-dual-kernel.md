# OpenTenBase 双内核深化记录（v0.5 批十二）

> 实测日期：2026-10-02。本文的“通过”只覆盖列出的镜像、拓扑、版本与合成用例，不构成厂商认证或生产兼容性声明。未获得真实环境证据的路径明确标为“待官方环境实测/未实现”。

## 1. 结论与边界

| 路径 | 本批结论 | AgentSQL 接入边界 |
| --- | --- | --- |
| OpenTenBase PostgreSQL 内核 | **指定 v2.5.0 容器和单机 GTM/CN/DN 拓扑下，最小安全闭环实测通过** | 复用 `postgres` dialect；只对精确 `server_version = 10.0 OpenTenBase V2` 启用有限计划规范化；未知版本、节点或结构 fail-closed |
| TXSQL/MySQL 内核 | **待官方环境实测，未实现产品专用代码** | TXSQL 是独立 MySQL 内核/仓库，不是 PG 镜像的另一启动模式；候选路径是复用 `mysql` dialect，但必须先通过真实 TXSQL 版本、目录、计划、取消及完整安全闭环验证 |

本批没有修改 `executor.go`：没有证据支持注册新的 `txsql` 数据源类型，也没有把普通 MySQL 8.0 的测试结果冒充 TXSQL 结果。

## 2. PostgreSQL 内核环境与原始证据

测试镜像为第三方 `domainlau/opentenbase:v2.5.0`。容器 `otb-v05` 中实测进程和端口如下：

- GTM：`20001`；
- CN：SQL `11000`、pooler `11001`；
- DN：SQL `21000`、pooler `21001`；
- `pgxc_node`：`gtm/G/20001`、`cn001/C/11000`、`dn001/D/21000`；
- `version()`：`PostgreSQL 10.0 OpenTenBase V2 ... 64-bit`；
- `SHOW server_version`：`10.0 OpenTenBase V2`。

现有合成表 `public.agentsql_v05_customers` 有 `id/name/phone/email` 四列、两行虚构数据和主键，`\d+` 明确显示：

```text
Distribute By: SHARD(id)
Location Nodes: ALL DATANODES
```

远端查询的 `EXPLAIN (FORMAT JSON)` 根节点为 `Remote Fast Query Execution`，真实输出仍在以下两项之间缺少唯一逗号：

```text
"Node/s": "dn001"
"Remote plan": [
```

CN 本地查询则不走远端包装。例如 `SELECT 1` 返回合法的标准 PostgreSQL JSON，根节点是 `Result`；`SELECT pg_sleep(1) LIMIT 1` 返回合法的 `Limit -> Result`。因此按版本选择 OpenTenBase 解析器后，解析器必须同时接受两种**已验证**形态，而不能把所有合法本地计划误拒绝。

官方 OpenTenBase 文档说明标准集群由 GTM、Coordinator 和 DataNode 构成，客户端连接 Coordinator；本次单容器拓扑只是兼容性冒烟，不是生产部署建议。依据：[OpenTenBase 官方仓库](https://github.com/OpenTenBase/OpenTenBase)、[官方 Quick Start](https://docs.opentenbase.org/en/guide/01-quickstart/)。

## 3. PostgreSQL 计划适配设计与实现

适配入口保持在 `PostgresExecutor.explainWithRunner`，只有 `SHOW server_version` 精确匹配 `10.0 OpenTenBase V2` 才进入 OpenTenBase 分支。普通 PostgreSQL 和未知 OpenTenBase 版本不做文本修复。

本批在既有缺逗号修复上增加以下边界：

1. 计划中的 `Plan Rows` 和 `Total Cost` 改为必填证据；缺失、负数、非有限值或越界均拒绝。
2. 对远端计划，只允许一个真实样本中的缺逗号；修复后必须成为合法 JSON。
3. 远端包装器必须是 `Remote Fast Query Execution`，必须包含合法 `Node/s`、恰好一个 `Remote plan`，且不能混入普通 `Plans`。
4. CN 本地计划必须从一开始就是合法 JSON，不能借用缺逗号修复；本地根节点及子节点进入同一节点白名单和估算校验。
5. 嵌套 `Remote plan`、未知节点、超过 1024 个节点、超过 512 KiB、非法 UTF-8、未知版本全部 fail-closed。
6. `ExplainInfo.Raw` 只保存汇总，不回传远端计划中的条件或对象文本。

新增测试覆盖：真实缺逗号样本、OpenTenBase 本地标准 PG JSON、缺失估算、缺少 `Node/s`、非法嵌套远端计划、两个缺逗号候选、未知节点、未知版本、超大载荷和超过节点上限的深计划。既有普通 PostgreSQL JSON 测试继续覆盖标准解析路径。

## 4. PostgreSQL 内核真实闭环

使用当前源码构建临时网关镜像，以独立网关容器和独立数据卷连接 `otb-v05`。数据库侧创建临时最小只读角色，只授予目标库连接、`public` usage 和目标表 select；CN HBA 只增加 Docker 主机地址 `172.17.0.1/32` 的角色专用 `md5` 规则，未开放网段。

| 步骤 | 真实结果 |
| --- | --- |
| Ping | 通过，33 ms |
| discovery | 通过；扫描 1 表、4 列、4 个样本值，产生 3 个发现；管理审计 ID 1 |
| R006 | 通过；带 `/* compat-b12 */` 注释的查询被拒绝；审计 ID 2 |
| 远端 EXPLAIN + 允许查询 | 通过；缺逗号计划被严格规范化，`est_scan_rows=120`，返回 2 行，数据库查询耗时 8 ms；审计 ID 3，`decision=allow`、`rows_returned=2` |
| 脱敏 | 通过；phone/email 共 4 个单元格被处理，结果为 `138****8000`、`139****9000`、`a***@example.com`、`b***@example.com` |
| 本地标准 JSON | 通过；`SELECT 1 AS local_probe` 返回 1 行，`est_scan_rows=1`；审计 ID 6 |
| 审计 | 通过；discovery、拒绝、允许、取消错误均可从管理审计接口读取 |
| 取消语义 | 通过；短时 `ACCESS EXCLUSIVE` 锁制造可控等待，客户端 750 ms 取消后于 795 ms 收到 `TaskCanceledException`；1.2 秒后该角色活动后端数为 0；审计 ID 5 为 `decision=error`、`error_code=DB_QUERY_TIMEOUT` |

取消用例验证的是请求上下文到 PG CancelRequest/连接处理的当前链路。它不等价于网络分区、CN 故障、GTM 故障或多 DN 故障恢复测试。

## 5. TXSQL/MySQL 内核调研

### 5.1 实际形态

官方资料把两个内核分开发布：下载页分别列出 `OpenTenBase` 和 `TXSQL`；组织下也存在独立的 `OpenTenBase/OpenTenBase` 与 `OpenTenBase/TXSQL` 仓库。[官方下载页](https://docs.opentenbase.org/en/download/)、[TXSQL 官方仓库](https://github.com/OpenTenBase/TXSQL)。

TXSQL 官方仓库当前说明其基于 MySQL 8.0.30；官方概述称其为兼容原生 MySQL 的企业级 MySQL 内核。官方部署页仍展示 8.0 分支构建和 `8.0.22-txsql-...` 示例，说明仓库与文档示例存在版本差异，接入时必须固定目标提交/版本，不能只写“TXSQL 8.0”。[TXSQL 概述](https://docs.opentenbase.org/en/guide/16-txsql_quickstart/)、[TXSQL 部署](https://docs.opentenbase.org/en/guide/18-txsql_deploy/)。

官方基础使用通过 `mysql -h host -P port -u user -p` 连接，证明其使用 MySQL 客户端协议；文档没有承诺一个 TXSQL 专用固定网络端口，部署示例甚至使用 mtr 启动后通过 Unix socket 连接。因此端口必须来自目标环境配置，不能在 AgentSQL 中硬编码。[TXSQL 基础使用](https://docs.opentenbase.org/guide/20-txsql_basic/)。

### 5.2 本地可运行性结论

本批环境中：

- `domainlau/opentenbase:v2.5.0` 内没有 `mysqld`、`mysql` 或 `txsql` 可执行文件；
- 本地只有普通 MySQL 8.x 镜像，没有 TXSQL 镜像；
- 官方部署文档要求从 TXSQL 源码构建，列出的目标环境为 CentOS 7.8/7.9、TencentOS Server 或银河麒麟 V10，并以 mtr 作为体验启动方式；
- 官方 GitHub 页面没有提供本批可直接拉起并与文档版本绑定的容器发布物。

因此本批将 TXSQL 标记为 **待官方环境实测/未实现**。普通 `mysql:8` 只能验证 AgentSQL 的现有 MySQL 路径，不能作为 TXSQL 通过证据；本批没有为未运行的产品写适配代码。

### 5.3 候选接入路径与 fail-closed 清单

在厂商或官方可复现环境到位后，优先验证复用现有 `mysql` executor，而不是预先新增注册项：

1. 固定 TXSQL 版本/提交、部署方式、监听地址和网络端口；以 `SELECT VERSION()` 保存能力证据，但不只凭版本字符串放宽解析。
2. 使用现有 MySQL 驱动做 TLS、认证、Ping、context cancellation、连接池和只读会话验证。
3. 逐项验证 `information_schema.tables/columns/statistics`、`DATABASE()`、表行数估算和最小只读权限；任何目录差异都单独适配并加版本门槛。
4. 捕获真实 `EXPLAIN` 列名、类型和值。只有包含现有标准路径所需的 `type/key/rows` 且行宽、行数、估算与载荷上限均合法时才复用；任何 TXSQL 专用格式必须以真实 fixture、精确版本和节点白名单新增分支。
5. 跑 Ping → discovery → R006 → 允许查询 → 脱敏 → 审计 → 取消的同一闭环；再覆盖事务、字符集/排序规则、错误码、XA/强同步特性对会话语义的影响。
6. 未知版本、目录权限不足、未知计划列/节点、估算溢出、取消后连接状态不明一律 fail-closed；不得跳过 EXPLAIN 风控后执行。

只有上述链路在真实 TXSQL 上完成，才考虑是否增加显式 `txsql` capability/别名。若只是标准 MySQL 协议和目录，继续使用 `db_type=mysql` 更少引入未经验证的分叉。

## 6. 已实现、未实现与后续建议

已实现：

- OpenTenBase V2 远端缺逗号计划与 CN 本地标准 JSON 的严格双形态解析；
- 必填估算、远端包装器、节点数量、嵌套远端计划与节点白名单校验；
- 单元测试和指定容器上的完整 PG 最小安全闭环。

未实现/待实测：

- TXSQL 本地实例、TXSQL 专用版本探测或计划适配；
- 多 CN、多 DN、复制、故障转移、扩缩容和生产 TLS；
- 未列入节点白名单的 OpenTenBase 计划形态；出现时应先保存脱敏 fixture，再以小批次评审扩展。

建议下一小批由 OpenTenBase/TXSQL 官方提供固定版本二进制或镜像、校验和、启动配置和支持矩阵；先只做只读合成数据闭环。PG 路径下一步补多 DN 真实计划样本与故障注入，不要通过放宽当前白名单来预判格式。
