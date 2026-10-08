# KingbaseES V9 本地真库验证（批六十四）

验证日期：2026-10-08（Asia/Shanghai）。仓库基线：`395c8815bf802caeef7a311c907e300342fe7748`。所有容器端口只发布在 `127.0.0.1`；未向外网部署或推送镜像。

## 结论与范围

**V9 未通过启动前置条件。** 用户提供的 `license-180.dat` 已放在本地 Docker 专用可写卷，数据库初始化完成，但数据库日志在启动时报告：

```text
FATAL:  XX000: productVersion check failed. server is 'V009R001B' but license is 'V009'
LOCATION:  KesMasterMain, master.c:1116
```

因此 V9 的 `SELECT version()`、CRUD、宿主机 PostgreSQL 协议连接、AgentSQL 接入、解析、血缘、审计、规则和脱敏**全部未验证**。上面的 `server is 'V009R001B'` 是许可校验报出的产品码，不能替代数据库 `SELECT version()` 的结果。镜像标签含 `V009R001C010B0004`，但本次无法核实服务实际版本。建议兼容矩阵仍保持当前 **🟡 待验证**，待用户提供与该镜像匹配的许可后重跑；本批未修改矩阵或 `SECURITY.md`。

V8 对照库已启动，并完成数据库、宿主机 PG 协议以及当前源码 AgentSQL 的有限防护闭环。V8 结果不代表 V9。

## 输入与镜像探查

输入仅为用户提供的 `D:\ruanjiansheji\db-images` 下两组 tar/license。V9 tar 765,955,072 字节、SHA-256 `16A436608CC204349E510CB136B8FC1FCBDF6874AEE7B204CDAC20A3522282DA`；V9 license 4,992 字节、SHA-256 `3CF5A2109695E59A847F012CF11849B616B631D212C192AEFB76194AB2A594FE`。验证结束后源 license 哈希未变化。

实际命令：

```powershell
& 'E:\Docker\DockerDesktop\resources\bin\docker.exe' load -i 'D:\ruanjiansheji\db-images\KingbaseES_V009R001C010B0004_x86_64_Docker.tar'
& 'E:\Docker\DockerDesktop\resources\bin\docker.exe' image inspect kingbase_v009r001c010b0004_single_x86:v1 --format '{{json .}}'
& 'E:\Docker\DockerDesktop\resources\bin\docker.exe' run --rm --entrypoint /bin/bash kingbase_v009r001c010b0004_single_x86:v1 -c 'cat /home/kingbase/docker-entrypoint.sh'
& 'E:\Docker\DockerDesktop\resources\bin\docker.exe' run --rm --entrypoint /home/kingbase/install/kingbase/bin/initdb kingbase_v009r001c010b0004_single_x86:v1 --help
```

关键输出原文：

```text
Loaded image: kingbase_v009r001c010b0004_single_x86:v1
Id: sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724
RepoDigest: kingbase_v009r001c010b0004_single_x86@sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724
Author: KDBDEV support@kingbase.com.cn
Config.User: kingbase
Config.ExposedPorts: 54321/tcp
Config.Entrypoint: /bin/bash /home/kingbase/docker-entrypoint.sh
```

镜像 `Author` 是镜像元数据，并非发行来源的独立签名校验。入口脚本定义 `DB_PATH=/home/kingbase/install/kingbase`、默认 `DATA_DIR=/home/kingbase/userdata/data`、默认 `DB_USER=system`，并将 `DB_NAME="kingbase"` 用于 ksql 环境变量。脚本在初始化后执行 `mv ${DB_PATH}/bin/license.dat ${etc_PATH}/license.dat`，再给 `bin/license.dat` 建符号链接。`initdb --help` 原文为 `-m, --dbmode=MODE set database mode(default value is oracle)`。本次明确传入 `-m pg`；V9 运行模式因服务未启动而未能用 `SHOW database_mode` 复核。

### license 挂载与启动尝试

先做过一次只读单文件挂载，源路径为用户提供的 V9 license，目标由入口脚本的 `bin/license.dat` 推得。数据库日志：

```text
FATAL:  XX000: License file should have write access mode in floating mode, or use license generating date as base date.
LOCATION:  KesMasterMain, master.c:1108
```

入口脚本还报告 `mv: ... Device or resource busy`，因为单文件 bind mount 无法被其移动。随后改为把 license 仅复制到本地 Docker 卷 `kingbase-v9-batch64-license`，设为 `kingbase:kingbase`、`0600`，挂载到入口脚本定义的数据目录同级 `etc`，让 `bin/license.dat` 指向该卷。使用镜像脚本中确认的 `initdb`、`sys_ctl` 路径执行初始化和启动。原始 license 文件不被挂载为可写文件，也未复制进仓库或产物。第二次数据库日志不再报告写权限错误，改为上文产品码不匹配。卷内副本仅在本地 Docker 供本批验证使用。

## V9 一键脚本与一次完整运行日志

脚本：[kingbase-v9-verify.ps1](../scripts/kingbase-v9-verify.ps1)。默认命令加载用户提供的 tar；`-ReuseLoadedImage` 从同一 tar 的 `manifest.json` 读标签并检查本地已加载镜像，用于快速复跑。脚本重建本批专用容器和 license 卷，请勿在其中存放其它数据。数据库就绪失败会读取数据库 `logfile` 并以非零状态退出。取得匹配 license 后，脚本还会从宿主机用 pgx 连库、离线构建当前 AgentSQL 源码并调用本机回环 MCP 验证防护；这些 V9 后续分支尚无实际运行证据。

```powershell
$log = Join-Path $env:TEMP 'kingbase-v9-batch64-final-run.log'
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\kingbase-v9-verify.ps1 *>&1 | Tee-Object -FilePath $log
exit $LASTEXITCODE
```

完整输出（本次退出码 1；末尾为 Windows PowerShell 的异常定位）：

```text
[2026-10-08T12:10:27+08:00] Load supplied V9 image
Loaded image: kingbase_v009r001c010b0004_single_x86:v1
[2026-10-08T12:11:34+08:00] Image tag=kingbase_v009r001c010b0004_single_x86:v1 id=sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724 digest=kingbase_v009r001c010b0004_single_x86@sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724 author=KDBDEV support@kingbase.com.cn
[2026-10-08T12:11:34+08:00] Image entrypoint=/home/kingbase/docker-entrypoint.sh exposed_port=54321 user=kingbase
[2026-10-08T12:11:34+08:00] Inspect vendor entrypoint and initdb metadata
[2026-10-08T12:11:45+08:00] Vendor data_dir=/home/kingbase/userdata/data default_user=system default_database=kingbase default_mode=oracle
[2026-10-08T12:11:45+08:00] Vendor license source=/home/kingbase/install/kingbase/bin/license.dat runtime=/home/kingbase/install/kingbase/etc/license.dat image_license_bytes=4995
[2026-10-08T12:11:45+08:00] Supplied license bytes=4992 sha256=3CF5A2109695E59A847F012CF11849B616B631D212C192AEFB76194AB2A594FE
[2026-10-08T12:11:45+08:00] Stage supplied license in dedicated local Docker volume
[2026-10-08T12:11:54+08:00] Start V9 in explicit pg mode with loopback-only published port
[2026-10-08T12:11:57+08:00] Container=kingbase-v9-batch64 host=127.0.0.1:52537 container_port=54321 license_volume=kingbase-v9-batch64-license
[2026-10-08T12:11:57+08:00] Wait for database, checking database logfile on every failure
[2026-10-08T12:12:45+08:00] FAIL: database did not become ready; database logfile follows
FATAL:  XX000: productVersion check failed. server is 'V009R001B' but license is 'V009'
LOCATION:  KesMasterMain, master.c:1116
powershell : V9 readiness/license verification failed; PG protocol and AgentSQL checks were not run
所在位置 行:2 字符: 63
+ ... l-run.log'; powershell -NoProfile -ExecutionPolicy Bypass -File .\scr ...
+                 ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
    + CategoryInfo          : NotSpecified: (V9 readiness/li...ks were not run:String) [], RemoteException
    + FullyQualifiedErrorId : NativeCommandError

At D:\ruanjiansheji\agentsql-v04\scripts\kingbase-v9-verify.ps1:183 char:5
+     throw 'V9 readiness/license verification failed; PG protocol and  ...
+     ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
    + CategoryInfo          : OperationStopped: (V9 readiness/li...ks were not run:String) [], RuntimeException
    + FullyQualifiedErrorId : V9 readiness/license verification failed; PG protocol and AgentSQL checks were not run
```

上述为默认一键命令的完整输出，数据库原始错误在脚本的 PowerShell 调用栈之前。另有一次 `-ReuseLoadedImage` 复跑得到相同授权错误。临时运行日志未加入仓库，避免在允许文件清单之外增加文件。

## V8 对照实测

V8 tar 加载结果：`kingbase_v008r006c009b0014_single_x86:v1`，镜像 ID `sha256:c9e9fdb309b6b18f022e16a8cc4ea91108bf1e609e3ac134e0050a82a01ed5d9`。其入口脚本同样使用 `bin/license.dat` 并在初始化后移至 `etc/license.dat`，`Config.ExposedPorts=54321/tcp`，默认用户 `system`、默认模式 `oracle`。用户提供的 V8 license SHA-256 为 `45A151AB295336A5B3926424D470B21363340A9C8408A7E34AAF13CA84F51E34`，与镜像内置 license 的 `df827d065e9ca1e405fada2f25082d3ce53223225f95b15773754574230c8f3b` 不同。源 license 验证结束后哈希未变化。

用同样的 Docker 专用可写卷方式，以 `-m pg` 启动 `kingbase-v8-batch64`；数据库日志显示：

```text
server started
starting KingbaseES V008R006C009B0014 on x86_64-pc-linux-gnu, compiled by gcc (GCC) 4.8.5 20150623 (Red Hat 4.8.5-28), 64-bit
listening on IPv4 address "0.0.0.0", port 54321
```

容器内 ksql 实测：

```text
SELECT version();       -> KingbaseES V008R006C009B0014 on x86_64-pc-linux-gnu, compiled by gcc (GCC) 4.8.5 20150623 (Red Hat 4.8.5-28), 64-bit
SHOW database_mode;     -> pg
CREATE TABLE ...        -> CREATE TABLE
INSERT ...              -> INSERT 0 1
SELECT ... WHERE id=1   -> 1|batch64|13812345678
UPDATE ... WHERE id=1   -> UPDATE 1
SELECT ... WHERE id=1   -> 1|updated
DELETE ... WHERE id=1   -> DELETE 1
SELECT count(*) ...     -> 0
```

宿主机使用项目依赖的 `github.com/jackc/pgx/v5`，连 Docker 实际回环映射端口，并执行 `$1` 参数绑定，原始结果：

```text
user=system mode=pg bound_result=42 version=KingbaseES V008R006C009B0014 on x86_64-pc-linux-gnu, compiled by gcc (GCC) 4.8.5 20150623 (Red Hat 4.8.5-28), 64-bit
readonly_user=agentsql_ro phone=13812345678 denied_write=ERROR: permission denied for table agentsql_batch64_verify (SQLSTATE 42501)
```

`agentsql_ro` 只获 `CONNECT`、`USAGE` 和合成表的 `SELECT`，上述拒绝写入在事务中执行并回滚。V8 `sys_hba.conf` 的 `host ... 127.0.0.1/32` 与 `host ... 0.0.0.0/0` 行配置为 `scram-sha-256`，容器内 `local` 行为 `trust`；这是 V8 当前实例配置，V9 认证方式尚未验证。

### V8 AgentSQL 当前源码防护闭环

Windows 宿主机未提供 CGO 编译器，原生 `go build` 报 `pg_query` 的 CGO API 未定义。改用本地已有 `golang:1.26-bookworm`，只读挂载当前仓库与 Go 模块缓存，`--network none` 离线执行 `CGO_ENABLED=1 go build -o /out/agentsql ./cmd/agentsql`，退出码 0。构建产物仅在本地 Docker 卷 `kingbase-batch64-go-build`。运行该二进制的 AgentSQL 容器只将 HTTP 端口映射到 `127.0.0.1`，`GET /healthz` 返回：

```json
{"status":"ok","version":"v0.5.0","b2":{"state":"feature-off","reason":"B2_FEATURE_OFF","protocol":2},"b5":{"enabled":true,"state":"READY","reason":"B5_READY","ready":true}}
```

测试配置将 `column_authorization.enabled=false`，所以**没有 B2 列级授权通过结论**。管理 API 建立 `db_type=postgres` 的 V8 数据源、`readonly` Agent、仅合成表的 allow 权限和 `phone` 列精确脱敏规则。通过 MCP `query` 提交普通 SELECT 与带块注释的 SELECT，再经管理 API 读取审计。第二次完整断言的原始输出：

```text
query_decision=allow audit_id=3 masked_cells=1 row=1|Alice|138****5678
deny_decision=deny audit_id=4 rule_ids=R006
audit_total=2 decisions=deny,allow
v8_agentsql_guard=PASS
```

脱敏命中精确表列规则，说明该样例的解析与来源匹配足以驱动规则；未直接导出内部 `ProjectionLineages`，不宣称完成独立血缘结构核验。`R006` 拒绝来自 AgentSQL，不是数据库原生拒绝。原生 PG 的本批并行对照未运行。

复核时，V8 数据库映射为 `127.0.0.1:62921->54321/tcp`，AgentSQL 映射为 `127.0.0.1:53414->7780/tcp`。两者均在本地 Docker 私有网络互连；V9 容器因许可错误退出，保留供日志复核。V8/V9 license 副本仅位于各自专用 Docker 卷内，临时 staging 容器已删除。

## 差异与未验证项

| 项目 | V9 本次 | V8 对照 | 原生 PostgreSQL 对比 |
| --- | --- | --- | --- |
| 镜像版本 | 标签 `V009R001C010B0004`，SQL 版本未取得 | `SELECT version()` 为 `V008R006C009B0014` | 未在本批重新启动原生 PG 对照 |
| 默认模式 | `initdb --help` 写 `oracle`；本次传 `-m pg` 但服务未就绪 | 显式 `-m pg`，`SHOW database_mode` 为 `pg` | PG 无金仓 `database_mode` 模式选择；不据此判定兼容范围 |
| 许可 | 可写卷消除了写权限错误，但产品码不匹配 | 所给 license 允许数据库启动 | PG 不涉及金仓许可校验 |
| 协议/认证 | 未验证 | pgx 宿主机连接、`$1` 参数绑定和 SCRAM 配置的账号通过 | 有限样例未观察到 V8 协议差异；V9 不可推断 |
| SQL/系统视图/EXPLAIN | 未验证 | 仅基础 CRUD、`version()`、`database_mode` 已验证 | 未覆盖方言、catalog、OID、`EXPLAIN (FORMAT JSON)` 差异 |
| AgentSQL 查询、解析、血缘、审计、R006、脱敏 | 未验证 | 当前源码的 SELECT、精确列脱敏、R006 拒绝和审计已通过；内部血缘结构未单独导出 | 不可依据 PostgreSQL 或 V8 推断 V9 |

V9 尚需匹配 license 后重新验证：容器内 CRUD；宿主机 PG 协议、认证与参数绑定；AgentSQL `db_type=postgres` 数据源连接、只读查询、SQL 解析与血缘、审计、R006 拒绝、脱敏与数据库最小权限；`information_schema` / `pg_catalog`、类型 OID、`EXPLAIN (FORMAT JSON)`、标识符大小写、错误码以及连接超时/取消行为。当前没有证据支持修改 `internal/` 代码。

## 自查

- 没有执行 git 写入命令；只读使用 `git rev-parse HEAD`、`git status`、`git diff --check`。
- 没有改兼容矩阵、`SECURITY.md`、README 或 joint-case 状态。
- 没有把 license 复制进仓库、构建产物或无关位置；源文件哈希未变化。本地 Docker 专用 license 卷供复核，删除该卷即可清理其副本。
- 本次开始前仓库已有两项未跟踪内容：`cmd/agentsql/b5-wal/` 与 `cmd/agentsql/memory.instance-id`；本批未触碰。
