# 金仓 KingbaseES + 瀚高 HighGo 联合案例验证记录

> 记录日期：2026-10-02（Asia/Shanghai）
>
> 文档性质：可复核的工程验证记录，不是厂商认证、厂商背书、兼容性证书或生产支持承诺。
>
> 结论原则：指定环境中的 PostgreSQL 协议路径实测通过，不等于完整方言兼容，也不等于对应商业版已经通过终验。

## 1. 结论摘要

| 产品线 | 本批状态 | 可以公开表述 | 不可以外推 |
| --- | --- | --- | --- |
| 瀚高 HighGo / SEE | **第三方 SEE 镜像的 PostgreSQL 协议路径已实测** | 在下列指定镜像、合成表和关闭 B2 的隔离测试配置中，连接、列发现、R006 规则拒绝、只读查询、结果脱敏和审计闭环有实测记录 | 不代表瀚高厂商认证；不代表 HighGo 商业版、其他版本、Oracle 兼容模式、TLS、生产拓扑或 B2 列级授权已经通过 |
| IvorySQL | **同源社区版参考，不替代 HighGo 结论** | IvorySQL 是瀚高社区产品线，已有独立的社区版协议路径记录 | 不以 IvorySQL 的结果替代 HighGo 商业版终验 |
| 中电科金仓 KingbaseES | **未实测，待厂商环境** | 现有第三方旧镜像被过期 license 阻断；V9R1C10 目标镜像尚未到位 | 不得表述为连接、发现、查询或 AgentSQL 安全闭环已通过；也不能把旧镜像失败解释为 KingbaseES 产品不兼容 |

AgentSQL 当前没有 `highgo` 或 `kingbase` 数据源类型。本记录中的瀚高测试通过现有 `db_type=postgres` 路径完成。这个事实只说明本次协议路径可工作，不是新增原生 dialect 或厂商识别能力。

## 2. 环境与证据口径

### 2.1 当前容器快照

2026-10-02 执行：

```powershell
docker ps -a --format "table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"
```

与本案有关的输出摘要：

```text
hg-v06-user   qiuchenjun/hgdb-see:4.5.10.3       Up ...   127.0.0.1:5866->5866/tcp
kingbase-v06  chyiyaqing/kingbase:v8r6           Up ...   127.0.0.1:54321->54321/tcp
```

`Up` 只表示容器主进程仍在运行，不表示容器内数据库已经可用。金仓数据库状态必须以 2.3 节的数据库启动日志为准。

瀚高容器的只读 inspect 摘要为：

```text
image sha256:e4dd0ac4877c3a8818ec4cef68108be8d54c410782a6248a01257ea66dd49725
command docker-entrypoint.sh postgres
mapping 127.0.0.1:5866 -> 5866/tcp
```

该镜像来自 Docker Hub 个人账号，不是厂商交付镜像。镜像 tag `4.5.10.3` 与数据库返回的版本文本应分别记录，不能把 tag 当作厂商确认的完整版本号。

### 2.2 瀚高当前复核证据

以下命令使用容器内既有 UID 999 和本地 `sysdba` 受信任连接，不读取、导出或修改任何数据库口令：

```powershell
docker exec --user 999 hg-v06-user /opt/highgo/hgdb-4.5/bin/psql `
  -w -X -v ON_ERROR_STOP=1 -p 5866 -U sysdba -d highgo `
  -c "SELECT version();" `
  -c "SELECT current_user, current_database();"
```

输出：

```text
HighGo Database Management System 4.5 on x86_64,build on 20250227
current_user = sysdba
current_database = highgo
```

系统目录和计划格式复核命令：

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

输出中可见 `public.agentsql_v05_customers`，列目录依次返回 `id integer`、`name character varying`、`phone character varying`、`email character varying`；`EXPLAIN (FORMAT JSON)` 返回合法 JSON 计划，根节点为 `Limit`，下含 `Sort` 等节点。容器日志同时记录数据库监听 `0.0.0.0:5866` 与 Unix socket `/tmp/.s.PGSQL.5866`。

三权分立是本环境的已知边界。当前复核中，`sysdba` 直接读取合成业务表返回：

```text
ERROR: You have no right to select it.
```

这不是协议失败：合成表由专用测试角色 `agentsql_ro` 创建并持有，2026-10-01 的 AgentSQL 闭环使用该角色。当前复核没有探测、导出或重置其口令，也没有提升 `sysdba` 权限。

### 2.3 金仓当前阻断证据

只读查看现有容器日志：

```powershell
docker logs --tail 200 kingbase-v06
```

关键原文：

```text
waiting for server to start.... stopped waiting
FATAL:  XX000: License file expired.
LOCATION:  PostmasterMain, postmaster.c:659
sys_ctl: could not start server
Examine the log output.
```

因此，即使 `docker ps` 将 `kingbase-v06` 显示为 `Up`，也不能判定 KingbaseES 数据库已启动。本批不重启、不替换 license、不绕过限制、不下载其他镜像。V9R1C10 目标镜像待厂商提供后再测。

## 3. 联合验证矩阵

### 3.1 瀚高 HighGo / SEE

2026-10-01 的 AgentSQL 端到端记录沿用仓库预研基线：第三方镜像 `qiuchenjun/hgdb-see:4.5.10.3`、端口 5866、库 `highgo`、专用测试角色 `agentsql_ro`、合成表 `public.agentsql_v05_customers`。`column_authorization.enabled=false`，所以没有 B2 列级授权通过结论。

| 验证项 | 状态 | 命令 / 输入 | 可复核输出 | 边界 |
| --- | --- | --- | --- | --- |
| 连接 | **已实测** | AgentSQL 数据源 `db_type=postgres` 执行 Ping；本轮另执行 `SELECT version()` | AgentSQL Ping 74 ms；本轮返回 `HighGo Database Management System 4.5 ... build on 20250227` | 非官方镜像；未测 TLS、取消、连接池故障切换 |
| 列发现 | **已实测** | 管理端对该数据源执行 discovery；本轮另查询 `information_schema.tables` | 发现 1 张目标表、4 列、4 个采样值；`phone` / `email` 各 2/2 命中，`name` 为低置信 generic 候选；discovery 审计 ID 4 | 样本为合成数据；本轮没有重放账号级 discovery；B2 未启用 |
| 只读查询 | **已实测** | `SELECT id,name,phone,email FROM public.agentsql_v05_customers WHERE id > 0 ORDER BY id LIMIT 10` | 返回 2 行；手机、邮箱共 4 个单元格完成脱敏，如 `138****8000`、`a***@example.com`；允许审计 ID 12，`rows_returned=2` | 只证明该单表、这些类型和该版本路径 |
| 执行计划 | **已实测子集** | 本轮在真库执行 `EXPLAIN (FORMAT JSON)` 系统目录查询 | 返回合法 JSON，根节点 `Limit` | 证明计划格式可解析的一个样例，不代表所有 HighGo 计划节点覆盖 |
| 规则拦截 | **已实测** | 通过 AgentSQL 提交带块注释的受控 `SELECT` | 命中内置 R006 并拒绝，拒绝审计 ID 11；业务 SQL 不应执行 | R006 是 AgentSQL 能力，不是数据库原生能力 |
| 错误口径 | **已实测子集** | 无权限的 `sysdba` 读取业务表 | 数据库原文 `ERROR: You have no right to select it.` | 对外只记录必要错误分类；不公开口令、连接串或内部敏感详情 |
| 审计 | **已实测** | 查看 discovery、拒绝和允许请求的审计记录 | discovery ID 4；拒绝/允许 ID 11/12 | ID 仅在该次本地控制面有效，不是跨环境稳定标识 |

上述端到端证据与更广的兼容性预研记录见 [国产数据库适配预研](ecosystem-db-compat-research.md#062-瀚高-see-与金仓旧镜像闭环)。本文件收敛联合案例所需子集，不扩张原结论。

### 3.2 中电科金仓 KingbaseES

| 验证项 | 状态 | 当前证据 | 到位后动作 |
| --- | --- | --- | --- |
| 数据库启动 / 连接 | **未通过前置条件，待厂商** | 第三方旧镜像初始化后报 `License file expired`，数据库服务未启动 | 厂商提供 V9R1C10 目标镜像、合法许可、目标兼容模式和推荐连接方式后执行 Ping |
| 列发现 | **未执行** | 无可用数据库连接 | 核验 `information_schema` / `pg_catalog`、标识符大小写、类型与 OID |
| 只读查询 | **未执行** | 无可用数据库连接 | 使用与瀚高一致的合成表、最小权限账号和限定行数查询 |
| 执行计划 | **未执行** | 无可用数据库连接 | 核验 `EXPLAIN (FORMAT JSON)` 的语法、结构、估算字段与错误行为 |
| 规则拦截 / 脱敏 / 审计 | **未执行** | 无 AgentSQL 请求或审计 ID | 重跑允许、拒绝、脱敏、错误与审计闭环 |
| 错误口径 | **仅记录环境阻断** | `FATAL: XX000: License file expired.` | 不把该错误描述为产品不兼容；厂商环境中的错误码另行登记 |

金仓到位后的结果必须单列目标版本、兼容模式、驱动、许可、TLS / 认证、catalog、类型、`EXPLAIN`、超时 / 取消和 fail-closed 行为。任何一项未测都标为“待验证”，不能从 PostgreSQL 协议兼容声明推导通过。

## 4. 三个真库演示场景

可复制 SQL 位于 [`examples/joint-case-highgo.sql`](../examples/joint-case-highgo.sql)。演示只使用虚构数据，不记录真实口令或连接串。

### 场景 A：受控只读查询与结果脱敏

通过 MCP `query` 提交：

```sql
SELECT id, name, phone, email
FROM public.agentsql_v05_customers
WHERE id > 0
ORDER BY id
LIMIT 10;
```

预期：规则与对象授权允许；返回 2 行；`phone` 和 `email` 按已启用规则脱敏；产生 allow 审计。2026-10-01 已按上述输入跑通。

### 场景 B：JSON 执行计划

通过 MCP `explain_query` 传入场景 A 的 `SELECT`；真库直连复核时可执行：

```sql
EXPLAIN (FORMAT JSON)
SELECT id, name
FROM public.agentsql_v05_customers
WHERE id = 1;
```

预期：返回 JSON 计划。计划节点和代价仅用于演示，不应写成性能承诺。本轮已在同一真库用系统目录查询复核 JSON 计划格式；业务表计划需使用持有该表 `SELECT` 权限的测试角色执行。

### 场景 C：R006 注释规则拦截

仅通过 MCP `query` 提交，不要用 `psql` 直连来判断 AgentSQL 规则：

```sql
SELECT /* joint-case-r006 */ id, name
FROM public.agentsql_v05_customers
WHERE id = 1
LIMIT 1;
```

预期：AgentSQL 在触达业务查询前以 R006 拒绝并写 deny 审计。2026-10-01 已用同类带注释 `SELECT` 跑通；该结果证明 AgentSQL 规则生效，不证明数据库本身会拒绝注释。

## 5. 接入现有 quickstart / demo

建议复用 [5 分钟快速上手“路径 B：接入你自己的库”](GETTING_STARTED.md#路径-b接入你自己的库真实生产闭环)，不要修改固定 PostgreSQL / MySQL 合成数据源的 Live Demo：

1. 在隔离环境中新建 HighGo 数据源，类型选择 `postgres`，填写测试实例的 host、5866 端口、库名和最小权限测试账号；不要新增 `highgo` 类型别名。
2. 创建专用 readonly Agent，只授权 `public.agentsql_v05_customers` 和演示所需四列。
3. 为 `phone`、`email` 配置精确到表列的启用脱敏规则。
4. 依次运行场景 A、B、C，保存工具响应与对应审计；不得保存口令、API Key 或完整 DSN。
5. 若要严格复现 2026-10-01 的协议路径结果，只能在隔离演示环境明确记录 `column_authorization.enabled=false`。这不等于 B2 验证通过，也不应作为生产配置建议。

现有 quickstart 的 `demo.enabled` 路径绑定固定数据源和固定合成剧本，不应直接改造成厂商商业版结论。联合演示建议走普通数据源接入路径，避免把 Live Demo 的安全边界与真库验证混在一起。

## 6. 厂商终验清单

- 厂商交付的产品全称、完整版本、镜像或实例来源、许可有效期、部署拓扑与兼容模式。
- 厂商推荐驱动 / 协议、TLS 与认证组合、连接参数、最小权限账号设计。
- 连接、catalog、列发现、类型 / OID、标识符、允许 / 拒绝查询、JSON 计划、超时 / 取消、连接池和错误码。
- AgentSQL 对象 / 列授权、规则拒绝、结果脱敏、审计、故障关闭，以及 B2 能力是否适用。
- 双方技术审校与公开发布许可。完成之前只使用“适配验证中”“待厂商环境终验”等表述。

## 7. 公开口径

推荐一句话：

> AgentSQL 首批联合案例验证聚焦中电科金仓 KingbaseES 与瀚高 HighGo；目前已完成指定第三方瀚高 SEE 镜像的 PostgreSQL 协议路径实验，金仓及 HighGo 商业版仍待厂商目标环境终验。以上结果不是厂商认证或生产支持承诺。

面向公众号、技术群和英文渠道的完整模板见 [联合案例社区运营口径](joint-case-operations.md)。
