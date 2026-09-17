# AgentSQL 本地 Live Demo

这套环境只用于本机回环演示。它创建独立的 PostgreSQL 16、MySQL 8、SQLite 控制面和 AgentSQL 网关；不会也不应连接真实数据或真实数据库。

## 前置条件

- Docker Engine 或 Docker Desktop，以及支持 `--wait` 和 `service_completed_successfully` 的 Docker Compose v2。
- Linux/macOS 使用 Bash 与 `curl`；Windows 使用 PowerShell 5.1 或更高版本。
- 首次构建镜像需要能够取得项目已有依赖。离线环境应提前准备镜像和 Go module 构建缓存。

## 准备凭据

从仓库根目录复制环境文件：

```bash
cp examples/docker/demo.env.example demo/demo.env
```

```powershell
Copy-Item examples/docker/demo.env.example demo/demo.env
```

启动前必须替换 `demo/demo.env` 中的以下公开本地示例：

- `AGENTSQL_SECRET`：恰好 32 个 ASCII 字节，并与生成的 SQLite 卷一起保管。
- `AGENTSQL_ADMIN_PASSWORD`。
- `DEMO_PG_OWNER_PASSWORD` 与 `DEMO_MYSQL_ROOT_PASSWORD`。
- `AGENTSQL_DEMO_PG_PASSWORD` 与 `AGENTSQL_DEMO_MYSQL_PASSWORD`：两库 `agentsql_demo_ro` 账号的独立只读密码。
- `AGENTSQL_DEMO_RO_KEY` 与 `AGENTSQL_DEMO_DML_KEY`：用产品生成的合法 `asql_` key 替换示例值。

不要设置 `AGENTSQL_INSECURE`。环境样例中的值已公开，只适用于回环地址上的短期本地验证，不能用于公网或共享环境。

## 一条命令启动或重置

reset 脚本固定使用 Compose project `agentsql-demo` 和 `demo/demo.env`，未指定 anchor 时自动取当天 UTC。它会删除旧演示卷并重新创建完整数据：

```bash
bash ./demo/reset.sh
```

```powershell
.\demo\reset.ps1
```

环境文件放在其他位置时，可用 `AGENTSQL_DEMO_ENV_FILE=/absolute/path/demo.env`（Bash）或 `-EnvFile C:\path\demo.env`（PowerShell）。`down -v` 会永久删除此前的演示业务数据、SQLite 元数据和审计记录。

手工启动的等价命令必须自行提供同一个 UTC anchor：

```bash
export DEMO_ANCHOR_DATE="$(date -u +%F)"
docker compose -p agentsql-demo -f docker-compose.demo.yml --env-file demo/demo.env up -d --build --wait
```

```powershell
$env:DEMO_ANCHOR_DATE = [DateTime]::UtcNow.ToString('yyyy-MM-dd')
docker compose -p agentsql-demo -f docker-compose.demo.yml --env-file demo/demo.env up -d --build --wait
```

附带 Prometheus 与 Grafana 时，在手工命令中加入 `--profile observability`。

## 访问与数据范围

默认地址：

- AgentSQL 控制台：<http://127.0.0.1:17880>，使用 `AGENTSQL_ADMIN_USER` 和 `AGENTSQL_ADMIN_PASSWORD` 登录。
- PostgreSQL：`127.0.0.1:5432`；MySQL：`127.0.0.1:3306`。它们只供本地检查，可在环境文件中改端口。
- 可选 Prometheus：<http://127.0.0.1:9090>；Grafana：<http://127.0.0.1:3000>。

两套业务库各有 128 个 customers、64 个 products、2400 个 orders、16 个 internal_notes。控制面包含 2 个数据源、2 个 Agent、10 条策略、4 条脱敏规则、300 条近 30 天审计和 24 条审批。只读、写入拦截、敏感列、低基数扫描、未授权表和审计回看所需数据均已就绪。

## 真实试运行与六个剧本

控制台仅在 `/healthz` 严格返回 `demo.enabled=true` 和字符串 `demo.banner` 时显示 Live Demo UI。真实试运行调用 `POST /api/v1/playground/run`；该端点仅在 demo 模式注册，数据源固定为 `ds-demo-pg`/`ds-demo-mysql`，身份固定为 `ro`/`dml`。前端不接收、不缓存也不允许输入演示 Agent key。非 demo 部署没有该端点与 Live 控件，Playground 继续使用静态评估。

六个引导卡分别演示：

1. PostgreSQL 只读查询正常放行，返回最多 5 行并写审计。
2. MySQL 无 WHERE 的 UPDATE 被 R002 与演示只读屏障拦截，不触达业务库。
3. MySQL 查询 customers 的 phone/email，返回值全部掩码。
4. PostgreSQL 大结果扫描命中 R005，最多显示 20 行并标记截断。
5. PostgreSQL 访问未授权 internal_notes，被 R010 拒绝且不返回内容。
6. 使用本会话最近的 `audit_id` 打开审计详情，并可跳到总览实时流回看。

截图占位：`docs/images/demo-scenario-1.png` 至 `docs/images/demo-scenario-6.png`。这些路径由主控在真实浏览器验收后补充，本任务不生成占位图片。

## 每日 04:00 重置

应用内没有定时器。Linux crontab 示例（`%` 在 crontab 中必须转义）：

```cron
0 4 * * * cd /opt/agentsql && DEMO_ANCHOR_DATE=$(date -u +\%F) bash ./demo/reset.sh >> /var/log/agentsql-demo-reset.log 2>&1
```

Windows 管理员终端中的任务计划示例；脚本会在每次运行时取当天 UTC：

```powershell
schtasks /Create /SC DAILY /ST 04:00 /TN "AgentSQL Demo Reset" /TR "powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\agentsql\demo\reset.ps1" /F
```

## 公开部署安全清单

- 替换 SECRET、管理员口令、数据库 owner/root/只读口令和两个演示 key；不要复用环境样例。
- 不设置 `AGENTSQL_INSECURE`，也不把公开示例值带到公网。
- 删除 PostgreSQL/MySQL 的宿主 `ports`，或继续只绑定受控的回环/私网接口；绝不直接暴露业务库。
- 保持 `demo.enabled: true`、固定数据源白名单、低 QPS 和明显横幅；横幅不可关闭。
- 仅接入这两个合成演示库。不要修改 manifest 指向真实主机，也不要导入真实、个人或生产数据。
- 在反向代理层增加 TLS、访问控制和限流；确认 `/healthz` 不泄漏口令、DSN 或 `asql_` key。
- 数据库只读账号是最后防线；上线前实测其 INSERT、UPDATE、DELETE、TRUNCATE 和 DDL 均被拒绝。

## 故障排查

### 修改 SQL 后数据没有变化

官方数据库镜像只在空数据卷首次启动时执行 `/docker-entrypoint-initdb.d`。修改初始化文件后必须运行 reset，或明确执行：

```bash
docker compose -p agentsql-demo -f docker-compose.demo.yml --env-file demo/demo.env down -v --remove-orphans
```

这会删除全部演示数据，随后再 `up`。

### seed 没有完成或网关没有启动

查看 `demo-postgres`、`demo-mysql` 和一次性 `demo-seed` 的日志。anchor 缺失/非法、只读密码不符合 ASCII 约束、数据库不健康、manifest 与配置横幅不一致，都会 fail-closed；`agentsql-demo` 只在 seed 成功退出后启动。

### 端口占用

在 `demo/demo.env` 修改 `DEMO_PG_PORT`、`DEMO_MYSQL_PORT` 或 `DEMO_GATEWAY_PORT`。同时检查常见冲突端口 `5432`、`3306`、`7780`、`55432`、`55433`；默认演示网关使用 `17880`，避免与普通网关的 `7780` 冲突。

### 验证健康与只读权限

reset 会检查 `/healthz` 中 `demo.enabled=true`、`/readyz`、seed 固定分布和两库四张表的固定计数，任一步失败都会报告阶段并非零退出。Docker 可用的主控验收还应分别以 `agentsql_demo_ro` 登录两库，确认 SELECT 成功且所有写入和 DDL 被数据库拒绝。
