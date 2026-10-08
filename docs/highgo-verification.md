# 批七十：瀚高 HighGo 9.0 企业版真库验证

验证日期：2026-10-08（Asia/Shanghai）。完整终端记录：`C:\Users\Administrator\AppData\Local\Temp\highgo-batch70-transcript.txt`。本批未执行 git 命令，未修改矩阵、README、SECURITY.md 或 `internal/` 业务代码。

## 输入与镜像

| 项目 | 实测值 |
| --- | --- |
| 官方安装包 | `hgdb-9.0.10.2-0-5936-59427d7-20260921.linux.x86_64.bin` |
| 安装包大小、SHA-256 | 670,185,691 bytes；`4B13BFACC55D753D42D21613020691AB2E20B3E84D275338B80C268AEA5D4BD9` |
| license | `9147d1c2_A20261008N4540397q0lLU.dat`，2,837 bytes；SHA-256 `3A58CA130FAD31E48CBAD24959362F3F966F5F988B4A63B98560879F309C4A4E`。只读挂载，未复制或输出内容 |
| 基础镜像 | 本机已缓存 `rockylinux:8`，image ID 前缀 `9794037624aa`；容器内 Rocky Linux 8.9、glibc 2.28 |
| 构建镜像 | `agentsql-highgo:9.0.10-b70`；image ID `sha256:338d2708c76d283b90b81c64a45e16f76c18f2aa0a5f1c154857b1fd927b5a41`；3,579,883,725 bytes |
| 授权元数据 | 官方 `licchk -T`：`2027-04-08`；`-U`：`highgo`；`-P`：`5866`；`-D`：`highgo`；`-H`：`瀚高数据库管理系统V9.0-企业版-9.0.10`；`--license-status`：`normal` |

任务背景写 5432；**这份 license 和安装器给出的端口都是 5866**，所以本轮按 5866 启动和测试。`SELECT version()` 报 PostgreSQL 14.23 内核；服务端启动日志给出瀚高 V9.0 企业版 9.0.10 产品版本与授权有效期。授权日志称其为 trial version，有效期至 2027-04-08。

## 安装包探查与构建依据

安装包首字节为 `#!/bin/sh`，头部写明 `Makeself 2.7.1`；`--info` 返回压缩方式 gzip、内部入口 `./luancher.sh`，`--list` 显示 `hgdb_install.jar`、自带 JDK 和入口脚本。对 `luancher.sh` 使用 `--tar xOf` 只读提取后，帮助原文列明 `-defaults-file [path] -auto` 为完全静默安装，以及 `-options`、`-options-system` 的交互式错误处理差异。本批使用厂商明确列出的 `-defaults-file /tmp/highgo-install.properties -auto`。

安装器 JAR 中 `LicenseSelectPanelAutomationHelper` 读取 `LICENSE_FILE_PATH`，`CustomTargetPanelAutomationHelper` 读取 `INSTALL_PATH`，`DatabaseInitPanelAutomationHelper` 读取 `DB_INIT_ENABLED`、`DB_MODE`。JAR 中 `DefaultVariableUtil` 把 PG 端口默认值列为 `5866`，授权检查工具的 `-P` 对本 license 也返回 5866。内置 `pack-Scripts` 的脚本写明 `mkdir -p $INSTALL_PATH/license` 后运行 `cp -p $LICENSE_FILE_PATH $INSTALL_PATH/license/license.dat`，据此确认最终授权路径为 `/opt/highgo/license/license.dat`。

为了满足只读挂载和不复制授权文件，Dockerfile 在该最终路径预先创建零字节占位文件，BuildKit secret **只读挂载到同一路径**。安装器按其原有流程验证授权，复制动作无法把字节写入目标挂载；构建包装脚本随后检查安装二进制存在、该目标仍是挂载点且安装目录中只有这一处 `license.dat`。镜像导出后，另用 `stat` 确认目标文件为 **0 bytes**。运行时同一路径通过 `readonly` bind mount 接入原始 license。安装包仅以临时硬链接加入 Docker 构建上下文，构建后移除该链接；原文件只读使用。

本机缓存的 Rocky Linux 8.9 上，安装器自带 Java 17.0.9 可执行，`ldd` 为 glibc 2.28；安装后的 `postgres` 链接检查没有缺库。安装器要求普通用户运行，因此构建时创建 `highgo` UID 2000 并以该用户运行静默安装。安装器启动脚本即使 Java 安装失败也会返回 0，本批额外检查 `initdb`、`pg_ctl`、`postgres`、`psql` 实际产物，避免假阳性。全部 Docker 构建命令使用 `--network none`、`--pull=false`，未拉取基础镜像或依赖。

构建的关键实际输出：

```text
Loaded 5 override(s) from /tmp/highgo-install.properties
License validated successfully: /opt/highgo/license/license.dat
Installation started
Total packs = 7
[ Unpacking finished ]
Installation finished
PASS installer binaries and sole license mount verified
postgres (PostgreSQL) 14.23
sha256:338d2708c76d283b90b81c64a45e16f76c18f2aa0a5f1c154857b1fd927b5a41 3579883725
PASS: offline HighGo image built without license bytes in image
```

首次尝试用 root 运行时，安装器打印 `run as non-root user please` 后直接结束，且无 `initdb`；脚本以缺少产物判失败。普通用户安装的首次后置检查因精简基础镜像没有 `find` 而失败；改用 Bash glob 后重新构建成功。这两次非零构建均未当作通过。

## 真库输出

启动日志关键行：

```text
starting PostgreSQL 14.23 on x86_64-pc-linux-gnu
listening on IPv4 address "0.0.0.0", port 5866
This is an trial version of 瀚高数据库管理系统V9.0-企业版-9.0.10, valid until 2027-04-08.
database system is ready to accept connections
```

容器内 `psql` 使用合成表 `public.agentsql_batch70_verify`，已取得以下真实输出：

```text
SELECT version() = PostgreSQL 14.23 on x86_64-pc-linux-gnu, compiled by gcc (GCC) 10.4.0, 64-bit
current_database() | current_user = highgo | highgo
CREATE TABLE
INSERT 0 2
SELECT = 1|Alice|13812345678 ; 2|Bob|13911112222
UPDATE 1
ORDER BY id LIMIT 1 OFFSET 1 = 2
quoted alias = MiXeD ; unquoted alias = mixed
DELETE 1
SELECT count(*) = 1
```

只读角色 `agentsql_b70_ro` 仅授予 CONNECT、public schema USAGE 和合成表 SELECT。`pg_hba.conf` 初始 TCP 规则为 `scram-sha-256`；验证脚本将本轮 Docker 私有网段追加为 SCRAM host 规则并 reload。宿主机端口、AgentSQL API 端口只向 `127.0.0.1` 发布。

AgentSQL 的真实输出：

```text
Datasource id=hg-b70 db_type=postgres
SELECT decision=allow MaskedCells=1 row=1|Alice2|138****5678
ProjectionLineages[0]=id <- public.agentsql_batch70_verify.id
ProjectionLineages[1]=name <- public.agentsql_batch70_verify.name
ProjectionLineages[2]=phone <- public.agentsql_batch70_verify.phone
R006 decision=deny rule_ids=R006
Audit total=2 decisions=deny,allow
SQL cleanup owned_exit=0 table_exit=0 role_exit=0
```

AgentSQL 初始健康探针有两次连接关闭的临时错误，随后同一轮返回 healthy，最终闭环与清理均退出 0；完整顺序见 transcript。

块注释规则探针由 parser 接受，确实进入 R006 并被拒绝，所以本轮产生的审计正好是一次 allow、一次 deny。**未产生 parse-error**，该审计分支本轮没有触发；不能据此声称 parse-error 已实测。Masking 和规则均由 AgentSQL 处理，`column_authorization.enabled=false`，不包含 B2 列级授权结论。

## 验收与边界

| 验证项 | 状态 / 证据 |
| --- | --- |
| 官方包探查、静默安装和离线镜像 | PASS；见上文、Docker build transcript |
| license 真库启动、版本、到期日 | PASS；服务端日志和 `licchk` 元数据一致 |
| 容器客户端 CRUD、分页、标识符大小写 | PASS；见上文输出 |
| 宿主机 pgx/v5、`$1`、只读拒写码 | PASS；`pgx_user=agentsql_b70_ro bind_phone=13812345678 denied_write_sqlstate=42501`；TCP host 规则使用 SCRAM（最终默认脚本 20:46:28） |
| AgentSQL `postgres` 数据源、脱敏、MaskedCells | PASS；当前源码构建的 AgentSQL 容器健康、数据源 `db_type=postgres`；SELECT `decision=allow`、`MaskedCells=1`、合成手机号 `138****5678`（20:46:37） |
| ProjectionLineages、R006 和 allow/deny 审计 | PASS；三列物理来源准确；块注释查询 `decision=deny`、hit 包含 `R006`；按 Agent 过滤的审计 `total=2`、`deny,allow`（20:46:45）。parse-error 未触发 |
| 离线 `go build ./...`、`go test -short` | 最终默认脚本：`go build ./...` PASS，退出 0（20:39:19）；`go test -short ./internal/parser ./internal/mask ./scripts/highgo/pgx-probe` PASS：parser 12.308s、mask 88.924s、探针包无测试文件（20:45:22）。工作区 `.gocache/mod` 缺少五个模块的首次尝试非零退出，改用本机已有完整缓存只读挂载后通过 |

交付版 `verify.ps1` 的**最终默认完整执行**于 20:46:52 退出 **0**。它在同一次执行中通过离线 `go build ./...`、短测、数据库启动和 SQL、宿主 pgx、AgentSQL 和审计断言，并打印 `PASS: HighGo SQL, pgx, AgentSQL masking, lineage, R006 and audit checks`。这次没有使用临时复用二进制的参数。

尚未覆盖：TLS、连接池故障切换、取消/超时、`EXPLAIN`、扩展类型和 OID、系统目录差异、B2 列级授权、parse-error 审计、跨版本与生产负载。一次合成表查询的通过不代表这些能力通过。

脚本在成功或失败时均移除本轮 AgentSQL 容器、临时表和角色，并删除本轮瀚高容器及其容器内数据；**构建镜像保留**用于复核。若 SQL 清理出现错误，删除本轮容器仍会删除合成库内对象。脚本对步骤失败返回非零；license 源文件执行前后以 SHA-256 核对。无 `internal/` 最小修复，改动文件清单为空。

第一次 AgentSQL 部署尝试在数据库、宿主 pgx、离线 Go 构建和短测通过后，因验证脚本生成了 48 字符的 `AGENTSQL_SECRET` 而退出；AgentSQL 要求恰好 32 bytes。该次脚本非零退出，未记为闭环通过，且 `owned_exit=0 table_exit=0 role_exit=0`。脚本已改用 16 个随机字节的 32 字符十六进制密钥。随后先复用同批构建产物完成一次快速复验，再使用交付版默认脚本重新执行完整构建、短测和闭环；后者退出 0。临时复用产物现已删除。

复跑入口为 `scripts/highgo/run.ps1`，它先执行 `build.ps1`，构建退出 0 后再执行 `verify.ps1`；单独复用已构建镜像时直接运行 `verify.ps1`。两者均追加写入上述固定 transcript。检查结束后，`highgo-b70-*` 临时容器、网络、卷和一次性缓存二进制均已移除，仅保留 `agentsql-highgo:9.0.10-b70` 镜像。

## 建议矩阵档位（待用户拍板）

建议将**本次 HighGo 9.0.10 企业版、PG 模式、单实例合成表范围**标为 **🟢 完整防护（真库实测）**：已实测解析/血缘、表级授权、受控只读执行、手机号脱敏、R006 拒绝和 allow/deny 审计。该建议不覆盖上文未测能力，也不是厂商认证或生产支持承诺；矩阵档位仍由用户拍板。矩阵、README 和 SECURITY.md 本批均未改动。
