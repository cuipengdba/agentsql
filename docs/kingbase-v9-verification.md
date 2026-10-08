# KingbaseES V9 真库验证：批七十二（可写 license 卷）

验证日期：2026-10-08（Asia/Shanghai）。一键脚本：[kingbase-v9-verify.ps1](../scripts/kingbase-v9-verify.ps1)。最终运行的完整终端 transcript：`C:\Users\Administrator\AppData\Local\Temp\kingbase-v9-batch72-transcript.txt`（7,877 bytes，14 条带时间戳的 PASS、0 条 FAIL，最终退出码 **0**）。早期调试失败已被最终 transcript 覆盖，其关键失败结论在下文单列。本批未执行 git 操作，未改矩阵、README、SECURITY.md 或 `internal/`。

## 结论与矩阵建议

**V9 的本次 `pg` 模式基础防护闭环通过。** 可写 license named volume 下数据库正常启动；日志为 `starting KingbaseES V009R001C010`，`SELECT version()` 与之相同，没有浮动模式写权限 FATAL 或产品码 FATAL。新 license 的 V009R001C 产品码被该服务器接受，这是由启动成功推得的兼容结论，不是对 license 文件内容的转录。

建议将矩阵中 **V9／PostgreSQL 协议基础闭环**标为 🟩（待用户拍板）；不把该结果扩展为 V9 全模式、长期授权或生产可用结论。若矩阵只有一个不区分范围的 V9 总档位，建议暂保留 🟨，直到实际到期日和未测边界另行确认。本报告未直接改矩阵。

## 方案 B：镜像、license 与容器

| 项目 | 批七十二实测 |
| --- | --- |
| 镜像 tar | `D:\ruanjiansheji\db-images\jincang\KingbaseES_V009R001C010B0004_x86_64_Docker.tar`，本轮复用本机已加载镜像 |
| tag / image ID | `kingbase_v009r001c010b0004_single_x86:v1` / `sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724`；与批六十四、六十九相同 |
| 镜像入口与端口 | `/home/kingbase/docker-entrypoint.sh`，用户 `kingbase`，`54321/tcp` |
| 镜像 license 路径 | 原始 `/home/kingbase/install/kingbase/bin/license.dat`，厂商入口运行时搬到 `/home/kingbase/install/kingbase/etc/license.dat` 并创建软链；本脚本按已核对的 `initdb`/`sys_ctl` 命令启动，将 bin 路径软链至 `/license/license.dat` |
| 新 license | `license_4_V009R001C-企业版-180天.dat`，5,036 bytes，SHA-256 `03793D90E5C23CD2F4303019D1EE6888D13D4679739923D5FDFCDBCDA648B1E9` |
| 可写卷 | `kingbase_v9_license_b72`，`--mount type=volume,source=kingbase_v9_license_b72,target=/license`；**不是 bind mount** |
| 复制 | 一次性容器 `kingbase-v9-license-copy-b72` 挂卷后，用 `docker cp` 从宿主机复制至 `/license/license.dat`；卷内设为 `kingbase:kingbase`、`0600`；尺寸和 SHA-256 与源文件一致，`kingbase` 用户的 `test -w` 通过；复制容器随即删除 |
| 启动与网络 | V9 以 `-m pg` 初始化，数据库端口仅发布在 `127.0.0.1` 临时端口；容器 inspect 确认 `/license` 为 `volume` 且 `RW=true` |
| 服务器许可元数据 | `get_license_validdays()` 返回 `180`；`get_license_info()` 中提取到 `2025-11-24`，但字段含义**未确认**，不得将其当作到期日。服务器启动日志未给出可确认的到期日；**实际到期日未验证**。脚本只输出日期与天数，不输出函数原文或 license 内容 |

原始 license 文件只读于本地验证流程，脚本结束时重新计算源文件 SHA-256，仍为表中哈希；没有 `docker commit`、构建镜像或向仓库复制 license。V9 数据目录仅在本轮容器中，最终删除该容器、临时角色/表、AgentSQL 容器及数据卷、license named volume；脚本完成后从 Docker 再次查验，这些本轮对象均不存在。离线 Go 构建卷、缓存卷和本轮私有网络保留以便重跑；批六十四的 V8/V9 容器及其卷没有被本脚本操作。

## 与前两次失败对照

| 批次 | license 接入 | 实测结果 |
| --- | --- | --- |
| 批六十四 | 旧 license 的可写副本 | `productVersion check failed. server is 'V009R001B' but license is 'V009'`；未启动 |
| 批六十九 | 新 V009R001C license，只读 bind mount | `FATAL: License file should have write access mode in floating mode`；先在写权限检查失败，未走到产品码校验 |
| 批七十二 | 新 license 的专用可写 named volume | `kingbase` 可写；V9 启动并完成后续 SQL、PG 协议、AgentSQL 闭环；未出现上述 FATAL |

## 实际运行证据

下列摘录来自最终运行的固定路径 transcript；完整输出和每一步时间戳以该文件为准。

```text
[2026-10-08T23:56:08+08:00] PASS: named volume=kingbase_v9_license_b72 target=/license copied_bytes/mode/owner=5036|600|kingbase:kingbase sha256=03793D90E5C23CD2F4303019D1EE6888D13D4679739923D5FDFCDBCDA648B1E9; kingbase user can write
[2026-10-08T23:57:01+08:00] PASS: database ready and license accepted by server startup
2026-10-08 15:56:59.257 UTC [151] LOG:  starting KingbaseES V009R001C010
[2026-10-08T23:57:01+08:00] PASS: server license metadata valid_days=180 reported_dates=2025-11-24 (date role unconfirmed; license text suppressed)
[2026-10-08T23:57:04+08:00] PASS: identifier comparison quoted=mixed unquoted=mixed
[2026-10-08T23:57:04+08:00] PASS: ksql version, PG mode, CRUD, LIMIT/OFFSET and identifier probes
host_pgx_user=agentsql_ro mode=pg phone=13812345678 denied_write=ERROR: permission denied for table agentsql_batch72_verify (SQLSTATE 42501) version=KingbaseES V009R001C010
[2026-10-08T23:57:13+08:00] PASS: host pgx/v5 $1 bind, SCRAM and readonly SQLSTATE 42501
[2026-10-08T23:58:50+08:00] PASS: offline go build ./...
[2026-10-08T23:59:18+08:00] SELECT decision=allow audit_id=1 masked_cells=1 row=1|Alice|138****5678
[2026-10-08T23:59:25+08:00] PASS: ProjectionLineages physical origins match the synthetic V9 table
[2026-10-08T23:59:25+08:00] R006 decision=deny audit_id=2 rule_ids=R006
[2026-10-08T23:59:25+08:00] Audit total=2 decisions=deny,allow
[2026-10-08T23:59:25+08:00] PASS: V9 PG protocol, readonly account, AgentSQL query, scoped masking, R006 and audit
[2026-10-08T23:59:30+08:00] PASS: temporary containers, synthetic role/table and license named volume removed
```

`ksql` 实际执行了 `SELECT version()`、`SHOW database_mode`（`pg`）、建表、插入、查询、更新（`UPDATE 1`）、删除（`DELETE 1` 后 `count(*)=0`）、`LIMIT 1 OFFSET 0`。带引号 `"MiXeD"` 和不带引号 `MiXeD` 的输出表头均为 `mixed`；因此不声称与 PostgreSQL 的标识符大小写行为完全相同。宿主机 pgx/v5 查询使用 `$1` 绑定；HBA 的 TCP 条目为 `scram-sha-256`，只读角色的写入拒绝错误码为 `42501`。

本轮 `pg` 模式不接受显式 `GRANT CREATE SESSION TO agentsql_ro`：单独探索运行在 `2026-10-08T23:33:46+08:00` 报 `syntax error at or near "SESSION"`，该次脚本非零退出并清理。最终脚本采用已实测可用的 `CREATE ROLE ... LOGIN`、`GRANT CONNECT`、schema `USAGE` 和单表 `SELECT`；SCRAM 登录和查询成功，UPDATE 被拒。**显式 CREATE SESSION 语法未通过，功能上的创建会话能力已验证。**

AgentSQL 使用仓库现有 `db_type=postgres`，以最小权限角色连接 V9。本轮没有新增 `kingbase` 类型或修改 `internal/`。普通查询 `decision=allow`，`MaskedCells=1`，手机号 `138****5678`；同一 SQL 的 `ProjectionLineages` 三列分别指向 `public.agentsql_batch72_verify.id/name/phone`；注释规则探针 `decision=deny` 且 hits 含 `R006`；审计恰有两条，分别为 allow、deny，没有 parse-error。注释被 parser 接受，本项不是未验证。

## 离线构建、测试与清理

最终脚本在已缓存的 `golang:1.26-bookworm` 容器中，用 `--network none`、`GOPROXY=off`、`GOTOOLCHAIN=local`、`CGO_ENABLED=1` 执行 `go build ./...`（23:58:50 PASS），并构建 AgentSQL 可执行文件（23:59:11 PASS）；未下载依赖。之前一次完整闭环运行中，独立执行相同离线参数下的 `go test -short ./internal/parser ./internal/mask`，输出为 `parser 12.133s`、`mask 89.797s`，并在 23:29:58 输出 PASS。随后一次重复短测试在约 12 分钟仍无结果，经停止测试容器而非零退出；该次脚本按失败路径清理，**不记为测试通过**。最终一键脚本去掉重复短测试；本批没有改 `internal/`，故不存在需要回归测试的改动包。最终运行证据以固定路径 transcript 为准，独立短测试结果来自本轮较早的实际终端输出。

## 未测边界

| 范围 | 状态 |
| --- | --- |
| 多行数据与深度分页、JOIN、聚合和一般函数、复杂表达式、类型 OID、系统目录、`EXPLAIN` | 未测；本轮仅单行 SQL 与一个 `LIMIT/OFFSET` 探针 |
| 列级授权 B2、多角色并发、长连接/超时取消、生产负载 | 未测 |
| TLS/加密传输、HA/故障切换、备份恢复 | 未测 |
| V9 Oracle 模式、原生 Kingbase 驱动、跨版本差异 | 未测；本轮明确是 `pg` 模式与现有 PostgreSQL 协议接线 |
| 实际 license 到期日及后续续期行为 | 未确认；仅得到服务器报告的 180 天和含义未明的日期 |

## 改动清单

- `scripts/kingbase-v9-verify.ps1`：复用已加载镜像；将只读 bind mount 改为一次性容器 `docker cp` 至可写 named volume，并校验尺寸、SHA-256、属主/权限、写入能力和容器挂载 RW；把卷删除纳入成功与失败清理；加入仅输出许可元数据的服务器探针；批次名改为 72；标识符对照按真实结果记录。
- `docs/kingbase-v9-verification.md`：更新为本次真库证据、前两批对照、边界和档位建议。
- `internal/`：**无文件改动**，无最小业务接线修复。
