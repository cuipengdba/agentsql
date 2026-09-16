# AgentSQL 部署指南

AgentSQL 是面向 AI Agent 的数据库安全网关和生产级 MCP Server。元数据与审计记录默认保存在本地 SQLite，也可使用 PostgreSQL 控制面；业务数据库仍使用各自的 PostgreSQL 或 MySQL 连接。

## 目录约定

| 路径 | 用途 |
| --- | --- |
| `/usr/local/bin/agentsql` | 网关服务二进制 |
| `/usr/local/bin/agentsqlctl` | 运维 CLI |
| `/etc/agentsql/config.yaml` | 服务配置 |
| `/etc/agentsql/agentsql.env` | 密钥与管理员凭证，权限应为 `0600` |
| `/var/lib/agentsql/agentsql.db` | SQLite 元数据与审计库 |

容器部署使用同样的 `/etc/agentsql` 与 `/var/lib/agentsql` 路径。Compose 使用命名卷 `agentsql-data` 持久化容器数据，避免 Linux 宿主 bind mount 生成 root 权限文件。

## 方式一：Docker Compose（推荐）

```bash
cp examples/docker/.env.example .env
```

编辑 `.env`，替换 `AGENTSQL_SECRET` 和 `AGENTSQL_ADMIN_PASSWORD`。`AGENTSQL_SECRET` 必须恰好 32 字节，且后续不得随意变更。

生成你自己的随机值；不要复制文档中的值，也不要把生成结果提交到仓库：

```bash
openssl rand -base64 24                    # AGENTSQL_SECRET：输出恰好 32 个 ASCII 字节
openssl rand -base64 24                    # 可作为强管理员口令
```

```powershell
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); [Convert]::ToBase64String($bytes); $rng.Dispose()
```

PowerShell 命令使用密码学 RNG 生成 24 个随机字节，再编码为恰好 32 个 ASCII 字节。分别运行两次，填写 SECRET 与管理员口令。

```bash
docker compose up -d --build
docker compose ps
```

浏览器打开 <http://127.0.0.1:7780>，使用 `.env` 中的管理员账号登录。默认不会启动演示数据库；需要 PostgreSQL 16 和 MySQL 8 时使用：

```bash
docker compose --profile demo up -d --build
```

控制台实时事件流默认通过 `server.event_stream: true` 启用，并允许最多
`server.event_stream_max_connections: 100` 条并发 SSE 连接（有效范围 1–1000）。
反向代理该路径时应关闭缓存、响应压缩和 buffering，并把空闲超时设为大于 25 秒。

需要同时启动 AgentSQL、Prometheus 与自动 provisioning 的 Grafana 六面板时使用：

```bash
AGENTSQL_SECRET='N7vK2mQ9xR4tY8pL6cW3sD5fH1jB0zUa' \
AGENTSQL_ADMIN_PASSWORD='S9afe-Admin-Passphrase-2026' \
docker compose --profile observability up -d --build
```

以上值仅是满足校验规则的本地示例，实际部署请按前文生成并妥善保存随机值。Prometheus 与 Grafana 和 AgentSQL 一样仅绑定宿主回环地址。命名卷可用 `docker compose down -v` 显式删除；该命令会永久清除 SQLite 元数据与审计记录。

## PostgreSQL 控制面部署

PostgreSQL 控制面兼容 PostgreSQL 15+，开发、Compose 与 CI 的基准版本为 PostgreSQL 18。SQLite 仍是默认零配置后端；本节使用两个独立数据库分别保存六张 metadata 表和不可变的 `audit_logs`。迁移期间必须停止 AgentSQL 写入。

### Compose 快速起停

复制 `.env` 示例并填写所有必填密码、原 `AGENTSQL_SECRET` 以及两个 DSN。`${VAR:?message}` 表示变量未设置或为空时 Compose 会直接报错。三个控制面服务都属于 `controlplane` profile；默认 `docker compose up -d` 仍只启动使用 SQLite 的 `agentsql`。

首次切换必须分阶段执行，不能直接启动整个 profile：

```bash
# 1. 只启动两个 PostgreSQL 18 数据库。
docker compose --profile controlplane up -d metadata-db audit-db

# 2. 按下文完成迁移、创建运行账号并授权。

# 3. 把 .env 中两个 STORE DSN 换成运行账号后，只启动控制面服务。
docker compose --profile controlplane up -d --build agentsql-controlplane
```

`agentsql-controlplane` 会等待两个数据库健康后再启动。宿主端口仅监听 `127.0.0.1:7780`、`127.0.0.1:55432` 和 `127.0.0.1:55433`；若迁移 CLI 不在宿主机运行，可删除两个数据库的 `ports` 映射。不要无服务名执行整个 profile，否则无 profile 的默认 `agentsql` 也会启动并与控制面实例争用 7780。

停止服务但保留命名卷：

```bash
docker compose --profile controlplane stop agentsql-controlplane metadata-db audit-db
```

不要对仍需保留的数据执行 `docker compose down -v`；`-v` 会删除 SQLite 和两个 PostgreSQL 命名卷。

### 从 SQLite 切换

1. 创建 `agentsql_metadata`、`agentsql_audit` 两个数据库，以及各自的一次性 migration owner。migration owner 应是对应数据库和 `public` schema 的 owner，或具备迁移所需的 CONNECT、USAGE、CREATE/DDL、表 SELECT/INSERT 与序列 USAGE/SELECT/UPDATE 权限。Compose 首次初始化时，`POSTGRES_DB` 和 `POSTGRES_USER` 对应的迁移账号会创建数据库与 schema；外部 PostgreSQL 可由集群管理员执行下列等价初始化，`\password` 会交互读取密码而不把它写入命令历史：

```sql
CREATE ROLE agentsql_meta_migrator LOGIN;
\password agentsql_meta_migrator
CREATE DATABASE agentsql_metadata OWNER agentsql_meta_migrator;
\connect agentsql_metadata
ALTER SCHEMA public OWNER TO agentsql_meta_migrator;

\connect postgres
CREATE ROLE agentsql_audit_migrator LOGIN;
\password agentsql_audit_migrator
CREATE DATABASE agentsql_audit OWNER agentsql_audit_migrator;
\connect agentsql_audit
ALTER SCHEMA public OWNER TO agentsql_audit_migrator;
```

2. 停止旧 AgentSQL，确保 SQLite 不再写入。备份 `agentsql.db`，并在同一受保护的备份集中保存与它配对的 `AGENTSQL_SECRET`。丢失原 SECRET 会导致迁移后的 `password_enc` 无法解密。

3. 让 `AGENTSQL_STORE_METADATA_DSN` 和 `AGENTSQL_STORE_AUDIT_DSN` 暂时指向两个 migration owner，运行迁移。宿主机 CLI 连接 Compose 时使用 `127.0.0.1:55432` 和 `127.0.0.1:55433`；DSN 只通过环境变量注入，不要放在命令参数或提交到配置文件：

```bash
agentsqlctl migrate-sqlite-to-postgres \
  --source /var/lib/agentsql/agentsql.db \
  --target-config examples/docker/config.controlplane.yaml \
  --verify-hash
```

迁移命令会创建并校验目标 schema、保留既有审计 ID、对齐 `audit_logs_id_seq`，并输出逐表校验摘要。目标库非空但内容不完全一致时会拒绝覆盖。

4. 创建独立的 metadata 与 audit 运行账号，按下一节施加最小权限。把两个 DSN 换成运行账号；Compose 容器内应连接 `metadata-db:5432` 和 `audit-db:5432`，外部 PostgreSQL 则使用其实际地址并启用适合生产环境的 TLS 校验。保持 `store.auto_migrate: false`，并继续使用原 `AGENTSQL_SECRET`。migration owner 在升级窗口之外应 `NOLOGIN`、轮换密码或撤销 CONNECT；下次升级临时恢复，升级完成后重新禁用，并针对新对象重施运行账号权限。

5. 切换前先检查配置和两个数据库连接，再启动：

```bash
agentsqlctl check-config --config examples/docker/config.controlplane.yaml
agentsqlctl health --config examples/docker/config.controlplane.yaml
docker compose --profile controlplane up -d --build agentsql-controlplane
agentsqlctl health --url http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

6. 验收 `/readyz` 返回 HTTP 200，并完成一次只读 allow、一次 deny 和一次 approve 流程。确认 metadata 变更只进入 metadata 库，新审计只进入 audit 库，新 `audit_logs.id` 大于迁移前最大 ID，且审计写入失败时请求按设计 fail-closed。验证期内保留原 SQLite 与 SECRET 备份。

### 运行账号最小权限

以下示例假定迁移已完成。先由 DBA 创建 `agentsql_meta_runtime` 与 `agentsql_audit_runtime` 两个仅登录角色并交互设置独立强密码：

```sql
CREATE ROLE agentsql_meta_runtime LOGIN;
\password agentsql_meta_runtime
CREATE ROLE agentsql_audit_runtime LOGIN;
\password agentsql_audit_runtime
```

再分别以对应数据库 owner 或具备授权权限的管理员执行后续语句；不要把示例角色名替换成 migration owner。

metadata 数据库：

```sql
REVOKE ALL ON DATABASE agentsql_metadata FROM PUBLIC;
GRANT CONNECT ON DATABASE agentsql_metadata TO agentsql_meta_runtime;

REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;

GRANT USAGE ON SCHEMA public TO agentsql_meta_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE
ON TABLE public.agents,
         public.datasources,
         public.policies,
         public.rules,
         public.mask_rules,
         public.approvals
TO agentsql_meta_runtime;
GRANT SELECT ON TABLE public.schema_migrations TO agentsql_meta_runtime;
```

audit 数据库：

```sql
REVOKE ALL ON DATABASE agentsql_audit FROM PUBLIC;
GRANT CONNECT ON DATABASE agentsql_audit TO agentsql_audit_runtime;

REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;

GRANT USAGE ON SCHEMA public TO agentsql_audit_runtime;
GRANT SELECT, INSERT ON TABLE public.audit_logs TO agentsql_audit_runtime;
GRANT USAGE, SELECT ON SEQUENCE public.audit_logs_id_seq TO agentsql_audit_runtime;
GRANT SELECT ON TABLE public.schema_migrations TO agentsql_audit_runtime;
```

运行账号不得获得 CREATE、ALTER、DROP、REFERENCES 或 TRIGGER 权限；audit 运行账号不得获得 `audit_logs` 的 UPDATE、DELETE、TRUNCATE 权限，也不得获得序列 UPDATE 权限。migration owner 具备 DDL 能力，只在迁移和升级窗口使用；完成后应禁用登录、轮换密码或撤销 CONNECT。

这些权限只限制 AgentSQL 运行账号。它们不能防止数据库 owner、DBA 或 superuser 篡改，也不提供法规级 WORM、保留锁或不可删除介质。独立 metadata/audit 数据库之间没有分布式事务。

### PostgreSQL 备份与恢复

停止 AgentSQL 写入后，分别生成两个自定义格式备份，并同时备份当前 `AGENTSQL_SECRET`：

```bash
pg_dump -Fc --dbname "$AGENTSQL_STORE_METADATA_DSN" --file agentsql-metadata.dump
pg_dump -Fc --dbname "$AGENTSQL_STORE_AUDIT_DSN" --file agentsql-audit.dump
```

这是两次独立 dump，**不构成跨库一致快照**。如果业务要求严格的跨库恢复点，需要由 PostgreSQL 平台提供额外的协调备份能力；AgentSQL 不宣称双库一致性快照。

恢复时先创建两个空数据库和对应 schema，以 migration owner 分别恢复：

```bash
pg_restore --no-owner --no-privileges --dbname "$AGENTSQL_STORE_METADATA_DSN" agentsql-metadata.dump
pg_restore --no-owner --no-privileges --dbname "$AGENTSQL_STORE_AUDIT_DSN" agentsql-audit.dump
```

恢复后重新执行最小权限 GRANT，校验两库 `schema_migrations` 版本、各表行数、`audit_logs` 的最小/最大 ID、`audit_logs_id_seq` 的 `last_value/is_called`、以及所有非空 `approvals.audit_id` 是否能在 audit 库找到对应 ID。最后换回运行账号 DSN，以 `auto_migrate:false` 启动并检查 `/readyz` 与审计写入。

### 回滚到 SQLite

停止控制面服务，恢复原 SQLite 配置，并将成对备份的 `agentsql.db` 与原 `AGENTSQL_SECRET` 一起恢复，再启动默认 `agentsql`。不要把 PostgreSQL 切换后的新写入视为已自动回灌 SQLite；双库和 SQLite 之间没有双写或自动反向迁移。回滚后保留 PostgreSQL 现场，直到核对完成。

## 方式二：本机二进制

构建依赖 Go 1.25、C 编译器与 glibc 兼容环境。`pg_query_go` 必须启用 cgo。

```bash
make build VERSION=v0.1.0
./bin/agentsqlctl init-config -o config.yaml
export AGENTSQL_SECRET="$(openssl rand -base64 24)"
export AGENTSQL_ADMIN_USER='admin'
export AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 24)"
./bin/agentsql serve -c config.yaml
```

上述命令会为当前进程生成你自己的随机值；请通过密码管理器或受保护的环境文件持久保存。仓库已经包含嵌入式控制台产物；只有前端源码变化时才需先运行 `make webui`。

PowerShell 本机启动示例：

```powershell
./bin/agentsqlctl.exe init-config -o config.yaml
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); $env:AGENTSQL_SECRET = [Convert]::ToBase64String($bytes); $rng.Dispose()
$env:AGENTSQL_ADMIN_USER = 'admin'
$passwordBytes = [byte[]]::new(24); $passwordRng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $passwordRng.GetBytes($passwordBytes); $env:AGENTSQL_ADMIN_PASSWORD = [Convert]::ToBase64String($passwordBytes); $passwordRng.Dispose()
./bin/agentsql.exe serve -c config.yaml
```

## 方式三：systemd

先构建二进制，再安装服务文件和目录：

```bash
sudo useradd --system --user-group --home-dir /var/lib/agentsql --shell /usr/sbin/nologin agentsql
sudo install -d -o agentsql -g agentsql -m 0750 /etc/agentsql /var/lib/agentsql
sudo install -m 0755 bin/agentsql bin/agentsqlctl /usr/local/bin/
sudo install -m 0644 deploy/systemd/agentsql.service /etc/systemd/system/agentsql.service
sudo install -o root -g agentsql -m 0640 examples/docker/config.yaml /etc/agentsql/config.yaml
```

如果系统已经存在 `agentsql` 用户，跳过 `useradd`。将 `/etc/agentsql/config.yaml` 的 `sqlite_path` 保持为 `/var/lib/agentsql/agentsql.db`。创建仅 root 可读的环境文件：

```bash
sudo sh -c 'cat > /etc/agentsql/agentsql.env <<EOF
AGENTSQL_SECRET=<GENERATE_YOUR_OWN_32_BYTE_SECRET>
AGENTSQL_ADMIN_USER=admin
AGENTSQL_ADMIN_PASSWORD=<GENERATE_YOUR_OWN_STRONG_PASSPHRASE>
EOF'
sudo chmod 0600 /etc/agentsql/agentsql.env
sudo systemctl daemon-reload
sudo systemctl enable --now agentsql
sudo systemctl status agentsql
```

先用本节前面的随机生成命令替换两个占位符，再启动服务。占位符本身不能用于启动。

## 探活

`/healthz` 是免鉴权存活检查；`/readyz` 会在 1 秒内检查配置的 metadata 与 audit 存储是否可用。

```bash
agentsqlctl health --url http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

只有 HTTP 200 且 JSON `status` 为 `ok` 时，`agentsqlctl health` 才返回成功。

## 升级与回滚

升级前先执行完整备份，然后停止当前实例、替换两个二进制或容器镜像并重新启动。AgentSQL 启动时会自动、幂等地执行尚未应用的 SQLite migration。

回滚不是只替换二进制：停止服务，将二进制或镜像恢复到旧版本，同时恢复升级前的 SQLite 备份，再启动服务。不要让旧版本直接读取已被新版本迁移且不兼容的数据库。

## 备份铁律

必须成对备份：

1. `agentsql.db` SQLite 文件。
2. 与该数据库对应的 `AGENTSQL_SECRET`。

二者缺一不可。密钥丢失后，SQLite 中已经加密的数据源密码无法解密。复制 SQLite 文件前应停止写入或使用 SQLite 一致性备份机制；不要只复制正在写入的主文件。

更换 `AGENTSQL_SECRET` 会使既有数据源密码无法解密，这不是无损密钥轮换。v0.1 没有在线重加密流程；需要更换时，必须先规划数据源凭据重新录入与可回滚的 SQLite 备份。

## 本地测试模式

只有本地开发或测试需要兼容仓库历史公开测试凭据时，才可设置 `AGENTSQL_INSECURE=1`。它只放行“长度正确的公开测试 SECRET”和“非空弱管理员口令”；SECRET 缺失或不是 32 字节、管理员口令为空仍会拒绝启动。该开关不会、也不得关闭认证、安全规则或 fail-closed 行为，生产环境禁止设置。

## 常见问题

### 容器映射端口后仍无法访问

容器内监听地址必须是 `0.0.0.0:7780`。`127.0.0.1` 只绑定容器自身回环接口，宿主机端口映射无法访问它。使用 `examples/docker/config.yaml`。

### 为什么不能设置 `CGO_ENABLED=0` 或使用 Alpine

项目的 `pg_query_go` 解析器依赖 cgo。纯 Go 构建会缺少必要符号；Alpine 使用 musl，也不符合当前 glibc 构建与运行约束。构建和运行镜像必须使用 Debian bookworm 系列。

### 7780 端口被占用

停止占用进程，或同时调整配置中的 `server.http_listen` 与 Compose 的宿主机端口映射。容器内部端口仍应与配置保持一致。

### 服务提示 AGENTSQL_SECRET 非法

`AGENTSQL_SECRET` 必须恰好 32 字节，不是 32 个任意 Unicode 字符。建议使用 32 个 ASCII 字节，并避免 shell 换行或额外引号进入值本身。

### 已有数据源突然无法连接

确认服务使用的 `AGENTSQL_SECRET` 与创建这些数据源时完全一致。恢复 SQLite 备份时必须同步恢复对应密钥。
