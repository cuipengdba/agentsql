# KingbaseES V9 真库验证：批六十九

验证时间：2026-10-08（Asia/Shanghai）。完整终端 transcript：`C:\Users\Administrator\AppData\Local\Temp\kingbase-v9-batch69-transcript.txt`。脚本：[kingbase-v9-verify.ps1](../scripts/kingbase-v9-verify.ps1)。本批未运行任何 git 命令，未修改矩阵、README、SECURITY.md 或 `internal/`。

## 结论

**FAIL，V9 不能转绿。** 新 tar 已从指定路径执行 `docker load`；image ID 与批六十四完全相同，**镜像未变，仅 license 更新**。新 license 以只读 bind mount 注入成功，但服务器在启动阶段报 `License file should have write access mode in floating mode`，退出前尚未进入产品版本校验。因此不能声称新的产品码已经匹配，也没有 V9 SQL、PG 协议或 AgentSQL 闭环通过证据。

本批按“license 文件只读挂载、不复制”的约束停止。批六十四曾把旧 license 复制到可写 Docker 卷以越过该权限检查；本批脚本删除了那条路径。[金仓官方 License FAQ](https://bbs.kingbase.com.cn/kingbase-doc/v9.3.11/faq/faq-new/license.html)说明浮动基准日期启用时，到期日按更换日期计算；该文档未给出让当前浮动授权在只读文件上启动的配置。需要厂商提供可按生成日期为基准且允许只读挂载的授权，或用户调整只读/不复制约束后，才能重跑下游项目。

建议兼容矩阵继续保持 **🟡 待验证**，由用户拍板；本批不能转绿。V8 批六十四已通过的对照结果不替代 V9 结果。

## 输入、镜像和授权元数据

| 项目 | 批六十九实测 |
| --- | --- |
| 新镜像文件 | `D:\ruanjiansheji\db-images\jincang\KingbaseES_V009R001C010B0004_x86_64_Docker.tar` |
| 新镜像大小 / SHA-256 | 765,955,072 bytes / `16A436608CC204349E510CB136B8FC1FCBDF6874AEE7B204CDAC20A3522282DA` |
| `docker load` tag | `kingbase_v009r001c010b0004_single_x86:v1` |
| 新加载 image ID | `sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724` |
| 批六十四旧 image ID | `sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724`，完全相同 |
| Entrypoint / User | `["/bin/bash","/home/kingbase/docker-entrypoint.sh"]` / `kingbase` |
| 暴露端口 | `54321/tcp` |
| 镜像环境变量 | `PATH=/usr/local/bin:/bin:/usr/bin:/usr/local/sbin:/usr/sbin:/home/kingbase/install/kingbase/bin:`；`USER=kingbase` |
| 新 license 文件 | `D:\ruanjiansheji\db-images\jincang\license_4_V009R001C-企业版-180天.dat` |
| 新 license 大小 / SHA-256 | 5,036 bytes / `03793D90E5C23CD2F4303019D1EE6888D13D4679739923D5FDFCDBCDA648B1E9` |
| 旧 license（仅对照） | `license-180.dat`，4,992 bytes / `3CF5A2109695E59A847F012CF11849B616B631D212C192AEFB76194AB2A594FE` |
| 新 license 到期日 | **未取得**：服务器未启动，不能调用 `get_license_info()` 或从日志确认。文件名中的“180天”不能推算实际到期日。 |

镜像入口脚本确认 `DB_PATH=/home/kingbase/install/kingbase`、数据目录 `/home/kingbase/userdata/data`、默认用户 `system` 和数据库 `kingbase`。`initdb --help` 的默认模式为 `oracle`；本批明确传 `-m pg`，但数据库未启动，`SHOW database_mode` 未执行。入口脚本会移动 `bin/license.dat` 至 `etc/license.dat`；单文件只读挂载不能移动，因此脚本按已核对的 `initdb`/`sys_ctl` 路径启动，并让 `bin/license.dat` 指向 `/license/license.dat` 只读挂载。没有复制、改写或打印 license 内容。

## 真实输出和批六十四对照

```text
[2026-10-08T17:47:57+08:00] Load supplied V9 image
Loaded image: kingbase_v009r001c010b0004_single_x86:v1
[2026-10-08T17:50:21+08:00] PASS: image ID equals batch64: image unchanged; only license input changed
[2026-10-08T17:50:48+08:00] PASS: read-only license mount metadata bytes/mode/owner=5036|777|root:root
[2026-10-08T17:50:51+08:00] Container=kingbase-v9-batch69 host=127.0.0.1:50542 container_port=54321 license_mount=readonly
[2026-10-08T17:51:14+08:00] FAIL: database did not become ready; database logfile follows
FATAL:  XX000: License file should have write access mode in floating mode, or use license generating date as base date.
LOCATION:  KesMasterMain, master.c:1108
[2026-10-08T17:51:14+08:00] FAIL: V9 readiness/license verification failed; PG protocol and AgentSQL checks were not run
[2026-10-08T17:51:15+08:00] PASS: temporary containers and their synthetic role/table removed
```

最终一键脚本退出码为 **1**。批六十四在**可写副本**上越过写权限检查后，报 `productVersion check failed. server is 'V009R001B' but license is 'V009'`。本批因只读权限检查先失败，不能据此判断新 license 的产品码是否解决了批六十四问题。新 license 源文件运行后 SHA-256 仍为 `03793D90E5C23CD2F4303019D1EE6888D13D4679739923D5FDFCDBCDA648B1E9`；Docker 中本批两个临时容器均已不存在。

## 验收步骤

| 步骤 | 本批状态 | 证据或原因 |
| --- | --- | --- |
| 新 tar 加载、镜像元数据对照 | PASS | `docker load` 退出 0；tag、ID、Entrypoint、User、端口、环境变量见上。 |
| license 挂载路径和权限 | PASS | `/license/license.dat` 是源文件的只读 bind mount；容器内 `stat` 为 `5036|777|root:root`。`bin/license.dat` 仅为指向它的符号链接。 |
| V9 启动、版本和到期日 | FAIL | 数据库日志为浮动模式写权限 `FATAL XX000`；无正常启动、无到期日。 |
| `ksql` 版本、CRUD、LIMIT、标识符大小写 | 未执行 | 启动前置失败。 |
| 宿主机 pgx/v5、`$1`、SCRAM、只读角色拒写错误码 | 未执行 | 启动前置失败；没有临时角色创建。 |
| AgentSQL `db_type=postgres` 接入、SELECT、脱敏、MaskedCells | 未执行 | 启动前置失败；仓库注册表无 `kingbase`，脚本预备使用现有 PostgreSQL 类型。 |
| ProjectionLineages 物理来源 | 未执行 | 脚本含同一 SQL 的独立 parser 探针；本批未运行，不能算通过。 |
| R006 注释探针、RuleID、allow/deny/parse-error 审计数量 | 未执行 | 启动前置失败；不得算通过。 |
| 临时用户、表和 AgentSQL 容器清理 | PASS（本次无创建） | 本批数据库未启动，未创建角色/表或 AgentSQL 容器。最终脚本失败清理结果见 transcript。 |

脚本非零退出即 FAIL，并输出步骤行。AgentSQL HTTP 仅发布在 `127.0.0.1` 临时端口。V9 容器在最终脚本中停止并删除；license 源文件仍在原路径，未复制。批六十四的旧容器和 V8 对照库不在本批清理范围。

## 构建和测试

离线命令使用已缓存的 `golang:1.26-bookworm`，`--network none`、`GOPROXY=off`、`GOTOOLCHAIN=local`、`CGO_ENABLED=1`，仓库与模块缓存只读挂载。独立执行 `go build ./...` **退出 0，PASS**。没有 `internal/` Go 包改动；另外尝试 `go test -short ./internal/parser ./internal/mask ./internal/pipeline`，其中 `internal/parser` 输出 `ok`（14.669s），`internal/mask` 输出 `ok`（101.802s），`internal/pipeline` 因缺少已缓存的 `github.com/moby/sys/user@v0.4.1`，在只读模块缓存创建锁文件时报错 `read-only file system`，**整体退出 1，pipeline 未通过**。后续单独重复 `parser`/`mask` 测试时，`parser` 输出缓存命中，但容器超过 10 分钟仍未给出 `mask` 结果，已停止，退出 1；这次重复测试不记为通过。脚本在 V9 启动后会执行 `go build ./...`、`go test -short ./internal/parser ./internal/mask` 及 AgentSQL 二进制构建；本轮因启动失败未到达脚本内的构建阶段。

## 未测边界

| 范围 | 状态 |
| --- | --- |
| V9 基础单行 CRUD、分页、大小写 | 未测：数据库未启动。 |
| 多行分页、JOIN、聚合和函数、类型 OID、系统目录、`EXPLAIN` | 未测。 |
| 连接超时与取消、TLS/加密、HA/故障切换 | 未测。 |
| B2 列授权、多用户并发、生产负载 | 未测。 |
| V9 与原生 PostgreSQL 行为差异 | 未测。 |

## 改动与清理

仅更新本脚本和本报告；**无 `internal/` 业务代码改动**，无需要逐处说明的内部接线修复。脚本相对批六十四的主要 diff：默认输入改为新路径；移除 license 可写卷、license 的 `docker cp`、`chown`、`chmod`；改为只读 bind mount 与启动失败立即报错；增加镜像对照、完整 PASS/FAIL、固定 Temp transcript、下游 SQL/驱动/血缘/AgentSQL 检查及清理步骤。后续分支尚无本次真库执行证据。
