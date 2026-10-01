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

“社区版协议路径实测通过”只表示下面的合成数据冒烟闭环通过；不代表厂商认证、完整 SQL 方言兼容、生产可用性或商业版支持。TiDB 7.5.1 与 OceanBase CE 4.4.2.1 虽有本机环境，但遵循“PG 系先交付”的顺序，本轮没有新增端到端证据，继续沿用既有 MySQL 适配结论，不在本文追加通过声明。

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
5. 在 CN 与 DN 双向登记节点，CN 创建 `DEFAULT NODE GROUP` 与 `SHARDING GROUP`，再创建 `DISTRIBUTE BY SHARD(id)` 的合成表。实测 `version()` 为 `PostgreSQL 10.0 OpenTenBase V2 ... 64-bit`，CN 直连查询与 `EXPLAIN (FORMAT JSON)` 成功，根计划节点为 `Remote Fast Query Execution`。
6. 镜像初始 HBA 只有本地 trust。网关首次连接原文为 `FATAL: no pg_hba.conf entry for host "172.17.0.1", user "opentenbase", database "postgres", SSL off`。本轮没有开放整个网段，而是创建最小只读测试角色，并仅为 Docker 主机地址、目标库和该角色添加 `/32` 的 `md5` 规则。
7. 初次启动出现审计/维护日志目录缺失风暴，原文为 `could not open audit log file "log/audit/audit-Thursday-06.log": No such file or directory` 和 `could not open audit log file "pg_log/maintain/maintain-Thursday-06.trace": No such file or directory`。创建 CN/DN 对应目录并重载后，重复拉起停止；后续仍观察到镜像内 2PC 清理函数缺失日志，应在正式拓扑继续核验。

允许查询失败并非数据库账号或 SQL 本身失败：相同只读账号从 CN 直连可返回 2 行，开启 `default_transaction_read_only=on` 后仍可返回；AgentSQL 处于 B2 `feature-off`，所以本轮不把错误归因于 B2。当前能确认的失败边界是 `internal/authorizedexecute/internal/businessdb` 的 PostgreSQL 查询执行/结果读取路径对该分布式 PG10 内核返回数据库错误，外层按安全设计只暴露 `AUTH_DATABASE_ERROR`。在未取得原始驱动错误与修复回归前，OpenTenBase 状态保持“待进一步适配”，不得写成实测通过。

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
