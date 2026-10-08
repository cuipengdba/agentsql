# DM8 厂商 license 真库验证（批六十五）

验证时间：2026-10-08（Asia/Shanghai）。本报告只记录本机 `dm8_single:dm8_20241022_rev244896_x86_rh6_64` 的本次结果。完整一次通过的终端 transcript 位于 `C:\Users\Administrator\AppData\Local\Temp\agentsql-dm8-b65-verify.log`；脚本每次重跑覆盖该日志。日志不含数据库密码、API key 或 license 内容。

## 复现与环境

在 `D:\ruanjiansheji\agentsql-v04` 执行：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/dm8-verify.ps1
```

脚本非零退出表示任一步失败。它仅用 Docker 本地容器和本机回环地址：先查 `docker inspect dm8-v06`、`ps -eo pid,args`、`/opt/singlestartup.sh`、`/proc/<dmserver pid>/cwd`、`dm.ini`、服务器日志或安装二进制中的 `dm.key` 字符串；核对路径和端口后，停止并保留原容器，在独立 `dm8-b65` 容器将原始 license 文件只读绑定到探查出的路径。后续执行 `disql`、离线 Go 探针和 AgentSQL 容器/API 验证。源代码及 Go module cache 以只读方式挂载到 `golang:1.26-bookworm`，构建时 `--network none`、`GOPROXY=off`。成功时停用本轮 AgentSQL 容器，删除本轮测试用户与两张表；保留持有只读 license 挂载的 `dm8-b65`，原 `dm8-v06` 保持停止。AgentSQL API 只发布到 `127.0.0.1` 的临时端口。

本次探查原文摘录：

```text
image=dm8_single:dm8_20241022_rev244896_x86_rh6_64; entrypoint=/opt/startup.sh; dmserver_ini=/opt/dmdbms/data/DAMENG/dm.ini; cwd=/opt/dmdbms/bin; license_target=/opt/dmdbms/bin/dm.key; port=5236; COMPATIBLE_MODE=0
Mount confirmed: D:\ruanjiansheji\db-images\dm8B01200017.key -> /opt/dmdbms/bin/dm.key (readonly)
License log: 32:2026-10-08 13:00:13.105 [INFO] database P0000000283 T0000000000000000283  License will expire on 2027-06-25
```

`dm.key` 文件名的证据来自原实例日志 `file dm.key not found, use default license!`，并与安装目录 `libdmlic.so` 内的 `dm.key` 字符串复核；安装目录来自运行中 `dmserver` 的实际工作目录。实例配置 `PORT_NUM=5236`，Docker 原映射为 `127.0.0.1:5236->5236/tcp`。原始 license 为 648 字节，SHA-256 为 `C2254529D5B3A5CD9D3E9256CD4501A52818FA26FACDD3AAD872B8CAFF0860F8`；脚本只读挂载，不复制文件。

## 实例、登录与 CRUD

容器内使用 `/opt/dmdbms/bin/disql /NOLOG`，`LD_LIBRARY_PATH=/opt/dmdbms/bin`，从原容器 `SYSDBA_PWD` 环境项取得本机配置密码，经 stdin 输入 `CONN SYSDBA/"<redacted>"@127.0.0.1`。没有把历史 `SYSDBA/SYSDBA` 假设为本实例密码。实例就绪和身份查询：

```sql
SELECT 1 AS READY FROM DUAL;
SELECT * FROM V$VERSION;
SELECT ID_CODE();
SELECT NAME FROM V$DATABASE;
SELECT USER, SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID) AS SCHEMA_NAME FROM DUAL;
SELECT SERIES_NO, EXPIRED_DATE FROM V$LICENSE;
SELECT PARA_NAME, PARA_VALUE FROM V$DM_INI WHERE PARA_NAME='COMPATIBLE_MODE';
```

```text
Server[127.0.0.1:5236]:mode is normal, state is open
1 DM Database Server 64 V8
2 8.4
3 企业版
4 DB Version: 0x7000c
5 03134284294-20241009-244896-20119
ID_CODE(): --03134284294-20241009-244896-20119 Pack3
NAME: DAMENG
USER SCHEMA_NAME: SYSDBA SYSDBA
SERIES_NO EXPIRED_DATE: 8B01200017 2027-06-25
PARA_NAME PARA_VALUE: COMPATIBLE_MODE 0
```

因此本次可确认数据库名 `DAMENG`、登录用户及当前 schema `SYSDBA`、兼容模式 `0`；这不是对其他 DM 实例默认用户或默认库的推断。license 序列号和到期日在 `V$LICENSE` 中可见，且服务器日志记录到期日，登录不再报 `-2501`。

本轮脚本以 `AGSQL_B65_134346` 运行以下 SQL，随后删除测试表。关键原始输出为 `INSERT/UPDATE/DELETE: affect rows 1`、更新后 `NOTE: updated`、删除后 `N: 0`。

```sql
CREATE TABLE AGSQL_B65_134346 (ID INT PRIMARY KEY, PHONE VARCHAR(20), NOTE VARCHAR(64));
INSERT INTO AGSQL_B65_134346 (ID,PHONE,NOTE) VALUES (1,'13800135678','created');
SELECT ID,PHONE,NOTE FROM AGSQL_B65_134346 WHERE ID=1;
UPDATE AGSQL_B65_134346 SET NOTE='updated' WHERE ID=1;
SELECT NOTE FROM AGSQL_B65_134346 WHERE ID=1;
DELETE FROM AGSQL_B65_134346 WHERE ID=1;
SELECT COUNT(*) AS N FROM AGSQL_B65_134346;
```

同一脚本对这台实例作了不修改数据的语法探针，五条查询均成功：

```text
SQL=SELECT TOP 1 1 AS V FROM DUAL;                         -> V: 1
SQL=SELECT 1 AS V FROM DUAL LIMIT 1;                       -> V: 1
SQL=SELECT 1 AS V FROM DUAL FETCH FIRST 1 ROWS ONLY;      -> V: 1
SQL=SELECT 1 AS MixedCase FROM dual;                       -> MIXEDCASE: 1
SQL=SELECT 1 AS "MixedCase" FROM DUAL;                     -> MixedCase: 1
```

箭头右侧是 disql 原始列名和值的紧凑转写；完整原始 `LINEID` 输出见 transcript。这只证明单行形式在 `COMPATIBLE_MODE=0` 的本实例可执行，不证明各分页形式在多行、排序或其他兼容模式下语义完全相同。

## 宿主机驱动与 AgentSQL 防护

脚本从项目锁定的 `github.com/godoes/gorm-dameng` v0.7.2 离线构建 Windows Go 探针，运行时 import `github.com/godoes/gorm-dameng/dm8`，`database/sql` 注册名为 `dm`。它连接 `127.0.0.1:5236`，通过 `dm://<reader>:<redacted>@127.0.0.1:5236?schema=SYSDBA` Ping、查询当前身份、用 `?` 绑定读表，并尝试无授权 INSERT。读账号仅获 `CREATE SESSION` 和两张测试表的 `SELECT`。原始输出：

```text
driver=github.com/godoes/gorm-dameng/dm8 user=AGSQL_B65_R_134348 schema=SYSDBA bind_phone=13800135678 denied_write=true write_error=Error -5501: 没有[SYSDBA.AGSQL_B65_134346]对象的插入权限
```

同一查询由 `parser.NewParser(model.DialectDM)` 在离线 Linux Go 容器解析，核对 `ProjectionLineages` 中 `PHONE` 的物理来源。原始输出：

```text
parser_dialect=dm source=SYSDBA.AGSQL_B65_134346 lineage_PHONE=SYSDBA.AGSQL_B65_134346.PHONE
```

AgentSQL v0.5.0 健康检查为 `ok`；经 API 注册 `db_type=dm`、目标 `dm8-b65:5236`、schema `SYSDBA` 的数据源，`ping` 返回 `{"ok":true,"latency_ms":10}`。只读 Agent 仅获 `SYSDBA.AGSQL_B65_134346` 的表策略；数据库账号同时可读另一张测试表 `SYSDBA.AGSQL_B65_D_134348`，用于验证 AgentSQL 自身的 R010 策略。MCP `query` 和审计的原始关键字段：

```json
{"data":{"audit_id":1,"est_scan_rows":1,"redact":{"MaskedCells":1,"TouchedColumns":{"1":"phone"}},"result":{"Columns":["ID","PHONE"],"RowCount":1,"Rows":[["2","138****5678"]],"Truncated":false}},"decision":"allow"}
{"data":{"audit_id":2,"est_scan_rows":1,"hits":[{"Decision":"deny","Risk":1,"RuleID":"R010"}]},"decision":"deny"}
{"decision":"error","error_code":"DB_SYNTAX_ERROR","error_stage":"parse"}
{"total":3,"list":[{"id":3,"decision":"error","error_code":"DB_SYNTAX_ERROR"},{"id":2,"db_type":"dm","stmt_type":"SELECT","objects":"SYSDBA.AGSQL_B65_D_134348","decision":"deny","rule_hits":"[{\"RuleID\":\"R010\"}]"},{"id":1,"db_type":"dm","sql_norm":"SELECT ID , PHONE FROM SYSDBA . AGSQL_B65_134346 WHERE ID = ?","stmt_type":"SELECT","objects":"SYSDBA.AGSQL_B65_134346","decision":"allow","rows_returned":1}]}
```

上面的 JSON 是从完整响应中保留字段的摘录；完整响应在 transcript。第三个输入为 `SELECT /* batch65-r006 */ ID ...`，在 parser 阶段返回 `DB_SYNTAX_ERROR` 并生成审计，**没有命中 R006**，不能算 R006 的真库通过证据。成功查询的手机号只在 `disql`/宿主机原始数据验证中明文出现，AgentSQL MCP 结果为 `138****5678`。

## 发现的 AgentSQL 缺陷与最小修复

发现两个连续的本方接线缺口，均已在相应 `internal/` 文件作最小修复。修复前，DM 数据源已注册且 Ping 成功，但首个 MCP 查询返回 `GATEWAY_INTERNAL`，服务日志原文为：

```text
datasource "dm8-b65" has unsupported dialect "dm"
```

加入 `internal/pipeline/pipeline.go` 的 `dm` 类型门和 `internal/pipeline/rules.go` 的通用规则装配、`internal/rules/generic.go` 的 DM AST 接纳后，解析和审计开始执行，但响应仍失败，日志原文：

```text
validate AST dialect "dm": invalid engine input
```

随后在 `internal/engine/engine.go` 接纳 DM AST/规则 dialect。修复后本报告上方的 MCP SELECT 为 `decision=allow`、脱敏命中 1 个单元格，R010 为 `decision=deny`，审计三条均出现。此修复没有扩展 `oracleCompatibleParser` 的语法，也没有放开 DM 写入。

## 差异与验证边界

| 项目 | 本次 DM8 真库证据 | Oracle / PostgreSQL 对照边界 |
| --- | --- | --- |
| 连接 | 本实例监听 5236，Go 驱动注册名 `dm`，`?` 绑定成功；DSN 显式传 `schema=SYSDBA` | 仓库既有 Oracle/PG 适配与历史报告采用各自驱动、端口和绑定形式；本批未连接其真库重测。不可用 Oracle/PG 连接串替代 DM。 |
| 库与 schema | `V$DATABASE.NAME=DAMENG`，当前 `USER`/schema 均为 `SYSDBA` | PG 的 database/schema、Oracle 的 service/PDB/schema 概念不可据此等同；本批对照真库未验证。 |
| 版本/配置 | `V$VERSION`、`ID_CODE()`、`V$DM_INI` 实测如上，`COMPATIBLE_MODE=0` | Oracle/PG 版本视图与兼容参数不得直接复用；本批未重验对照实例。 |
| 解析与血缘 | `dm` 走 `oracleCompatibleParser`，简单 SELECT 的 `PHONE` 物理来源已核对；注释 SELECT 被 parser 拒绝 | 这只覆盖当前窄 SELECT 子集，不能代表完整 Oracle grammar 或 PG grammar。 |
| 分页与标识符 | 本次 DM8 对单行 `TOP 1`、`LIMIT 1`、`FETCH FIRST 1 ROWS ONLY` 均成功；不加引号的 `MixedCase` 显示为 `MIXEDCASE`，双引号别名保留大小写 | 仓库既有 Oracle/PG 方言适配采用各自分页与标识符规则；本批未在其真库重测，也未测试 DM 多行分页语义。 |
| 规则与脱敏 | R010 表策略拦截、手机号掩码、三条审计实测通过 | Oracle/PG 的同类链路本批未重测；DM 的 R006 因注释先被 parser 拒绝，未验证。 |
| 权限 | DM `CREATE SESSION` + 表级 `SELECT` 可读，INSERT 返回 `-5501` | DM 数字错误码与 Oracle `ORA-`、PG SQLSTATE 的错误体系不同；本批只实测 `-5501`。 |

协议接入对照来自本次成功的 DM 驱动探针及仓库当前适配源码：DM 使用 `github.com/godoes/gorm-dameng/dm8`（`go.mod` 锁定 v0.7.2）、`dm://...?...schema=SYSDBA`；Oracle 路径调用 `go-ora/v2` v2.9.0 的 `BuildUrl(host, port, database, ...)`；PostgreSQL 路径 import `pgx/v5` v5.9.2。三者在项目中是独立连接路径，不能仅替换类型名或 SQL parser 代用。后两项是代码结构对照，本批未通过协议互连试验验证其真库行为。

本批没有单独实测 DM 多行分页语义、复杂 JOIN、函数、系统目录、EXPLAIN、通信加密、超时取消、连接复用、HA、Oracle/PG 实例差异和其他兼容模式。AgentSQL 经本次修复验证的是有界只读查询及所述防护链路；通用写入、完整列授权和所有 DM8 语法均未验证。license 到期日为数据库报告的 `2027-06-25`，并非长期授权承诺。

**批七十一矩阵决定：**用户已确认将 DM8 档位改为绿色。绿色仅表示指定 DM8 Pack3、`COMPATIBLE_MODE=0` 实例的只读 SELECT、列血缘、R010 表策略、审计及手机号脱敏链路实测通过；R006 未验证，多行分页/JOIN/函数/系统目录未测，写入未放开。不据此声明完整 DM8 兼容或生产认证。README、SECURITY、兼容矩阵及官网矩阵已按此边界同步。

## 构建、清理与自查

完整运行最后输出：

```text
PASS: SELECT, column lineage, R010 denial, phone mask, parse failure, and three audit rows
drop verification reader: ... executed successfully
drop verification table: ... executed successfully
drop ungranted-policy table: ... executed successfully
PASS: temporary DM8 user and tables removed; licensed DM8 container retained; AgentSQL container stopped
```

离线 `golang:1.26-bookworm`、`--network none`、`GOPROXY=off`、只读源码及 module cache 下，`go build ./...` 退出 0；`go test -short ./internal/engine ./internal/rules ./internal/parser` 三包通过。pipeline 用所有非测试源码和 `pipeline_test.go`、`rule_overrides_test.go`、`static_assess_test.go`、`t25_observer_test.go` 组成的文件列表执行 `go test -short`，结果 `ok command-line-arguments 0.111s`。完整 `go test -short ./internal/pipeline ./internal/authorizedexecute/...` **未通过测试构建**：离线缓存缺 `github.com/moby/sys/user v0.4.1`、`sequential v0.7.0` 与 `gopsutil/v4 v4.26.6` 的 zip，测试依赖在只读 cache 写 `.lock` 时失败；没有把此项记为代码测试通过。上述缺包只在测试依赖路径出现，未影响 `go build ./...` 与本次真库 E2E。

此前失败调试轮次留下的对象，经目录查询发现 13 张精确匹配 `AGSQL_B65_(D_)?[0-9]{6}` 的表和 9 个精确匹配 `AGSQL_B65_R_[0-9]{6}` 的账号；逐一按精确名称删除，输出 `drop_success_count=22`、`remaining_batch65_objects=0`。本轮成功脚本自己的对象也在末尾删除。保留 `dm8-b65` 的只读 license 原路径挂载，AgentSQL 容器停止。原始 license 在结束时 SHA-256 仍为 `C2254529D5B3A5CD9D3E9256CD4501A52818FA26FACDD3AAD872B8CAFF0860F8`。

`git diff --check` 退出 0；新增脚本和报告各自检查无行尾空白。最终 `git status --short` 除本批允许的 4 个 `internal/` 修改及脚本、报告新增外，还列出任务开始前已存在的 `cmd/agentsql/b5-wal/` 和 `cmd/agentsql/\357\200\272memory\357\200\272.instance-id` 两项未跟踪物；本批未触碰它们。未执行任何 git 写入操作，未复制或提交 license，未修改矩阵档位，所有“通过”均以上述命令输出为依据。
