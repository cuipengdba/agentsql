# 国产数据库适配预研

> 文档状态：预研与实验性协议路径实测，不是兼容性认证或支持声明。2026-10-01 的实测结论仅适用于下文列出的社区版本、拓扑与用例；商业版本及未验证项仍需厂商提供与目标版本一致的测试环境、驱动和文档后确认。

AgentSQL 当前原生支持 PostgreSQL 与 MySQL。本预研用于拆分国产数据库接入工作、安排联合验证顺序，不应作为“已支持”或“已完成适配”的对外依据。即使数据库宣称兼容 PostgreSQL、MySQL 或 Oracle，也不能直接推导出其 SQL 方言、系统目录、驱动行为和安全能力与对应数据库完全一致。

## 0. 首批适配状态与实测记录（2026-10-01）

### 0.1 统一状态口径

| 厂商 / 产品线 | 本轮状态 | 结论边界 |
| --- | --- | --- |
| 电科金仓 KingbaseES 商业版 | **协议层映射，待厂商环境终验** | 官方资料显示存在 PostgreSQL 兼容模式，但本轮没有合法可用的商业版环境；建议映射到现有 PG 路径后逐项验证，不写“已支持” |
| 瀚高 HighGo 商业版 | **协议层映射，待厂商环境终验** | 本轮没有商业版环境；IvorySQL 社区版的结果只能作为同厂商社区产品的工程参考，不能替代 HighGo 终验 |
| IvorySQL 5.3（PG18） | **社区版协议路径实测通过** | 在 PG 入口完成最小闭环；这是指定镜像和用例的实验性结果，不是 HighGo 商业版结论或官方兼容认证 |
| openGauss 7.0.0-RC3 | **社区版协议路径实测通过** | 以 `db_type=postgres` 完成最小闭环；GaussDB 商业版仍为待厂商环境终验 |
| 腾讯 TDSQL 商业版 | **协议层映射，待厂商环境终验** | 必须先锁定 TDSQL PostgreSQL 版或 TDSQL MySQL 版，再分别映射 PG/MySQL 路径；不同产品线的结论不能互相替代 |
| OpenTenBase v2.5.0（PG 内核） | **部分实测，待进一步适配** | 单机 GTM/CN/DN 已初始化；连接、发现、规则拒绝与审计成功，但允许查询在 AgentSQL 执行器返回 `AUTH_DATABASE_ERROR`，闭环未通过 |
| OpenTenBase 的 TXSQL/MySQL 路径 | **规划中，本轮未评估** | 属于另一内核/项目，本轮只评估 OpenTenBase PG 内核 |
| 达梦 DM | **需独立评估** | 当前不在 PG/MySQL 适配路径内；按 Oracle 兼容方向独立评估，不做硬映射 |

“社区版协议路径实测通过”只表示下面的合成数据冒烟闭环通过；不代表厂商认证、完整 SQL 方言兼容、生产可用性或商业版支持。首批没有新增 TiDB 7.5.1 与 OceanBase CE 4.4.2.1 的端到端证据；第二批补测结果见 0.6，其中两者均在 `EXPLAIN` 结果解析阶段失败，不能追加通过声明。

### 0.2 实测基线

- AgentSQL：从当前仓库源码构建，版本标记 `v0.5.0-compat-research`；MCP 使用无状态 HTTP 调用。
- 安全开关：`column_authorization.enabled=false`，因此本轮验证的是现有表级策略与结果脱敏，不声称 B2 列级授权通过；B2 健康状态为 `feature-off`、协议号 2。
- 合成表：`public.agentsql_v05_customers(id, name, phone, email)`，2 行虚构数据；手机与邮箱各配置一条精确脱敏规则。
- 允许 SQL：`SELECT id,name,phone,email FROM public.agentsql_v05_customers WHERE id > 0 ORDER BY id LIMIT 10`。
- 拒绝 SQL：在受控 `SELECT` 后附加注释，命中内置规则 R006，用来证明规则拒绝和审计链路；不把该规则用例解释为数据库自身能力。
- 网关首次启动拒绝公开示例密钥，原文为 `AGENTSQL_SECRET uses a publicly known example value and is refused; generate a unique 32-byte value; for local development/testing set AGENTSQL_INSECURE=1`。本轮仅在隔离本机测试容器使用 `AGENTSQL_INSECURE=1`；生产与联合验收不得使用该绕过。

### 0.3 最小闭环结果

| 数据库 | 连接 / Ping | discovery | 规则命中 | 允许查询 | 列级授权或脱敏 | 审计落库 | 本轮结论 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| openGauss 7.0.0-RC3，端口 15432，库 `postgres` | 通过，35 ms | 通过：1 表、4 列、4 个采样值；识别 phone/email，两类均 2/2 命中 | 通过：R006 拒绝 | 通过：2 行 | 结果脱敏通过：4 个单元格；手机如 `138****8000`，邮箱如 `a***@example.com`；B2 未启用 | 通过：拒绝/允许审计 ID 4/5，允许记录 `rows_returned=2` | **社区版协议路径实测通过** |
| IvorySQL 5.3（PostgreSQL 18.3），端口 5434，库 `postgres` | 通过，33 ms | 通过：与上表相同 | 通过：R006 拒绝 | 通过：2 行 | 结果脱敏通过：4 个单元格；B2 未启用 | 通过：拒绝/允许审计 ID 6/7，允许记录 `rows_returned=2` | **社区版协议路径实测通过** |
| OpenTenBase v2.5.0，CN 端口 11000，库 `postgres` | 通过，237 ms | 通过：1 表、4 列、4 个采样值；识别 phone/email，两类均 2/2 命中 | 通过：R006 拒绝 | **未通过**：决策进入 `error`，审计码 `GATEWAY_INTERNAL`，网关稳定错误详情 `AUTH_DATABASE_ERROR` | 未到结果阶段，不能声明脱敏通过；B2 未启用 | 通过：规则拒绝/执行错误审计 ID 13/14 | **部分实测，待进一步适配** |

IvorySQL 的 discovery 第一次紧邻前一请求时触发全局限流，原文为 `{"code":429,"msg":"敏感发现请求过于频繁","data":{"error_code":"DISCOVERY_RATE_LIMITED"}}`；等待约 1.2 秒重试成功。这是 AgentSQL 发现接口限流行为，不是数据库不兼容。

openGauss 容器内自带 `gsql` 无法启动，原文为 `error while loading shared libraries: libssl.so.3: cannot open shared object file: No such file or directory`。本轮改用 IvorySQL 容器内的 `psql` 通过外部 TCP 建表和核验，AgentSQL 网关本身仍直接连接 openGauss。实测服务端版本原文为 `(openGauss 7.0.0-RC3 build 01b7e318) ... GCC 10.3.0, 64-bit`；IvorySQL 为 `PostgreSQL 18.3 (IvorySQL 5.3) ... 64-bit`。

### 0.4 OpenTenBase 初始化、阻碍与可复现边界

镜像 `domainlau/opentenbase:v2.5.0` 实测为 Ubuntu 22.04 安装包型镜像：默认用户 `opentenbase`，工作目录 `/var/lib/opentenbase`，包含 `gtm`、`initgtm`、`initdb`、`pg_ctl`、`pgxc_ctl`、`postgres`、`psql`，无自动初始化入口，默认命令为 `/bin/bash`。本轮在单容器中启动 1 个 GTM、1 个 CN、1 个 DN：GTM 20001；CN 11000、pooler 11001；DN 21000、pooler 21001。该拓扑只用于兼容性冒烟，不代表生产部署建议。

官方资料中，OpenTenBase 的标准快速开始使用 `opentenbase_ctl` 安装 GTM/CN/DN；组件文档也给出带 `--master_gtm_nodename`、`--master_gtm_ip`、`--master_gtm_port` 的 CN 初始化方式。本轮镜像没有 `opentenbase_ctl` 配套入口，因此按组件工具完成最小初始化。依据：[OpenTenBase Quick Start](https://docs.opentenbase.org/en/guide/01-quickstart/)、[组件安装及管理](https://docs.opentenbase.org/guide/05-component/)、[v2.5.0 发布说明](https://docs.opentenbase.org/en/release/v2-5-0/)。

关键步骤与实际差异如下：

1. 挂载数据卷后先将目录所有者设为容器内 UID/GID 1000；否则 `initgtm` / `initdb` 原文为 `could not create directory ... Permission denied`。
2. `initgtm -Z gtm` 初始化并启动 GTM。`gtm_ctl -w` 在已启动后仍停在 `waiting for server to start`，本轮通过进程、端口与 GTM 日志三项确认后继续，不能只依赖包装器等待状态。
3. 初次 `initdb` 未传 GTM 主节点参数时失败，原文包含 `FATAL: syntax error at or near "("` 和 `create gtm node (null) with (type='gtm', host='(null)',port=(null), primary=1);`。补充 `--master_gtm_nodename=gtm --master_gtm_ip=127.0.0.1 --master_gtm_port=20001` 后，CN/DN 初始化成功。
4. 启动参数直接加入 `gtm_host` 时失败，原文为 `FATAL: unrecognized configuration parameter "gtm_host"`；去掉运行时参数，使用 `initdb` 已持久化的 GTM 信息后启动成功。
5. 在 CN 与 DN 双向登记节点，CN 创建 `DEFAULT NODE GROUP` 与 `SHARDING GROUP`，再创建 `DISTRIBUTE BY SHARD(id)` 的合成表。实测 `version()` 为 `PostgreSQL 10.0 OpenTenBase V2 ... 64-bit`，CN 直连查询成功；`EXPLAIN (FORMAT JSON)` 命令能够返回以 `Remote Fast Query Execution` 为根节点的文本，但第二批复核确认该文本在 `"Node/s": "dn001"` 与 `"Remote plan"` 之间缺少逗号，不是合法 JSON。
6. 镜像初始 HBA 只有本地 trust。网关首次连接原文为 `FATAL: no pg_hba.conf entry for host "172.17.0.1", user "opentenbase", database "postgres", SSL off`。本轮没有开放整个网段，而是创建最小只读测试角色，并仅为 Docker 主机地址、目标库和该角色添加 `/32` 的 `md5` 规则。
7. 初次启动出现审计/维护日志目录缺失风暴，原文为 `could not open audit log file "log/audit/audit-Thursday-06.log": No such file or directory` 和 `could not open audit log file "pg_log/maintain/maintain-Thursday-06.trace": No such file or directory`。创建 CN/DN 对应目录并重载后，重复拉起停止；后续仍观察到镜像内 2PC 清理函数缺失日志，应在正式拓扑继续核验。

允许查询失败并非数据库账号、PG 协议或查询结果读取失败：相同只读账号从 CN 直连可返回 2 行，开启 `default_transaction_read_only=on` 后仍可返回；第二批使用与 AgentSQL 相同的 pgx v5.9.2、1 MiB 前端帧上限和运行参数复现时，`Ping` 与直接 `Query` 也都返回 2 行。根因已经定位到查询前的计划评估：OpenTenBase 返回的 `EXPLAIN (FORMAT JSON)` 文本缺少逗号，AgentSQL 的固定 PostgreSQL JSON 计划解析失败。执行器级原始错误为 `parse PostgreSQL explain result: decode JSON plan: invalid character '"' after object key:value pair`，网关外层仍按安全设计只暴露 `AUTH_DATABASE_ERROR`。AgentSQL 处于 B2 `feature-off`，所以该问题与 B2 无关；在实现兼容分支并完成回归前，状态保持“待进一步适配”，不得写成实测通过。

### 0.5 商业版协议映射方案

仓库当前没有厂商识别开关：`Datasource.DBType`、parser、`openExecutor`、discovery 只接受 `postgres` / `mysql`，B2 又只对精确的 `DBType == "postgres"` 探测。因而本轮没有增加 `kingbase`、`highgo`、`tdsql` 等别名，也没有修改核心网关；仅加别名会掩盖系统目录、版本、类型和安全能力差异，并形成虚假支持声明。

| 厂商 / 产品 | 建议映射路径 | 厂商环境终验清单 |
| --- | --- | --- |
| KingbaseES 商业版 | 仅在厂商确认目标实例为 PG 兼容模式后映射到 PG parser / executor / discovery；KingbaseES 官方手册列出 `pg`、`oracle`、`mysql` 等初始化兼容模式，不能只凭产品名选择路径 | 产品完整版本与兼容模式；官方推荐 Go/PG 驱动及许可证；认证/TLS/DSN；`version()`/`server_version_num`；`pg_catalog`、`information_schema`、OID/类型；标识符大小写；`EXPLAIN JSON`；超时/取消；发现、规则、允许/拒绝、脱敏、审计；B2 catalog/binder 与故障关闭 |
| HighGo 商业版 | 建议从 PG 路径开始；IvorySQL 5.3 的结果只作为社区版参考，不继承为商业版结论 | 商业版完整版本、内核基线与兼容模式；官方驱动；TLS/认证；catalog/OID/扩展类型；标识符；`EXPLAIN`；取消/连接池；完整安全闭环与 B2 适用性 |
| TDSQL PostgreSQL 版 | 映射到 PG 路径，但按分布式数据库单列能力矩阵；OpenTenBase v2.5.0 的部分实测不能替代商业版 | 商业产品全称/版本/拓扑；CN/代理入口；驱动；路由与分布式事务；catalog、计划、类型、错误码、取消；安全闭环；B2 的 PG14--18 版本门槛与 catalog 假设 |
| TDSQL MySQL 版 / TXSQL | 映射到 MySQL parser / executor / discovery；与 TDSQL PostgreSQL 版分开登记 | 产品全称/版本/拓扑；MySQL 协议/驱动；`information_schema`；分片路由与事务；`EXPLAIN`；类型/字符集；规则、脱敏、审计。现有 B2 不支持 MySQL，列级授权需另行设计 |
| 达梦 DM | 不映射到 PG/MySQL | 当前不在 PG/MySQL 适配路径内，需按 Oracle 兼容特征独立评估 parser、驱动、目录、类型、授权与脱敏 |

公开资料只作为确定测试入口的依据，不作为 AgentSQL 通过证据。KingbaseES 官方应用参考手册列出了多兼容模式：[KingbaseES 服务器应用参考手册](https://help.kingbase.com.cn/v8.6.8.14/PDF/KingbaseES%E6%9C%8D%E5%8A%A1%E5%99%A8%E5%BA%94%E7%94%A8%E5%8F%82%E8%80%83%E6%89%8B%E5%86%8C.pdf)。IvorySQL 官方文档说明其基于 PostgreSQL、具有 PG/Oracle 模式与双入口：[IvorySQL 5.3 文档](https://docs.ivorysql.org/en/ivorysql-doc/v5.3/welcome.html)、[IvorySQL 框架设计](https://docs.ivorysql.org/en/ivorysql-doc/v5.4/7.1)。腾讯官方分别维护 [TDSQL PostgreSQL 版](https://cloud.tencent.com/document/product/1129) 与 [TDSQL MySQL 版](https://cloud.tencent.com/product/dcdb) 文档，因此必须分线映射和终验。

### 0.6 第二批实测记录（2026-10-01）

#### 0.6.1 环境、口径与统一用例

- 宿主环境：Windows、Docker Client/Server 29.8.0（Docker Desktop 4.92.0），连接 `npipe:////./pipe/docker_engine`。AgentSQL 镜像版本标签为 `v0.5.0-compat-research`，MCP 协议 `2025-06-18`，无状态 HTTP；`column_authorization.enabled=false`，所以本节只验证表级策略与结果脱敏，不声称 B2 列级授权通过。
- 安全边界：网关继续使用公开示例密钥，因而只在隔离本机容器设置 `AGENTSQL_INSECURE=1`。生产、厂商联合验收和任何非隔离环境不得使用该绕过；本节未记录测试口令。
- PG 路径合成表为 `public.agentsql_v05_customers(id integer primary key,name varchar(64),phone varchar(32),email varchar(128))`；MySQL 路径位于库 `agentsql_v06`，表结构相同。两者均为 Alice/Bob 两行虚构数据，并为 `phone`、`email` 各配置一条精确 `mask` 规则。
- PG 允许 SQL 为 `SELECT id,name,phone,email FROM public.agentsql_v05_customers WHERE id > 0 ORDER BY id LIMIT 10`；MySQL 允许 SQL 去掉 `public.`。拒绝 SQL 在 `SELECT` 后插入 `/* compat-v06 */`，预期由 R006 拒绝。该拒绝只证明 AgentSQL 规则链路，不是数据库自身安全能力。
- 本节中的“通过”只表示列出的镜像、端口、账号、SQL 和合成数据完成对应步骤；第三方镜像、社区版协议结果都不是厂商认证，不能外推到商业版或生产环境。达梦与 Oracle 未接入网关，不适用合成表、允许/拒绝 SQL 和审计 ID，也不得据此声称 AgentSQL 已支持。

镜像与数据库原始版本证据如下。镜像 ID 用于固定本次对象；`latest` 或第三方 tag 后续可能指向其他内容。

| 数据库 / 镜像 | 镜像 ID 与端口 | 账号 / 库或服务 | 数据库版本原文与边界 |
| --- | --- | --- | --- |
| 达梦 DM8，`dm8_single:dm8_20241022_rev244896_x86_rh6_64` | `sha256:d8dfa0b3332e...`；5236 | `SYSDBA`；实例 `DM8_TEST` | `DM Database Server 64 V8`、`DB Version: 0x7000c`、`03134284294-20241009-244896-20119`、`ID_CODE(): --03134284294-20241009-244896-20119 Pack3` |
| Oracle AI Database Free，`container-registry.oracle.com/database/free:latest` | `sha256:f988b0c04c4c...`；1521 | `SYS` / `SYSTEM`；CDB 服务 `FREE`，应用 PDB `FREEPDB1` | 用户指定查询 `SELECT version FROM v$instance` 原文为 `23.0.0.0.0`；实例 `FREE / OPEN`；`PDB$SEED / READ ONLY`、`FREEPDB1 / READ WRITE`。容器启动日志另报 `Oracle AI Database 26ai Free Release 23.26.3.0.0` |
| 瀚高 SEE，`qiuchenjun/hgdb-see:4.5.10.3` | `sha256:e4dd0ac4877c...`；5866 | `agentsql_ro`；库 `highgo` | 服务端原文 `HighGo Database Management System 4.5 on x86_64,build on 20250227`。`4.5.10.3` 仅来自第三方镜像 tag，未获得厂商镜像或厂商确认 |
| 金仓，`chyiyaqing/kingbase:v8r6` | `sha256:32146e4b8856...`；预期 54321 | 预期 `SYSTEM`；服务未启动 | 镜像环境自报 `DB_VERSION=V008R003C002B0320`，与 `v8r6` tag 不一致；无法执行 SQL 核验完整版本。启动原文见 0.6.2 |
| OpenTenBase，`domainlau/opentenbase:v2.5.0` | CN 11000、pooler 11001；GTM 20001；DN 21000、pooler 21001 | `agentsql_compat`；库 `postgres` | `PostgreSQL 10.0 OpenTenBase V2 on x86_64-pc-linux-gnu, compiled by gcc (Ubuntu 11.4.0-1ubuntu1~22.04) 11.4.0, 64-bit` |
| TiDB，现有 `tidb-test` | 4000 | `agentsql_ro`；库 `agentsql_v06` | `8.0.11-TiDB-v7.5.1`；`TiDB Server (Apache License 2.0) Community Edition, MySQL 8.0 compatible` |
| OceanBase CE，现有 `ob-test` | 2881 | `agentsql_ro@test`；库 `agentsql_v06` | `5.7.25-OceanBase_CE-v4.4.2.1`；`OceanBase_CE 4.4.2.1 (r101000022026050611-8cf64ed50606966fd5c29f47265cf557d97ea776) (Built May 6 2026 12:21:55)` |

#### 0.6.2 瀚高 SEE 与金仓旧镜像闭环

| 数据库 | 连接 / Ping | discovery | 规则命中 | 允许查询 | 列级授权或脱敏 | 审计落库 | 本轮结论 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 瀚高 SEE，第三方镜像 tag `4.5.10.3`，端口 5866，库 `highgo` | 通过，74 ms | 通过：1 表、4 列、4 个采样值；phone/email 均 2/2 命中，另将 `name` 标为低置信 generic 候选 | 通过：R006 拒绝 | 通过：2 行 | 结果脱敏通过：4 个单元格；`138****8000`、`a***@example.com` 等；B2 未启用 | 通过：discovery 审计 ID 4；拒绝/允许审计 ID 11/12，允许记录 `rows_returned=2` | **第三方镜像的 PG 协议路径实测通过；非官方、非厂商认证，商业版仍待厂商终验** |
| 金仓第三方旧镜像 tag `v8r6`，预期端口 54321 | **未通过**：初始化完成但服务启动失败 | 未执行 | 未执行 | 未执行 | 未执行 | 无 AgentSQL 审计 ID | **未实测 / 待厂商终验**；仅确认旧镜像被许可证阻断，不能形成协议层结论；官网 V9R1C10 待负责人合法下载后另测 |

瀚高镜像由 Docker Hub 个人账号发布，不是瀚高官方交付物，只用于协议层验证。为绕开镜像入口以 root 身份递归 `chown /opt/highgo` 长时间无进展的问题，本轮以镜像内 UID/GID 999 启动；初始化日志确认监听 5866。三权分立配置下，`sysdba` 向普通角色授予表权限返回原文 `ERROR: Can't grant it to other role.`，因此合成表由测试角色直接创建并持有；这不是生产授权方案。商业版仍需厂商提供正式镜像、完整版本、驱动/TLS/认证矩阵和最小权限设计后重跑。

金仓镜像同样由 Docker Hub 个人账号发布，创建于 2021-11-01，且 tag 与镜像内版本环境不一致。`initdb` 已完成，但 `sys_ctl` 启动原文为：

```text
sys_ctl: could not start server
FATAL:  XX000: License file expired.
LOCATION:  PostmasterMain, postmaster.c:659
```

因此本轮没有连接、发现、规则、允许查询、脱敏或审计证据；不能把镜像启动失败写成 KingbaseES 产品不兼容，也不能把 tag 当作已核实版本。该镜像仅保留为旧协议环境线索，正式结论等待官网 V9R1C10 或厂商交付的目标版本环境。

#### 0.6.3 OpenTenBase 根因复现与建议改动

恢复单机 GTM/CN/DN 时，GTM 必须显式以 20001 启动，DN 必须显式以 21000、pooler 21001 启动；否则 CN/DN 分别出现 `GTM error, could not obtain global timestamp` 或落到默认 5432。恢复后 AgentSQL Ping 4 ms、discovery 成功（审计 ID 5），R006 拒绝成功（审计 ID 13），允许查询仍以 `AUTH_DATABASE_ERROR` 失败（审计 ID 14，审计码 `GATEWAY_INTERNAL`）。

独立复现排除了 PG 连接与普通查询读取问题：pgx v5.9.2 在 `default_transaction_read_only=on`、`statement_timeout=5000` 和 AgentSQL 的 1 MiB bounded frontend 下，`Ping` 成功，字段 OID 为 `23/1043/1043/1043`，两行均可完整读取。直接调用仓库现有执行器时，`Query` 同样返回 `rows=2, err=<nil>`；失败只发生于前置 `Explain`：

```text
otb-v06 explain: *fmt.wrapError: parse PostgreSQL explain result: decode JSON plan: invalid character '"' after object key:value pair
otb-v06 query: rows=2 err=<nil>: <nil>
```

数据库返回文本中的关键原文为：

```text
"Node/s": "dn001"
"Remote plan": [
```

两项之间缺少 JSON 逗号。根因是 OpenTenBase v2.5.0 的分布式根计划文本不满足标准 PostgreSQL JSON 计划格式，而 AgentSQL `internal/authorizedexecute/internal/businessdb/postgres.go` 的 `explainWithRunner` 固定执行 `EXPLAIN (FORMAT JSON)` 后直接 `json.Unmarshal`；不是 pgx 驱动查询失败。

建议改动清单（本批未修改源码）：

1. 在 `internal/authorizedexecute/internal/businessdb/postgres.go` 的 `explainWithRunner` / `parsePostgresExplainJSON` 周边引入明确的“PG 兼容实现能力”分支；不要对所有 `db_type=postgres` 静默修补任意非法 JSON，也不要在解析失败时跳过计划风控。
2. OpenTenBase 分支优先尝试厂商可稳定支持的结构化计划接口；若只能取得当前文本，新增严格、有限、带版本门槛的适配器，将 `Remote Fast Query Execution` 的远端子计划规范化后再进入现有 `ExplainInfo`，任何未知格式继续 fail-closed。
3. 为上述函数增加该原始缺逗号样本、标准 PostgreSQL JSON、超大/畸形计划和未知节点的单元测试；再以 CN/DN 实例回归 Ping、discovery、R006、允许查询、脱敏、审计和取消语义。
4. 在不泄露 SQL、参数、凭据或服务端自由文本的前提下，为 `DBStageExplain` 增加结构化内部诊断（数据源类型、阶段、安全错误类别、可选 SQLSTATE/驱动错误类型），外部继续保持稳定的 `AUTH_DATABASE_ERROR`。

#### 0.6.4 TiDB / OceanBase 端到端补测

| 数据库 | 连接 / Ping | discovery | 规则命中 | 允许查询 | 列级授权或脱敏 | 审计落库 | 本轮结论 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| TiDB 7.5.1，端口 4000，库 `agentsql_v06` | 通过，12 ms | 通过：1 表、4 列、4 个采样值；phone/email 均 2/2 命中，成功审计 ID 9 | 通过：R006 拒绝 | **未通过**：前置 `EXPLAIN` 结果解析失败；直接驱动查询返回 2 行 | 未到结果阶段，不能声明网关脱敏通过；B2 不支持 MySQL | 拒绝/执行错误审计 ID 15/16；错误记录 `GATEWAY_INTERNAL` | **部分实测，待适配 TiDB 计划格式** |
| OceanBase CE 4.4.2.1 MySQL 模式，端口 2881，库 `agentsql_v06` | 通过，10 ms | 通过：同一合成表与采样；phone/email 均 2/2 命中，成功审计 ID 10 | 通过：R006 拒绝 | **未通过**：前置 `EXPLAIN` 结果解析失败；直接驱动查询返回 2 行 | 未到结果阶段，不能声明网关脱敏通过；B2 不支持 MySQL | 拒绝/执行错误审计 ID 17/18；错误记录 `GATEWAY_INTERNAL` | **部分实测，待适配 OceanBase 计划格式** |

两库的普通查询以及 AgentSQL 注入的 `/*+ MAX_EXECUTION_TIME(5000) */` 查询均直接返回两行，失败点不是 MySQL 驱动、账号或超时 hint。TiDB 的 `EXPLAIN` 列为 `id, estRows, task, access object, operator info`；OceanBase 客户端只呈现 `Query Plan`。当前 `parseMysqlExplainRows` 固定要求 `type`、`key`、`rows` 三列，执行器级原始错误均为：

```text
parse MySQL explain result: MySQL EXPLAIN requires type, key, rows and at least one row
```

应在 `internal/authorizedexecute/internal/businessdb/mysql.go` 的 `explainWithRunner` / `parseMysqlExplainRows` 增加显式 TiDB、OceanBase 计划适配器和版本化 fixture；未知格式必须继续 fail-closed，不能为了跑通允许查询而跳过 `EXPLAIN` 风险评估。本批遵守禁改核心源码要求，仅记录建议。

TiDB 限流也得到实际复现：第一次以错误的 `public.agentsql_v05_customers` 范围请求返回 `403 / DISCOVERY_SCOPE_NOT_VISIBLE`（审计 ID 6），紧邻重复请求返回原文 `{"code":429,"msg":"敏感发现请求过于频繁","data":{"error_code":"DISCOVERY_RATE_LIMITED"}}`（审计 ID 7）；等待并改用正确库名 `agentsql_v06` 后成功（审计 ID 9）。这是 AgentSQL 全局 1 QPS 发现限流，不是 TiDB 数据库错误。

#### 0.6.5 达梦独立评估

本机从指定官方单机 tar 加载镜像，以 `SYSDBA_PWD=<redacted>` 覆盖口令并映射 `127.0.0.1:5236:5236` 启动；实际默认端口 5236、管理账号 `SYSDBA`、实例名 `DM8_TEST`。日志原文包含 `create dm database success. 2026-10-01 18:40:14` 与 `SYSTEM IS READY.`；`disql` 连接、版本查询成功。镜像许可证日志同时提示 `License will expire in 14 day(s) on 2026-10-15`，所以本环境只用于当日冒烟，不可作为持续 CI 或生产授权依据。

镜像实际 `dm.ini` 为 `COMPATIBLE_MODE = 0`（none），并非 Oracle 模式 2；`CASE_COMPATIBLE_MODE = 1` 只表示 Oracle 风格大小写处理。因此即便产品具有 Oracle 兼容方向，也不能用 Oracle parser 直接替代 DM 方言验证，更不能复用 PG/MySQL parser 后声称支持。

驱动方面，达梦官方 Go 指南说明其驱动实现 Go `database/sql`，驱动名为 `dm`，DSN 形如 `dm://...`，可调用 `Ping`；官方 FAQ 同时说明 Go 驱动暂未在线提供，需要从数据库安装目录的驱动包取得。依据：[达梦 Go 编程指南](https://eco.dameng.com/document/dm/zh-cn/pm/go-rogramming-guide.html)、[达梦 Go 驱动 FAQ](https://eco.dameng.com/document/dm/zh-cn/faq/faq-go-new.html)。本批只用镜像内 `disql` 完成最小 TCP/登录冒烟，没有把厂商 Go 驱动引入仓库，也未验证其许可证与可分发性。

接入建议为独立 `dm` dialect：新增受支持驱动与连接参数合同；以 DM parser corpus 覆盖分页、标识符、Oracle 兼容语法和错误失败关闭；以 `V$VERSION`、`V$DM_INI`、`SYS` 目录重新实现 discovery；单列评估数值/日期时间/LOB/二进制/自定义类型、账号/角色/对象授权、错误码与取消语义；最后再跑合成表、规则、允许/拒绝、脱敏和审计。**当前 AgentSQL 未支持达梦，本次连通性冒烟不得表述为已支持。**

#### 0.6.6 Oracle 独立评估

官方 `container-registry.oracle.com/database/free:latest` 以 `ORACLE_PWD=<redacted>` 启动并映射 `127.0.0.1:1521:1521`。实际监听端口 1521；`SYS` / `SYSTEM` 口令由 `ORACLE_PWD` 设置；CDB 服务 `FREE`，应用连接应使用已打开的 `FREEPDB1`。Oracle 官方安装资料也区分 `FREE` 根容器服务和默认 PDB 服务 `FREEPDB1`：[Oracle AI Database Free 安装指南](https://docs.oracle.com/en/database/oracle/oracle-database/26/xeinl/oracle-ai-database-free-installation-guide-linux.pdf)。

Go 驱动建议优先评估 `godror`。其项目说明实现 `database/sql/driver`，基于 ODPI-C/OCI，需要 CGO 编译器，并在运行时提供 Oracle Client 库；连接串示例使用 `host:1521/service`，生产还应使用连接池。依据：[godror README](https://github.com/godror/godror/blob/main/README.md)。本批只使用镜像内 SQL*Plus 验证服务和版本，没有把 godror 或 Oracle Client 引入 AgentSQL，也没有验证其打包、许可证、TLS/wallet 和平台部署。

Oracle 不能映射到现有 PG/MySQL 路径。建议新增独立 `oracle` dialect、parser 与 executor，分别处理 `ALL_*` / `DBA_*` / `V$*` 目录、CDB/PDB 与 service 语义、quoted identifier、空字符串为 `NULL`、`NUMBER` 精度、`DATE` / `TIMESTAMP WITH TIME ZONE`、LOB、数组/对象类型、授权与角色、错误码和取消。完成这些实现与安全闭环前，**当前 AgentSQL 未支持 Oracle；本次官方 Free 镜像的 SQL*Plus 连通性不构成支持或认证声明。**

### 0.7 第五批实测与独立评估（2026-10-02）

本批继续沿用第 0.2 节的安全口径：只使用合成数据，不修改 AgentSQL 核心生产代码，不以协议可连接替代完整兼容认证。PolarDB 以现有 `db_type=postgres` 路径完成端到端实测；崖山仅验证厂商容器、客户端和版本，并根据官方资料给出独立 dialect 设计建议，**不声明当前 AgentSQL 已支持崖山**。

#### 0.7.1 PolarDB for PostgreSQL 实测结果

镜像 `polardb/polardb_pg_local_instance:15` 已在本机存在，镜像 ID 为 `85cf47a18d84`。按 `polardb-v06` 容器名启动并将读写节点 `5432` 映射到宿主机 `127.0.0.1:15433`；初始化期间日志有 `polar_cache_trash` 目录不存在的 warning，但随后 `pg_isready` 从拒绝连接转为 `accepting connections`，版本和读写查询均正常。`SELECT version();` 实际返回：

```text
PostgreSQL 15.19 (PolarDB 15.19.5.0 build unknown) on x86_64-linux-gnu
```

测试表为 `public.agentsql_test(id integer, name varchar(100), phone varchar(32))`，插入 `Alice/13800138000` 与 `Bob/13900139000` 两行合成数据，并为网关创建仅具有目标库连接、`public` schema usage 和该表 select 权限的 `agentsql_ro` 账号。容器内直接执行 `EXPLAIN (FORMAT JSON) SELECT id,name,phone FROM public.agentsql_test ORDER BY id` 返回合法 PostgreSQL JSON 计划（`Sort -> Seq Scan`），直接查询返回 2 行。

AgentSQL 使用 `db_type=postgres`、`host.docker.internal:15433`、库 `postgres` 注册数据源 `polardb-v06`，未增加 PolarDB 别名、dialect 或识别开关。实测闭环如下：

| 闭环步骤 | 实际结果 |
| --- | --- |
| 连接 / Ping | 通过；首次冷连接 446 ms |
| discovery | 通过；扫描 1 表、3 列，`phone` 为唯一候选列，2 个采样值均命中手机号分类；管理审计 ID **26** |
| R006 规则拒绝 | 通过；带 `/* compat-v07 */` 注释的受控查询被 R006 拒绝，未进入数据库执行；审计 ID **27** |
| `EXPLAIN` 与允许查询 | 通过；AgentSQL 的前置计划评估成功，允许查询返回 2 行，`est_scan_rows=10`、数据库执行耗时 4 ms；审计 ID **28**，审计记录 `rows_returned=2` |
| 结果脱敏 | 通过；`phone` 列共 2 个单元格被掩码，结果为 `138****8000`、`139****9000` |
| 审计落库 | 通过；discovery、拒绝、允许分别落库为 ID 26、27、28，允许记录的 decision 为 `allow` |

结论：**该指定社区镜像和合成用例下，PolarDB for PostgreSQL 的现有 PostgreSQL 路径实测通过，无需新增 dialect 或厂商识别开关。** 依据不是产品名称，而是本次真实验证的 PostgreSQL wire protocol、PG 15 语法、`information_schema` discovery、合法 `EXPLAIN (FORMAT JSON)` 及完整安全闭环。该结论不外推为阿里云商业服务全部拓扑、扩展、故障切换、认证/TLS 或生产兼容认证；这些仍需在目标版本和拓扑上联合终验。

#### 0.7.2 崖山 YashanDB 独立评估

容器 `yashan-v06` 使用镜像 `yashandb:yashandb-image-23.4.1.109-linux-x86_64`，宿主机端口 1688。`docker ps` 显示容器持续运行；部署日志中 `DeployYasdbCluster` 最终由 `RUNNING` 变为 `SUCCESS`，`return_code=0`、`progress=100`、`cost=422`。客户端实际位于 `/data/yashan/yasdb_home/23.4.1.109/bin/yasql`。加载同目录 `lib` 后，以交互式口令登录成功，避免把口令中的 `@` 错误解释为 DSN 分隔符。实测结果为：

```text
YashanDB Server Enterprise Edition Release 23.4.1.109 x86_64 - Linux
V$VERSION.VERSION_NUMBER = 23.4.1.109
DATABASE_ROLE: PRIMARY / OPEN
COMPAT_VECTOR = yashan
```

因此本容器是 **yashan 模式**。官方 23.4 兼容性说明把该模式定位为崖山自己的 SQL/PL 与系统视图体系，并在大量已交付特性上兼容 Oracle；`COMPAT_VECTOR` 文档说明 `yashan` 与 `oracle` 取向等价。它不是 PostgreSQL 协议或 PostgreSQL 方言。23.4 另有安装时选择的 MySQL 模式，但该模式具有单独的 MySQL 协议监听和语义，不能用来代表当前容器，更不能把当前 1688 端口映射到 AgentSQL 的 PG/MySQL 路径。依据：[与 Oracle 兼容性说明](https://doc.yashandb.com/yashandb/23.4/zh/All-Manuals/Product-Overview/Compatibility/Compatibility-with-Oracle.html)、[23.4 单机部署与模式说明](https://doc.yashandb.com/yashandb/23.4/zh/All-Manuals/Installation-and-Upgrade/Installation-and-Deployment/YashanDB-Installation-via-CLI/Standalone-%28Primary-Standby%29-Deployment.html)、[COMPAT_VECTOR 参数](https://doc.yashandb.com/yashandb/23.4.6/zh/All-Manuals/Reference-Manual/Configuration-Parameters.html)。

Go 接入的官方结论如下：

| 项目 | 评估结果 |
| --- | --- |
| 官方驱动 | `yasdb-go`，`database/sql` 驱动名 `yasdb` |
| 当前公开导入路径 | `github.com/yashan-technologies/yashandb-go`；官方仓库示例为 v1.4.2 |
| DSN | `user/password@host:port[?param=value]`，例如 `sales/sales@127.0.0.1:1688`；不是 `yasql://...`。用户名或口令包含 `/`、`@`、`\` 时需按官方规则用反斜杠转义 |
| CGO / 运行时 | **需要 CGO 和 YashanDB C 客户端**；Linux 需配置客户端动态库搜索路径，Windows 还需 64 位 GCC |
| 本容器实测边界 | 本批使用镜像内 `yasql`/C 客户端完成登录和版本查询，未把 Go 驱动引入 AgentSQL，也未验证 Go 驱动在本镜像上的 Ping、取消、事务或并发行为 |

公开资料同时存在旧版手册导入路径 `git.yasdb.com/go/yasdb-go` 与当前公开 GitHub 路径；新接入应以当前官方 GitHub 发布和厂商支持矩阵为准，旧路径是否继续受支持标记为**待核实**。Go 仓库为 Apache-2.0，但配套 C 客户端的获取、再分发、静态/动态链接、基础镜像和 CI 使用许可仍需厂商书面确认。依据：[官方 Go 驱动仓库](https://github.com/yashan-technologies/yashandb-go)、[23.4 Go 驱动使用介绍](https://doc.yashandb.com/yashandb/23.4/zh/All-Manuals/Development-Guide/Go-Driver/Go-Driver-Usage-Introduction.html)、[Go 驱动 Linux 安装](https://doc.yashandb.com/yashandb/23.4.6/zh/All-Manuals/Development-Guide/Go-Driver/Go-Driver-Installation/Installing-Go-Driver-%28Linux%29.html)。

建议新增独立 `yashandb` dialect，而不是将其伪装为 `postgres`、`mysql` 或尚不存在的通用 `oracle` 别名：

1. **驱动与部署**：在 `openExecutor` 增加显式的 `yashandb` 分派，使用受厂商支持的 `yasdb` 驱动；定义 DSN 特殊字符、连接池、超时取消、TLS/认证和动态库装载合同，并单独审查 C 客户端许可与多平台打包。
2. **parser / 规则**：建立 YashanDB corpus，覆盖 Oracle 兼容语法、崖山扩展、PL、hint、分页、空字符串/NULL、标识符与注释；即使未来复用某个 Oracle AST，也必须有显式能力边界，未知语法继续 fail-closed。
3. **计划与发现**：适配崖山 `EXPLAIN`/AUTOTRACE 输出，不复用 PostgreSQL JSON 计划假设；使用崖山/Oracle 兼容系统视图实现 schema、表、列、约束和类型发现，验证普通只读账号的最小权限与不可见对象语义。
4. **类型与错误**：覆盖 `NUMBER`、日期时间与时区、interval、RAW、LOB、JSON/XML、ROWID、自定义类型和 NULL；建立 YAS 错误码到稳定安全错误的映射，并回归 bind、批量、事务、取消和连接失效。
5. **授权、脱敏与审计**：现有 B2 只支持精确的 `db_type=postgres`，崖山必须独立设计 catalog/binder 和列级授权探测；完成前只能验证应用层表策略与脱敏原型，不能宣称 B2 支持。

复杂度评估为**高**，主要风险是 proprietary wire/C 运行时与 CGO 交付、Oracle 兼容并非 Oracle 完全等价、系统目录与计划格式差异、类型和错误码覆盖，以及驱动/C 客户端许可。**当前 AgentSQL 未支持 YashanDB；本次容器与 yasql 连通性只证明目标环境可用，不构成 AgentSQL 支持或厂商认证。**

### 0.8 B2 closed-only 降级与 PG 兼容内核实测（2026-10-04）

本节只评估 PostgreSQL B2 binder 的能力握手与 closed catalog 证据链，不替代前述表级策略、脱敏和审计结论，也不是厂商认证。实测版本号来自 `current_setting('server_version_num')`；产品版本号不能代替 PostgreSQL 内核兼容口径。

#### 0.8.1 能力边界

- native `agentsql_binder` 的构建期 expectation 仍严格限定 PostgreSQL major 14--18，ABI、扩展版本和各 manifest hash 的精确匹配规则未放宽。低版本或魔改内核不会被伪装成 native 可用。
- closed mode 不再把 native major 范围当作入口门槛。首次握手必须先证明 `CATALOG_CLOSED_V1`、`Available=true`、非空 digest、`server_version_num`/major 一致和非零 database OID；仅当 major 为 14--18 时才进行第二次 native expectation 探测。低 major 的 closed 证据成立时返回 closed-only，`NativeAvailable=false`、native digest/ABI/扩展版本为空；不会仅因 native major 不支持而返回 `AUTH_BINDER_MODE_UNSUPPORTED`。
- closed capability 仍是 fail-closed 的能力探测，不是“所有 PG 协议库都默认可用”。query pack v2 自检要求相关系统目录及 8 个关键字段存在，包括 `relispartition`、RLS 字段、`attidentity`、`attgenerated` 和 `pg_partitioned_table.partrelid`。字段缺失、权限不足或目录语义无法证明时拒绝 closed；不以 `false`/空值猜测厂商语义。
- native 扩展发现属于 native-only 证据。兼容内核缺少或限制扩展发现目录时，只会把 native 标为不健康；已经完成的 closed 自检不会因此被撤销。上下文取消仍整体失败。
- closed AST cache 升级到 v2。cache key 不再要求 major 14--18，但仍要求正 major，并继续同时绑定 datasource identity、database OID、server major、role OID、search-path digest、SQL digest、capability digest 和 catalog fingerprint。缓存只保存 candidate hint；每次执行仍重新加锁、重扫 `Fpre/Fpost`、核对 OID 锁集合并重新 `PREPARE`，因此解除版本门槛不会把 native 证据或跨内核缓存混入 closed 执行。

#### 0.8.2 实测矩阵

| 产品 / `server_version_num` | native 14--18 eligibility | closed 握手 / AST 结果 | 本轮结论 |
| --- | --- | --- | --- |
| PolarDB for PostgreSQL 15 / `150019`（major 15） | 有资格；实例无 `agentsql_binder` 文件或安装记录 | 通过容器本地 socket 完成只读 catalog 自检：8/8 必需字段齐全；因本轮没有读取现有容器外部凭据，未计为完整 Gateway/AST E2E | **closed-only 候选成立，完整 B2 E2E 待专用测试凭据复核**；不得把 major eligibility 写成 native available |
| openGauss 7.0.0-RC3 / `90204`（major 9） | 不支持 | 现有 PG gateway 探测稳定返回 `AUTH_DATABASE_ERROR`；只读目录核验同时确认缺 `pg_partitioned_table`、`relispartition`、`attidentity`、`attgenerated` | **closed 不可用并安全拒绝**；需独立 openGauss query pack/驱动适配，不能用解除 major gate 绕过目录缺口 |
| IvorySQL 5.3 / `180003`（major 18） | 有资格；测试实例未安装扩展 | 完整 Gateway probe 通过，返回 `CATALOG_CLOSED_V1`、非空 closed digest、`NativeAvailable=false`；随后对隔离永久表完成 closed AST bind → execute → post-proof，结果正确并清理 schema | **本指定社区镜像的 closed-only E2E 通过**；不等于 native 或 HighGo 商业版认证 |
| HighGo SEE 4.5.10.3 / `120007`（major 12） | 不支持 | 完整 Gateway probe 通过，返回 `CATALOG_CLOSED_V1`、非空 closed digest、`NativeAvailable=false`；随后完成 closed AST bind → execute → post-proof | **本第三方镜像的 closed-only E2E 通过**；证明 closed 可独立于 native major，但商业版仍待厂商终验 |
| OpenTenBase v2.5.0 / `100000`（major 10） | 不支持 | 现有 PG gateway 探测稳定返回 `AUTH_DATABASE_ERROR`；目录核验发现缺 `pg_attribute.attgenerated`，不满足 query pack v2 | **closed 不可用并安全拒绝**；还需解决现有 pgx/拓扑入口与厂商目录差异，不能宣称 B2 支持 |
| KingbaseES V9 | 待取得 10/8 目标环境版本证据 | 未执行 | **待测**；不从产品版本号推测 major，不从旧镜像或其他 PG 兼容库外推 |

真实厂商矩阵位于 `internal/authorizedexecute/pg_compat_closed_matrix_e2e_test.go`，由 `AGENTSQL_PG_COMPAT_CLOSED_MATRIX` 或其 base64 形式显式提供目标，且在 `testing.Short()` 下跳过；口令不写入仓库。矩阵现在同时覆盖 capability、closed SELECT、启用 B2 后的列允许/拒绝、closed B5 的 INSERT/UPDATE/DELETE/隐式 NULL、提交与显式回滚、缺失 write grant 拒绝和索引隐式对象故障关闭。默认 PostgreSQL 无扩展的完整生产路径仍由 `b2_closed_only_no_extension_e2e_test.go` 覆盖。单元测试另覆盖 `90204/100000/120007/150019/180003` 的 closed handshake 版本身份、低 major cache key，以及 malformed version/major 的故障关闭。

本轮不增加 `openGauss`、`highgo`、`opentenbase` 等 datasource alias，不修改 RBAC、store、admin API，也不扩展 native binder 的版本支持范围。对外口径必须区分“连接/表级路径通过”“catalog 自检通过”“closed-only E2E 通过”和“native 可用”；四者不能互相替代。

### 0.9 closed-only DML 与真实列授权矩阵（2026-10-05）

本轮补齐的是 closed-only 能力链，不把兼容内核伪装成 native。`NATIVE_C_V1` 仍只接受 PostgreSQL 14--18 的完整五份构建 attestation；`CATALOG_CLOSED_V1` 的 B5 事务改为接受一份与当前 `server_version_num` major 精确绑定的嵌入式 grammar/query-pack attestation。低 major 只有在 closed catalog 自检、OID 身份、顺序锁、重扫、策略快照和 `PREPARE` 交叉核验全部成立时才能进入 DML；major 不匹配、混用 native/closed attestation 或目录证据不完整均继续 fail-closed。

矩阵中的成功 DML 表刻意不建索引、触发器、外键、默认值、identity、generated、RLS、规则或分区。当前 closed DML 尚不能证明这些隐式对象的完整执行闭包，因此目标表一旦存在索引（包括普通主键索引）就返回 `AUTH_IMPLICIT_OBJECT_UNSUPPORTED`，不会降级到表级授权或直接执行原 SQL。允许集只包含：带显式列清单的 `INSERT ... VALUES`、被省略普通列的隐式 NULL、带 `WHERE` 的单表 `UPDATE`、带 `WHERE` 的单表 `DELETE`；`RETURNING`、upsert、子查询、`UPDATE FROM`、`DELETE USING`、无 `WHERE` 写入和未限定表名继续拒绝。

列授权用例不再沿用第 0.2/0.6 节的 `column_authorization.enabled=false` 口径。每个可用目标都创建隔离 metadata，显式设置 `column_authorization.enabled=true`，经 production `Pipeline.Process` 路由验证：已登记 `id` 的 output/reference 允许并返回一行；同表未登记的 `label` 输出被 B2 拒绝、无结果返回且不会回退到表级 allow。closed `SemanticFacts` 与 native 适配器仍共同进入 `postgresColumnAuthorizationInputFromFacts` / `AuthorizeB2`，数据库原生 RBAC 只作为纵深防御，不替代 AgentSQL 的列策略证据。

厂商结论仍按能力证据分层：

| 目标 | closed SELECT / B2 前提 | closed DML 预期 | native 结论 |
| --- | --- | --- | --- |
| openGauss 9 系 | 缺 `pg_partitioned_table`、`relispartition`、`attidentity`、`attgenerated`，closed 自检失败 | 不进入 DML binder，安全拒绝 | 不得声称 native；需要独立 query pack |
| OpenTenBase 10 系 | 缺 `attgenerated`，closed 自检失败 | 不进入 DML binder，安全拒绝 | 不得声称 native；还需解决目录与拓扑差异 |
| HighGo 12 系 | 既有第三方镜像的 closed capability/SELECT 已通过；商业版仍待厂商终验 | 新矩阵允许在同一 closed 证据成立后运行受授权简单 DML；真实目标须重跑后才能记为通过 | major 12 不在 native 14--18 范围，`NativeAvailable=false` |
| PolarDB for PostgreSQL 15 | 既有只读 catalog 自检为 closed 候选，完整专用凭据复核仍待执行 | 具备进入新矩阵的版本条件，但未跑完不得声明 DML 通过 | 未发现受信任的 `agentsql_binder` attestation 时只能 closed-only |
| IvorySQL 18 | 既有指定社区镜像 closed SELECT E2E 已通过 | 新矩阵覆盖简单 DML 与授权拒绝；真实目标须重跑并记录镜像/模式 | 未安装并通过 attestation 的扩展不构成 native |

建议的环境 JSON 每项继续使用 `name/host/port/database/username/password/server_major/expect_closed`；openGauss/OpenTenBase 应配置 `expect_closed=false` 和稳定错误原因，HighGo/PolarDB/IvorySQL 仅在目标实例确实满足 closed query pack 时配置 `expect_closed=true`。本节描述实现后的验收口径；没有当次矩阵日志、版本原文和容器/拓扑证据的目标，不追加“实测通过”声明。

## 1. 十家数据库基线

下表中的驱动形态只描述公开资料中常见的接入方向，不表示 AgentSQL 已验证，也不表示相关驱动均由厂商以相同方式维护。具体驱动名称、版本、许可证、支持周期和 Go `database/sql` 兼容性均待厂商确认。

| 厂商 / 数据库 | 主要兼容系 | 官方驱动形态（公开资料判断，待实测） | 与现有 dialect 的技术亲缘度 | 适配主要工作点 |
| --- | --- | --- | --- | --- |
| 电科金仓 KingbaseES | PostgreSQL 系 | 常见 JDBC、ODBC、C 接口；Go 驱动或 PostgreSQL 协议接入方式待厂商确认 | 高，优先评估复用 PG parser、协议与执行器 | 核对版本对应的 PG 语法差异、系统目录与 `EXPLAIN`；确认 Go 驱动、连接参数和错误码；映射类型/OID；验证列级授权所需 catalog/binder 能力及结果脱敏 |
| 瀚高 HighGo | PostgreSQL 系；与 IvorySQL 同属瀚高产品线，HighGo 为商业版 | 常见 JDBC、ODBC、C/libpq 类接口；Go 接入方式待厂商确认 | 高，优先评估复用 PG dialect | 与 IvorySQL 按同一厂商统一对接；核对商业版扩展语法、标识符和系统目录；验证驱动、TLS/认证和超时取消；补充类型映射；验证列元数据、列级授权和脱敏链路 |
| openGauss（GaussDB） | 源自 PostgreSQL | 常见 JDBC、ODBC、C/libpq 类接口，并有 Go 生态连接方式；具体官方支持范围待确认 | 较高，但内核演进、系统目录和方言差异可能大于一般 PG 兼容库 | 评估 PG parser 覆盖率、系统表与 `EXPLAIN` 格式；选定受支持 Go 驱动；适配自有类型和错误码；验证对象发现、列级授权、脱敏及审计 |
| IvorySQL | PostgreSQL 系，强调 Oracle 兼容能力；与 HighGo 同属瀚高产品线，IvorySQL 为社区版 | 以 PostgreSQL 协议及 JDBC、ODBC、C/libpq 类客户端为主；独立 Go 驱动待确认 | 高，PG 模式可优先复用；Oracle 兼容语法需单独评估 | 与 HighGo 按同一厂商统一对接；区分原生 PG 与 Oracle 兼容语法范围；核对系统目录、类型和函数；验证 Go 连接、列血缘、列级授权及脱敏，不把协议可连通等同于方言完整支持 |
| TiDB | MySQL 协议兼容 | 通常使用 MySQL 生态的 C、Go、Java 驱动；兼容版本与参数待验证 | 高，优先评估复用 MySQL parser 与执行器 | 核对 TiDB 特有语法、事务和 `EXPLAIN`；验证 MySQL Go 驱动行为、类型返回与错误码；适配 information_schema 差异；列级授权 B2 不能沿用 PG 闭环，需另行设计，脱敏需端到端验证 |
| OceanBase | MySQL 与 Oracle 两种兼容模式 | 常见 C、Go、Java/JDBC、ODBC 接入形态；不同模式使用的驱动与能力需分别确认 | MySQL 模式中高；Oracle 模式与现有 dialect 亲缘度低 | 两种模式必须拆分认证；MySQL 模式评估协议/语法复用，Oracle 模式需新增 parser/dialect 路径；核对租户、系统目录、类型、事务、错误码；分别验证授权与脱敏 |
| TDSQL | 商用产品包含 MySQL 版、PG 版等产品线，具体以厂商资料为准 | 驱动形态须按选定的具体产品线分别确认 | 须按具体产品线分别评估，不以一条产品线的结论替代另一条 | 先锁定产品全称、版本和部署形态；按具体产品线核对分布式语法、路由、事务、`EXPLAIN`、系统目录及安全闭环 |
| OpenTenBase | PG 系（OpenTenBase 内核源自腾讯自研 TBase，基于 PostgreSQL 分支）；另有 TXSQL（MySQL 兼容）内核，双内核须分别评估 | OpenTenBase 内核按 PostgreSQL 兼容路径评估，TXSQL 内核按 MySQL 兼容路径评估；具体驱动和官方支持范围待确认 | OpenTenBase 内核可优先评估复用 PG dialect；TXSQL 内核按 MySQL 系另行评估，两者结论不得互相替代 | 企业级分布式数据库 TDSQL 的社区发行版，由腾讯云捐赠给开放原子开源基金会孵化运营；与 TDSQL 按腾讯一家厂商两条线统一对接，并分别核对双内核的语法、系统目录、分布式能力、类型、错误码与安全闭环 |
| 崖山数据库 YashanDB | 兼容 Oracle / PostgreSQL 语法 | 常见 JDBC、ODBC、C 接口；Go 驱动形态待厂商确认 | PostgreSQL 兼容子集为中等，Oracle 兼容部分较低 | 明确实际运行模式及兼容边界；建立 SQL corpus 判断能否复用 PG parser；接入受支持驱动并映射类型、错误码和系统目录；列级授权、脱敏和审计均需真实环境验证 |
| 达梦数据库 DM | 自研，语法兼容 Oracle 为主 | 常见 DPI/C、JDBC、ODBC，并有 Go 接入形态；具体版本和许可证待确认 | 低，不能直接复用 PG/MySQL dialect 作支持声明 | 需要独立评估 parser/dialect 与驱动；适配标识符、分页、系统目录、类型和错误码；设计发现、`EXPLAIN`、列级授权与脱敏支持方式，并在官方环境验证 |

## 2. AgentSQL 接入扩展点

仓库当前没有集中在单一 `internal/dbext` 目录下的插件式方言接口。数据库能力分布在多个层次，新增 dialect 不能只在数据源类型上增加一个枚举或把兼容库映射成 `postgres` / `mysql`。建议先抽象稳定合同，再按以下路径接入：

1. **数据源模型与管理面**：`internal/model` 中的数据源以 `DBType` 标识类型；管理 API、持久化校验、控制台表单和配置目前围绕 `postgres` / `mysql`。新增数据库需定义稳定的 dialect ID、默认端口、连接参数、凭据与 TLS 边界，并保证旧数据兼容。
2. **驱动与连接池**：`internal/authorizedexecute/internal/businessdb` 的 `Executor` / `Session` / `WriteTx` 是主要执行合同，`openExecutor` 当前只分派 PostgreSQL 和 MySQL。每个新 dialect 需实现连接、Ping、会话、查询、写入、事务、超时取消、行数限制和安全错误映射；协议兼容库也必须验证连接池与取消语义。
3. **SQL 解析与标准化**：`internal/parser.NewParser` 当前只注册 PG 与 MySQL，分别使用 `pg_query_go` 和 Vitess parser。新增 dialect 需要确定是复用既有 AST 转换、增加兼容层，还是引入独立 parser；必须用真实方言 corpus 验证单语句约束、对象提取、语句分类、危险函数、注释和列血缘，解析不确定时继续 fail-closed。
4. **规则与动态评估**：`internal/engine` 目前只接受 PG/MySQL AST，规则还包含方言专属能力。需逐项审查静态规则、`EXPLAIN` 结果、估算行数、事务状态和危险管理命令，不能因规则未识别而默认放行。
5. **发现与元数据读取**：`internal/controlledread` 目前为 PG/MySQL 分别生成标识符、`information_schema` 查询和采样 SQL。新 dialect 需适配标识符转义、catalog/schema 含义、基础表过滤、类型发现、采样查询和权限不足时的安全失败。
6. **类型系统与结果处理**：需要建立数据库类型到 AgentSQL 九类脱敏类型的映射，覆盖数值、日期时间、二进制、JSON、大对象、定制类型及 `NULL`，同时验证列名、来源表/列元数据、字符集和时区。无法可靠判定列归属时应保持现有 fail-closed 语义。
7. **授权、脱敏与审计**：表级/列级策略匹配、mask/hash/block/range、审计字段和错误脱敏都要纳入方言回归。数据库原生列权限、视图、行级安全或脱敏能力可作为纵深防御，但不能未经设计就替代 AgentSQL 的应用层决策证据链。

### B2 当前边界

代码可确认，`internal/bootstrap/b2_runtime.go` 只对 `DBType == "postgres"` 的数据源执行 B2 探测；包括 MySQL 在内的所有非 PostgreSQL 数据源会进入 `unsupported_datasources`，原因为 `B2_DATASOURCE_DIALECT_UNSUPPORTED`。因此，任何 MySQL 系兼容数据库即使可以复用连接与 parser，也不能宣称已获得 B2 列级授权能力；其列级授权闭环需要独立设计、实现和验证。PG 系兼容数据库也不能仅修改 `DBType` 伪装成 PostgreSQL，仍需验证 catalog、binder、类型与故障关闭语义。

## 3. 适配优先级建议

优先级首先服从真实用户项目：若某数据库厂商是用户项目的乙方，愿意提供工程师、授权测试环境、版本文档和联合验证窗口，应在同档候选中优先，必要时可上调一档。以下排序是在尚无厂商承诺和实测数据时给出的预研规划，不代表已达成合作或已完成适配。瀚高 HighGo 与 IvorySQL 是同一厂商、同一产品线的商业版与社区版，对接和联合合作均按瀚高一家厂商处理，验证可覆盖两条产品线。腾讯 TDSQL 与 OpenTenBase 分别按商业产品线与社区发行版两条线处理，对接和联合合作均按腾讯一家厂商统一推进；OpenTenBase 的 OpenTenBase（PG 兼容）与 TXSQL（MySQL 兼容）双内核须分别评估，具体产品线、内核版本与支持范围待腾讯官方资料确认。

| 优先级 | 候选 | 理由 |
| --- | --- | --- |
| P0：首批验证 | 电科金仓 KingbaseES、openGauss（GaussDB）、瀚高 HighGo（含 IvorySQL 社区版）、腾讯 TDSQL（含 OpenTenBase 社区版） | PG 系优先；瀚高与腾讯均按一家厂商两条线统一对接并分别记录版本和验证边界；OpenTenBase 优先评估 PG 兼容的 OpenTenBase 内核，TXSQL 按 MySQL 系另行评估，双内核结论不得互相替代 |
| P1：后续评估 | TiDB | 保留 MySQL 协议系候选，待 PG 系首批验证推进后评估；非 PG 的列级授权闭环需另行设计 |
| P2：专项适配 | OceanBase、崖山数据库 YashanDB、达梦数据库 DM | 多兼容模式或 Oracle 兼容特征带来更大的 parser、类型和系统目录差异，宜在通用扩展合同稳定后专项推进；若已有明确用户和厂商联合资源可提前 |

每一档的入口条件应至少包括：明确目标版本、合法可用的测试环境、厂商技术联系人、驱动与许可证确认，以及覆盖安全失败路径的验收用例。市场曝光只能用于同等条件下排序，不能替代工程可验证性。

## 4. 联合案例最小验证方案

统一的最小闭环建议为：在隔离的测试环境中部署 AgentSQL 与目标数据库实例，使用合成数据完成“数据源连接与发现 → Agent/对象及列规则 → 查询决策 → 结果脱敏 → 审计回看”。至少同时验证一条允许、一条越权拒绝和一条敏感列脱敏；数据库及 AgentSQL 均记录版本，禁止使用生产数据。本节是完整联合验收方案；本轮已经执行的社区版子集及其限制只以第 0 节记录为准，不能把下表中的建议项目当作已完成项。

| 数据库 | 最小可验证场景 | 环境要求 |
| --- | --- | --- |
| 电科金仓 KingbaseES | 以 PG 兼容路径连接测试库，发现一张含手机号/邮箱的表，跑通允许查询、未授权列拒绝、脱敏和审计 | 需厂商提供目标版本实例或合法镜像、推荐驱动及兼容参数；真实环境验证必需 |
| 瀚高 HighGo | 与 IvorySQL 按瀚高一家厂商统一对接；复用同一 PG 基线 schema，验证标识符、系统目录、`EXPLAIN`、超时取消及完整安全链路 | 需瀚高提供目标商业版本环境和驱动支持矩阵；真实环境验证必需 |
| openGauss（GaussDB） | 使用普通表及一项 openGauss 特有类型，验证发现、规则、脱敏、审计，并记录与 PG 路径的差异 | 社区 openGauss 可先做实验；GaussDB 联合案例仍需厂商提供对应商业版本环境与文档 |
| IvorySQL | 作为瀚高产品线的社区版，先以 PG 原生语法跑通闭环，再增加一条 Oracle 兼容语法作为明确的支持/拒绝边界用例 | 可先使用官方可得测试环境；与 HighGo 统一由瀚高对接，目标版本与 Oracle 兼容模式仍需确认 |
| TiDB | 使用 MySQL 驱动接入单表，验证发现、规则、脱敏和审计，再验证一条 TiDB 特有语法被正确处理或安全拒绝 | 可先用合法测试实例；联合认证需厂商提供目标版本、拓扑和推荐配置 |
| OceanBase | MySQL 模式先跑最小闭环；Oracle 模式建立独立用例，若尚无 parser 支持应明确返回不支持而非降级放行 | 两种租户模式都需厂商提供真实测试环境、驱动和版本说明，不得以一种模式代表另一种 |
| TDSQL | 确定具体商业产品线后，按其兼容系跑通连接、发现、规则、授权、脱敏与审计闭环，并增加分布式事务或路由相关的安全失败用例；不同产品线分别记录结论 | 需厂商提供具体产品、版本、拓扑及测试账号；真实环境验证必需 |
| OpenTenBase | OpenTenBase 内核以 PG 兼容路径跑通连接、发现、规则、列级授权、脱敏与审计闭环；TXSQL 内核按 MySQL 兼容路径另行验证，双内核分别记录边界 | 作为 TDSQL 社区发行版与 TDSQL 商业产品线按腾讯一家厂商两条线统一对接；具体内核版本、驱动、支持范围及测试环境待腾讯官方资料确认 |
| 崖山数据库 YashanDB | 选定 PostgreSQL 或 Oracle 兼容入口，跑基础查询/拒绝/脱敏/审计，并用语法 corpus 明确可解析范围 | 需厂商提供真实环境、官方驱动和兼容模式说明；真实环境验证必需 |
| 达梦数据库 DM | 使用官方驱动连接合成表，先验证发现和只读查询，再验证未支持方言 fail-closed、脱敏和审计 | 需厂商提供合法授权的目标版本环境、驱动包及文档；真实环境验证必需 |

联合案例出稿前还应保存可复现的配置清单、非敏感测试数据生成方式、成功与失败用例、审计截图或导出，以及双方确认的版本边界。若只完成“驱动可连接”而未跑通规则、脱敏和审计，不应称为 AgentSQL 兼容认证。

## 5. 风险与约束

- **宣传边界**：未完成适配与验收的数据库只能写“规划”“预研”或“评估中”，不能写“已支持”“原生兼容”或暗示已获得厂商认证。社区版与商业版、单机与分布式版、不同兼容模式不得相互代替结论。
- **协议兼容边界**：网络协议可连接不等于 SQL 方言、事务、系统目录、类型、错误码、取消语义和 `EXPLAIN` 完全兼容。简单复用 PG/MySQL 驱动或 parser 可能造成错误放行，安全网关必须以 fail-closed 为准。
- **许可证与分发**：厂商数据库、驱动、镜像和测试许可证可能是闭源或限制再分发。只在厂商许可的官方环境、镜像或客户授权环境验证；驱动能否随 AgentSQL 分发、静态链接或用于 CI 必须逐项审查。
- **环境真实性**：社区镜像、兼容模式或旧版本的结论不能自动外推到客户生产版本。凡未取得目标版本环境的项目均标记为“待厂商提供环境验证”，不记录虚构的性能、兼容率或通过率。
- **安全能力差异**：数据库原生列级授权、视图、RLS、审计或脱敏的名称相似，但语义与证据链可能不同。联合方案需明确哪些由 AgentSQL 执行、哪些由数据库执行，以及绕过网关直连时的边界。
- **性能与稳定性**：驱动连接池、超时取消、长事务、故障切换、编码和大对象处理可能影响安全决策与资源释放。最小案例通过后仍需压力、故障和升级回归，不能用功能冒烟替代生产适用性评估。

## 6. 建议的准入产物

每个数据库进入“已适配”评审前，至少应具备：目标版本与部署形态清单、驱动及许可证结论、方言差异表、parser corpus、类型映射、发现/规则/授权/脱敏/审计测试证据、故障关闭用例、已知限制和厂商联合确认记录。在这些产物齐备前，README 与官网仍保持“生态合作与兼容性评估的规划方向”口径。
