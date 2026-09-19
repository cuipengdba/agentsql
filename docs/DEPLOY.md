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

## 脱敏哈希密钥管理

`hash` 使用专用 HMAC 密钥生成不可逆哈希指纹；它不是加密，没有解密或还原原文的能力。该密钥与用于数据源密码、控制台/JWT 等用途的 `AGENTSQL_SECRET` 相互独立，不得复用或派生。密钥可来自 YAML `redaction.hash_key` 或环境变量 `AGENTSQL_REDACTION_HASH_KEY`；环境变量只要存在（包括空串）就覆盖 YAML。非空密钥按 UTF-8 字节计数，至少 32 字节，不会自动 trim。

`block` 不使用任何密钥、盐或算法参数，也没有新增配置项；它不参与 hash key 启动 fail-fast。无 key 时 `mask` 与 `block` 都可正常运行，只有 `hash` 需要上述专用密钥。

生产环境应从 secret manager、编排平台 Secret 或权限受控的环境文件注入，不要写进容器镜像、Dockerfile、Git 或命令行参数，也不要把真实值留在示例配置中。可生成一把随机密钥：

```bash
openssl rand -base64 32
```

Compose 已将 `AGENTSQL_REDACTION_HASH_KEY` 作为可选变量传入默认服务和 controlplane profile；未配置时传入空串，仅使用 `mask` / `block` 的部署仍可正常启动。由于空串也属于“环境变量存在”，Compose 部署若要启用 `hash`，应在受保护的 `.env` 或外部 Secret 中设置 `AGENTSQL_REDACTION_HASH_KEY`，不要只在 YAML 中填写 `redaction.hash_key`。本机二进制或 systemd 环境未声明该变量时，才会使用 YAML 值。

启动和管理面的能力矩阵如下：

| enabled 规则集合 | hash key 状态 | 启动与运行结果 |
| --- | --- | --- |
| 仅 `mask` / `block`（可混合） | 未配置或为空 | 正常启动并执行；`block` 不进入密钥门禁 |
| 仅 `mask` / `block`，另有 disabled `hash` 草稿 | 未配置或为空 | 正常启动；disabled hash 可保存和编辑，不参与执行 |
| 包含 enabled `hash`（可同时含 `mask` / `block`） | 未配置或为空 | 对外提供服务前启动失败（fail-fast），不会退化成返回原文、`***` 或整列 `[REDACTED]` |
| 任意合法 enabled 规则集合 | 至少 32 字节 | 正常启动并执行全部合法规则；密钥只供 `hash` 使用 |
| 任意规则集合 | 显式配置非空但不足 32 字节 | 配置装配立即失败，错误不回显密钥 |

服务运行期间，在无 key 的实例中新建默认启用的 `hash` 规则，或把现有 hash 规则改为 enabled，管理 API 返回 HTTP `503`、机器码 `HASH_REDACTION_UNAVAILABLE`，并在写库前拒绝，保证零写入。可以先保存为 disabled；配置密钥并重启服务后再启用。新建或启用 `block` 永远不会因为缺少 hash key 返回该错误。敏感类型与算法组合非法时返回 HTTP `422`、机器码 `INVALID_MASK_RULE`。

### 哈希指纹的安全边界

1. AgentSQL 使用带专用密钥的 HMAC-SHA256，而不是裸 SHA-256。手机号、身份证、邮箱和生日等低熵值可枚举；裸 SHA 容易被离线穷举或彩虹表反查，HMAC 只是把验证候选值的能力绑定到秘密 key。
2. HMAC 不是匿名化证明。攻击者拿到 key，或能够把自选候选值送入同一 hash oracle 时，仍可能字典化低熵输入；“不可逆”只表示没有解密函数，不等于绝对匿名，最小查询权限和密钥保护仍不可少。
3. 确定性指纹必然暴露相等关系与频率：同一 key 下的同值会得到同指纹，可供下游等值关联、去重和分组，也会暴露重复值、热点和分布。全局共享 key 还会形成跨数据源、跨表的关联追踪风险；互不应关联的环境或租户必须使用不同 key。
4. 更换 key 会改变全部指纹并断裂旧、新结果的等值关联。首版仅支持单密钥，没有 key version、双写、多版本验证或在线轮换/重算；只能在维护窗口统一切换。历史指纹不会自动重算，轮换前必须盘点 enabled `hash` 规则和依赖这些指纹的存量数据，制定下游回填、全量重算和回滚方案，并把旧 key 作为受控回滚材料保留到验收完成。
5. 跨库等值只对“到达 HMAC 的字符串字节完全相同”成立。日期、时区、decimal、首尾空白、大小写或 Unicode 表示不同都会产生不同指纹；系统不为等值关联做 trim 或格式正规化。
6. `hash` 在业务数据库把结果返回 AgentSQL 后、响应调用方之前计算，不会下推到业务数据库的 `JOIN`、`WHERE` 或 `GROUP BY`，也不改变数据库内部比较语义。它只能供拿到返回指纹的下游系统做等值关联。
7. 密钥不得进入响应、审计、日志、metrics、panic 或配置回显。YAML 仅适用于文件权限受控的部署；密钥也不写入 metadata/audit 数据库或脱敏规则。

升级到支持 `hash` 的版本前，应通过控制台、管理 API 或受控查询盘点 `mask_rules` 中 `algo=hash` 的存量，重点核对所有 enabled 规则。没有可用 key 时，必须先停用这些规则或在升级启动前安全注入原 key；否则新版本会按上述矩阵 fail-fast。disabled 草稿不会阻止启动。

## PostgreSQL 控制面部署

PostgreSQL 控制面兼容 PostgreSQL 15+，开发、Compose 与 CI 的基准版本为 PostgreSQL 18。SQLite 仍是默认零配置后端；本节使用两个独立数据库分别保存六张 metadata 表和不可变的 `audit_logs`。迁移期间必须停止 AgentSQL 写入。

### Compose 快速起停

复制 `.env` 示例并填写所有必填密码、原 `AGENTSQL_SECRET` 以及两个 DSN。默认只加载主文件 `docker-compose.yml`，因此不会读取或解析控制面变量，只会启动使用 SQLite 的 `agentsql`。PostgreSQL 控制面定义在 `docker-compose.controlplane.yml`，其中三个服务仍属于 `controlplane` profile；所有控制面操作都必须先加载主文件、再加载控制面文件，并保持两个 `-f` 的顺序不变。加载控制面文件后，保留的 `${VAR:?message}` 会在创建任何容器前对未设置或空的必填变量给出清晰错误。

首次切换必须分阶段执行，不能直接启动整个 profile：

```bash
# 1. 纯解析预检，不创建容器。
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane config --quiet

# 2. 切换前先停止可能占用 7780 的默认 SQLite 网关，然后只启动两个 PostgreSQL 18 数据库。
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane stop agentsql
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane up -d --wait metadata-db audit-db

# 3. 按下文完成迁移、创建运行账号并授权。

# 4. 把 .env 中两个 STORE DSN 换成运行账号后，只启动控制面服务。
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane up -d --build --wait agentsql-controlplane
```

`agentsql-controlplane` 会等待两个数据库健康后再启动。宿主端口仅监听 `127.0.0.1:7780`、`127.0.0.1:55432` 和 `127.0.0.1:55433`；若迁移 CLI 不在宿主机运行，可删除两个数据库的 `ports` 映射。不要执行没有服务名的整个 profile `up`，否则无 profile 的默认 `agentsql` 也会进入合并模型，并与 `agentsql-controlplane` 争用 7780。切换控制面前务必先停止默认 `agentsql`。

`ps`、`logs`、`stop`、`down` 等后续控制面管理命令也必须带上相同、同序的两个 `-f`，否则 Compose 可能把控制面容器视为 orphan，或根本看不到这些服务。例如：

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane ps
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane logs agentsql-controlplane
```

停止服务但保留命名卷：

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane stop agentsql-controlplane metadata-db audit-db
```

不要对仍需保留的数据执行 `docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane down -v`；合并模型中的 `-v` 会同时删除 `agentsql-data`、`agentsql-metadata-pgdata` 和 `agentsql-audit-pgdata`，有任何数据需要保留时都禁用该命令。

可运行 `sh scripts/compose-smoke.sh` 做纯 `config` 解析的 Compose 回归检查；该脚本不会启动容器。

为避免宿主机无法解析 Compose 网络内的 `metadata-db` / `audit-db`，可在控制面镜像内使用仓库实际提供的 `agentsqlctl` 子命令。源码构建首次执行前先构建镜像；配置校验、存储健康检查和空库初始化迁移分别使用：

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane build agentsql-controlplane
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane check-config --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane health --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane migrate --config /etc/agentsql/config.yaml
```

`migrate` 适用于初始化空控制面或升级 schema；从既有 SQLite 搬迁时按下一节使用 `migrate-sqlite-to-postgres`，不要把两种流程混为一谈。

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

3. 让 `AGENTSQL_STORE_METADATA_DSN` 和 `AGENTSQL_STORE_AUDIT_DSN` 暂时指向两个 migration owner，运行迁移。宿主机 CLI 不会自动读取 Compose 的 `.env`；调用者必须自行把原 `AGENTSQL_SECRET` 以及使用宿主地址的两个 DSN 导出到当前 shell。宿主机 CLI 连接 Compose 时使用 `127.0.0.1:55432` 和 `127.0.0.1:55433`；DSN 只通过环境变量注入，不要放在命令参数或提交到配置文件。Bash/zsh 使用 `export NAME=...`，PowerShell 使用 `$env:NAME = '...'`：

```bash
export AGENTSQL_SECRET='<与 SQLite 备份配对的原值>'
export AGENTSQL_STORE_METADATA_DSN='<指向 127.0.0.1:55432 的 migration-owner DSN>'
export AGENTSQL_STORE_AUDIT_DSN='<指向 127.0.0.1:55433 的 migration-owner DSN>'
agentsqlctl migrate-sqlite-to-postgres \
  --source /var/lib/agentsql/agentsql.db \
  --target-config examples/docker/config.controlplane.yaml \
  --verify-hash
```

迁移命令会创建并校验目标 schema、保留既有审计 ID、对齐 `audit_logs_id_seq`，并输出逐表校验摘要。目标库非空但内容不完全一致时会拒绝覆盖。

4. 创建独立的 metadata 与 audit 运行账号，按下一节施加最小权限。把两个 DSN 换成运行账号；Compose 容器内应连接 `metadata-db:5432` 和 `audit-db:5432`，外部 PostgreSQL 则使用其实际地址并启用适合生产环境的 TLS 校验。保持 `store.auto_migrate: false`，并继续使用原 `AGENTSQL_SECRET`。migration owner 在升级窗口之外应 `NOLOGIN`、轮换密码或撤销 CONNECT；下次升级临时恢复，升级完成后重新禁用，并针对新对象重施运行账号权限。

5. 切换前先检查配置和两个数据库连接，再启动：

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane check-config --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane health --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane up -d --build --wait agentsql-controlplane
curl --fail http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

6. 同时验收两个不等价的探针：`/healthz` 应返回 HTTP 200、`status: "ok"` 和 `version: "v0.2.0"`，仅表示进程存活；`/readyz` 应返回 HTTP 200 和 `status: "ready"`，表示配置的 metadata 与 audit 两个存储均已通过就绪检查。随后完成一次只读 allow、一次 deny 和一次 approve 流程。确认 metadata 变更只进入 metadata 库，新审计只进入 audit 库，新 `audit_logs.id` 大于迁移前最大 ID，且审计写入失败时请求按设计 fail-closed。验证期内保留原 SQLite 与 SECRET 备份。

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

### 预编译二进制系统要求

官方 linux/amd64 预编译二进制在 RHEL/Rocky/AlmaLinux 8（glibc 2.28）工具链中构建，适用于 x86_64 的 RHEL/Rocky/Alma/CentOS 8 系、Alibaba Cloud Linux 3、麒麟 V10、统信 UOS、Ubuntu 20.04、Debian 11 及更新版本。已在 Alibaba Cloud Linux 3（glibc 2.32）真机验证；CentOS 7（glibc 2.17）明确不支持。

预编译二进制不支持 musl（Alpine）或非 x86_64 架构。这些环境请使用基于 Debian bookworm、内含 glibc 的容器镜像，或在目标机运行 `make build` 本机编译。可用 `make docker-linux-amd64 VERSION=v0.2.0` 复现官方二进制；构建脚本会校验产物所需 GLIBC 符号不高于 2.28。

```bash
make build VERSION=v0.2.0
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

## 一键安装（推荐）

一键安装器只支持 Linux x86_64、glibc 2.28+。普通安装还要求 systemd 为 PID 1；Alpine/musl、CentOS 7、ARM64 和其他非 x86_64 主机不会进入下载、解压或系统变更阶段。ARM 主机即使运行 x86 容器，也需要宿主正确配置仿真。推荐命令：

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/latest/download/install.sh | sudo sh -s -- install
```

安装器会创建受限系统账号、回环监听配置、`0600` 环境文件和 systemd unit，并对 tarball 外层 sidecar、包内 `SHA256SUMS`、版本号与 ELF 架构逐层校验。自动生成的是持续有效的管理员密码，不是一次性口令。stdout 为 TTY 时首次创建凭据会显示一次密码；管道、CI 等非 TTY 默认不显示，可由 root 查看 `/etc/agentsql/agentsql.env`，或明确传入 `--show-password`。

常用生命周期命令：

```bash
sudo ./install.sh install --version v0.2.0
sudo ./install.sh upgrade --version v0.2.1
sudo ./install.sh uninstall
sudo ./install.sh uninstall --purge --yes
```

默认卸载只删除两个二进制和 unit，保留 `/etc/agentsql`、`/var/lib/agentsql` 及系统账号。升级快照保存在 root 专用的 `/var/backups/agentsql`，即使 `--purge` 也会保留，确认不再需要后应由管理员单独归档或清理。`--purge` 只删除前述配置和数据两个固定目录，不跟随配置中的外部数据库、证书或其他路径；非交互环境必须同时传 `--yes`。只有账号确由安装器创建且已无其他文件归属时，额外的 `--remove-user` 才会删除它。失败升级留下的快照中，`agentsql.db` 等文件保留了 agentsql 属主（用于原样回滚），因此 `uninstall --purge --remove-user` 会据此拒绝删账号并点名残留文件；确认快照不再需要后，先执行 `sudo chown -R root:root /var/backups/agentsql`（或归档后删除该目录）再重跑卸载命令，即可连同系统账号一并删除。全新安装、从未发生过失败升级的主机不存在该快照，`uninstall --purge --yes --remove-user` 可一次清空二进制、unit、配置、数据和账号。

离线安装可把已解压包目录传给 `--from`，或传入 tarball；传 tarball 时同目录必须有 `<tarball>.sha256`。从发布包根执行 `sudo ./install.sh install` 会自动使用当前已校验的包，不访问网络：

```bash
sudo ./install.sh install --from /srv/releases/agentsql-v0.2.0-linux-amd64
sudo ./install.sh upgrade --from /srv/releases/agentsql-v0.2.1-linux-amd64.tar.gz
```

`--no-start` 只用于 chroot 或镜像预安装：允许在 systemd 不是 PID 1 时写入文件，但不会启动或健康检查，安装结果在 systemd 成功启动前不可用。升级默认只自动备份 `/var/lib/agentsql/agentsql.db` 及其 `-wal`、`-shm`；使用 PostgreSQL 控制面或自定义 SQLite 路径时，必须先完成外部一致性备份并明确传 `--external-backup-done`，安装器不会声称已备份外部数据库。若外部数据库升级后的健康检查失败，安装器会恢复二进制、unit、配置和环境文件，但会让旧服务保持停止；操作者必须先恢复外部数据库快照，再启动旧版本。

下载默认只使用 GitHub Release，不会在校验失败时切换镜像。受控网络可显式设置 `AGENTSQL_DOWNLOAD_BASE` 为 HTTPS 资产根；该根需按 `<base>/<version>/agentsql-<version>-linux-amd64.tar.gz[.sha256]` 提供同源文件。建议同时明确 `--version`，例如：

```bash
sudo env AGENTSQL_DOWNLOAD_BASE=https://agentsql.cn/releases sh ./install.sh install --version v0.2.0
```

安装器若发现 `restorecon` 会恢复二进制、unit、配置和数据目录的 SELinux 默认上下文，不调用 `chcon`、不关闭 enforcing；遇到拒绝时用 `ausearch -m AVC` 调查。发布 tarball 的许可证位于包根 `LICENSE`，容器镜像内位于 `/usr/share/licenses/agentsql/LICENSE`。

## 方式三：systemd

先构建二进制，再安装服务文件和目录：

```bash
sudo useradd --system --user-group --home-dir /var/lib/agentsql --shell /usr/sbin/nologin agentsql
sudo install -d -o root -g agentsql -m 0750 /etc/agentsql
sudo install -d -o agentsql -g agentsql -m 0750 /var/lib/agentsql
sudo install -m 0755 bin/agentsql bin/agentsqlctl /usr/local/bin/
sudo install -m 0644 deploy/systemd/agentsql.service /etc/systemd/system/agentsql.service
sudo install -o root -g agentsql -m 0640 deploy/systemd/config.yaml /etc/agentsql/config.yaml
```

如果系统已经存在 `agentsql` 用户，跳过 `useradd`。systemd 专用配置模板已固定仅监听 `127.0.0.1:7780`，并将 SQLite 数据写入 `/var/lib/agentsql/agentsql.db`，无需再手工修改 `sqlite_path`。裸机默认只监听 `127.0.0.1`；如需远程访问，应使用 SSH 本地转发或受控反向代理（TLS/鉴权），不要直接把 `http_listen` 改成 `0.0.0.0` 暴露公网。创建仅 root 可读的环境文件：

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

### 裸机使用 PostgreSQL 控制面

SQLite 是默认的零配置起步方式。若要把 metadata 与 audit 放到 PostgreSQL 独立审计库，先按本文档前面的「PostgreSQL 控制面部署」准备数据库、一次性 migration owner 和最小权限运行账号。在 `/etc/agentsql/agentsql.env` 中追加 `AGENTSQL_STORE_METADATA_DSN`、`AGENTSQL_STORE_AUDIT_DSN`；DSN 只通过环境变量提供，不要写入配置文件。再把 `/etc/agentsql/config.yaml` 的 `store` 改为等价的 `metadata` / `audit` 形式，可参考 `examples/docker/config.postgres.yaml` 与 `examples/docker/config.controlplane.yaml`，但裸机的 `server.http_listen` 必须保持为 `127.0.0.1:7780`。

首次启动前，在已安全加载上述环境变量的 root 或等价受控环境中运行：

```bash
agentsqlctl migrate --config /etc/agentsql/config.yaml
```

systemd 随后以 `agentsql` 用户运行服务；迁移账号与最小权限运行账号的权限边界、迁移完成后的账号处置方式均与前文一致。

## 探活

`/healthz` 是免鉴权存活检查；`/readyz` 会在 1 秒内检查配置的 metadata 与 audit 存储是否可用。

```bash
agentsqlctl health --url http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

只有 HTTP 200 且 JSON `status` 为 `ok` 时，`agentsqlctl health` 才返回成功。

## 升级与回滚

升级前先执行完整备份，盘点 enabled `hash` 规则并确认对应 `AGENTSQL_REDACTION_HASH_KEY` 已安全注入，然后停止当前实例、替换两个二进制或容器镜像并重新启动。AgentSQL 启动时会自动、幂等地执行尚未应用的 SQLite migration；enabled `hash` 无 key 会在对外服务前拒绝启动。

回滚不是只替换二进制：停止服务，将二进制或镜像恢复到旧版本，同时恢复升级前的 SQLite 备份，再启动服务。不要让旧版本直接读取已被新版本迁移且不兼容的数据库。

## 发布与分发

发布者应按以下顺序构建、验证并分发同一版本。正式发布建议把 `ROCKY_IMAGE` 固定到审核过的 `rockylinux:8@sha256:...`，默认值仍为 `rockylinux:8`：

```bash
make docker-linux-amd64 VERSION=v0.2.0 ROCKY_IMAGE=rockylinux:8@sha256:<reviewed-digest>
make package-release VERSION=v0.2.0
sh scripts/push-release-image.sh v0.2.0
```

发布镜像脚本要求操作者先执行 `docker login ghcr.io`，默认只推精确版本 tag；只有显式加 `--latest` 才会移动 latest。之后必须按顺序完成：

1. 在 GHCR 将 `ghcr.io/cuipengdba/agentsql` package 设为 public。
2. 在没有 GHCR 登录状态的干净环境执行 `docker pull ghcr.io/cuipengdba/agentsql:v0.2.0`，核对架构为 amd64，并以回环端口启动、等待 health 为 healthy。
3. 把 `dist/agentsql-v0.2.0-linux-amd64.tar.gz`、同名 `.sha256` 和固定名 `dist/install.sh` 上传到同一个 GitHub Release。
4. 再次下载 Release 资产，校验外层 SHA-256、包内 `SHA256SUMS` 和两个二进制版本，全部通过后发布 Release。

`scripts/package-release.sh` 不读取或覆盖既有 `bin/SHA256SUMS`；`SOURCE_DATE_EPOCH` 可覆盖确定性 tar 的固定时间。GitHub 的 latest 不包含 prerelease，一键安装器也只接受严格的 `vX.Y.Z`。

| 环境 | 原生发布包 | Docker 快速启动 |
| --- | --- | --- |
| x86_64 + glibc 2.28+ + systemd PID 1 | 支持 | 支持 |
| x86_64 + glibc 2.28+，无 systemd PID 1 | 仅 `--no-start` 预安装 | 支持 |
| CentOS 7 / glibc 2.17 | 不支持 | 使用 Debian/glibc 镜像 |
| Alpine / musl | 不支持 | 使用 Debian/glibc 镜像 |
| ARM64 或其他非 x86_64 | 无原生包 | 本快速脚本不支持；自行配置 x86 仿真 |

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

项目的 `pg_query_go` 解析器依赖 cgo。纯 Go 构建会缺少必要符号；Alpine 使用 musl，也不符合当前 glibc 构建与运行约束。官方容器镜像基于 Debian bookworm；官方 linux/amd64 预编译二进制则使用 Rocky Linux 8（glibc 2.28）工具链构建。

### 7780 端口被占用

停止占用进程，或同时调整配置中的 `server.http_listen` 与 Compose 的宿主机端口映射。容器内部端口仍应与配置保持一致。

### 服务提示 AGENTSQL_SECRET 非法

`AGENTSQL_SECRET` 必须恰好 32 字节，不是 32 个任意 Unicode 字符。建议使用 32 个 ASCII 字节，并避免 shell 换行或额外引号进入值本身。

### 已有数据源突然无法连接

确认服务使用的 `AGENTSQL_SECRET` 与创建这些数据源时完全一致。恢复 SQLite 备份时必须同步恢复对应密钥。
