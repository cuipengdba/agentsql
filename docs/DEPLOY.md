# AgentSQL 部署指南

AgentSQL 是面向 AI Agent 的数据库安全网关和生产级 MCP Server。元数据与审计记录默认保存在本地 SQLite，也可使用 PostgreSQL 控制面；业务数据库仍使用各自的 PostgreSQL 或 MySQL 连接。

当前精确安装版本、下载链接和升级前检查见 [v1.6.0 快速上手](GETTING_STARTED.md)与 [v1.6.0 发布说明](release-notes-v1.6.0.md)。本文后部标有 v1.2.0 或其他旧版本号的发布命令与分阶段上线记录是历史示例，不应直接用于 v1.6.0；执行前须使用正式 Release 的同版资产、签名及镜像摘要。

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

`hash` 使用专用 HMAC 密钥生成不可逆哈希指纹；它不是加密，没有解密或还原原文的能力。该密钥与用于数据源密码、控制台/JWT 等用途的 `AGENTSQL_SECRET` 相互独立，不得复用或派生。兼容单 key 可来自 YAML `redaction.hash_key` 或环境变量 `AGENTSQL_REDACTION_HASH_KEY`；v0.4 多版本模式使用 inline `redaction.hash_keys` 或绝对路径 `AGENTSQL_REDACTION_HASH_KEYS_FILE`。这些来源互斥。每份非空材料按原始字节计数，至少 32 字节，多版本材料还须通过多样性与重复检查。

`block` 与 `range` 不使用任何密钥、盐或 HMAC 参数，也不参与 hash key 启动 fail-fast。无 key 时 `mask`、`block` 与 `range` 都可正常运行，只有 `hash` 需要上述专用密钥。

生产环境应从 secret manager、编排平台 Secret 或权限受控的环境文件注入，不要写进容器镜像、Dockerfile、Git 或命令行参数，也不要把真实值留在示例配置中。可生成一把随机密钥：

```bash
openssl rand -base64 32
```

Compose 已将 `AGENTSQL_REDACTION_HASH_KEY` 作为可选变量传入默认服务和 controlplane profile；未配置时传入空串，仅使用 `mask` / `block` / `range` 的部署仍可正常启动。由于空串也属于“环境变量存在”，Compose 部署若要启用 `hash`，应在受保护的 `.env` 或外部 Secret 中设置 `AGENTSQL_REDACTION_HASH_KEY`，不要只在 YAML 中填写 `redaction.hash_key`。本机二进制或 systemd 环境未声明该变量时，才会使用 YAML 值。

启动和管理面的能力矩阵如下：

| enabled 规则集合 | hash key 状态 | 启动与运行结果 |
| --- | --- | --- |
| 仅 `mask` / `block` / `range`（可混合） | 未配置或为空 | 正常启动并执行；`block` 与 `range` 不进入密钥门禁 |
| 仅 `mask` / `block` / `range`，另有 disabled `hash` 草稿 | 未配置或为空 | 正常启动；disabled hash 可保存和编辑，不参与执行 |
| 包含 enabled `hash`（可同时含 `mask` / `block` / `range`） | 未配置或为空 | 对外提供服务前启动失败（fail-fast），不会退化成返回原文、`***` 或整列 `[REDACTED]` |
| 任意合法 enabled 规则集合 | 至少 32 字节 | 正常启动并执行全部合法规则；密钥只供 `hash` 使用 |
| 任意规则集合 | 显式配置非空但不足 32 字节 | 配置装配立即失败，错误不回显密钥 |

服务运行期间，在无 key 的实例中新建默认启用的 `hash` 规则，或把现有 hash 规则改为 enabled，管理 API 返回 HTTP `503`、机器码 `HASH_REDACTION_UNAVAILABLE`，并在写库前拒绝，保证零写入。可以先保存为 disabled；配置密钥并重启服务后再启用。新建或启用 `block` 永远不会因为缺少 hash key 返回该错误。敏感类型与算法组合非法时返回 HTTP `422`、机器码 `INVALID_MASK_RULE`。

### 哈希指纹的安全边界

1. AgentSQL 使用带专用密钥的 HMAC-SHA256，而不是裸 SHA-256。手机号、身份证、邮箱和生日等低熵值可枚举；裸 SHA 容易被离线穷举或彩虹表反查，HMAC 只是把验证候选值的能力绑定到秘密 key。
2. HMAC 不是匿名化证明。攻击者拿到 key，或能够把自选候选值送入同一 hash oracle 时，仍可能字典化低熵输入；“不可逆”只表示没有解密函数，不等于绝对匿名，最小查询权限和密钥保护仍不可少。
3. 确定性指纹必然暴露相等关系与频率：同一 key 下的同值会得到同指纹，可供下游等值关联、去重和分组，也会暴露重复值、热点和分布。全局共享 key 还会形成跨数据源、跨表的关联追踪风险；互不应关联的环境或租户必须使用不同 key。
4. 更换 key 会改变指纹并断裂旧、新结果的等值关联。v0.4 支持版本化历史核验和重启式计划切换，但没有热加载、双写、在线轮换或自动重算；必须在维护窗口停写、drain、CAS active、统一修改 manifest、完全重启、强对账并确认 `/readyz` 200。轮换前必须盘点 enabled `hash` 规则和依赖指纹的存量数据，制定下游回填/双版本过渡和回滚方案，并把旧 key 作为 legacy 回滚材料保留到验收完成。
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

6. 同时验收两个不等价的探针：`/healthz` 应返回 HTTP 200、`status: "ok"` 和待部署版本号，仅表示进程存活；`/readyz` 应返回 HTTP 200 和 `status: "ready"`，表示配置的 metadata 与 audit 两个存储均已通过就绪检查。随后完成一次只读 allow、一次 deny 和一次 approve 流程。确认 metadata 变更只进入 metadata 库，新审计只进入 audit 库，新 `audit_logs.id` 大于迁移前最大 ID，且审计写入失败时请求按设计 fail-closed。验证期内保留原 SQLite 与 SECRET 备份。

分库模式下，管理审计和流量审计都写入 audit 库的 `traffic` 链；metadata 库只保留待投递的 `management_audit_outbox`。Admin API 查询 `domain=management` 时会映射到这条 `traffic` 链，因此验收管理操作时应在 audit 库核对持久审计记录。

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

从源码构建依赖 Go 1.26、C 编译器与 glibc 兼容环境；v1.2.0 发布脚本固定使用 Go 1.26.9。`pg_query_go` 必须启用 cgo。

### 预编译二进制系统要求

官方原生包在对应架构的 Rocky Linux 8（glibc 2.28）工具链中构建。`linux/amd64` 对应 `uname -m` 的 `x86_64` / `amd64`；v0.4.0 起提供 `linux/arm64` 原生 glibc 包，对应 `aarch64` / `arm64`。amd64 包适用于 x86_64 的 RHEL/Rocky/Alma/CentOS 8 系、Alibaba Cloud Linux 3、麒麟 V10、统信 UOS、Ubuntu 20.04、Debian 11 及更新版本，并已在 Alibaba Cloud Linux 3（glibc 2.32）真机验证；CentOS 7（glibc 2.17）明确不支持。

预编译二进制不支持 musl（Alpine）或 amd64 / arm64 之外的架构。这些环境请使用基于 Debian bookworm、内含 glibc 的 amd64 / arm64 多架构容器镜像，或在目标机运行 `make build` 本机编译。v1.2.0 源码可用 `make release-linux-amd64 VERSION=v1.2.0` 或 `make release-linux-arm64 VERSION=v1.2.0` 在同架构容器内构建、测试并打包；构建脚本会校验产物所需 GLIBC 符号不高于 2.28。正式发布时 `ROCKY_IMAGE` 必须固定为审核过的 `@sha256` digest。

```bash
make build VERSION=v1.2.0
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

一键安装器支持 Linux amd64（`uname -m` 为 `x86_64` / `amd64`）与 arm64（`aarch64` / `arm64`）原生 glibc 包；v0.4.0 起提供 linux/arm64 原生 glibc 包。两者均要求 glibc 2.28+，普通安装还要求 systemd 为 PID 1；Alpine / musl、CentOS 7 和其他架构不会进入下载、解压或系统变更阶段。推荐命令：

执行前先核对 [v1.2.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.2.0) 的资产、哈希和签名；发布结果以 Release 正文及可复核资产为准。

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v1.2.0/install.sh | sudo sh -s -- install --version v1.2.0
```

安装器会创建受限系统账号、回环监听配置、`0600` 环境文件和 systemd unit，并对 tarball 外层 sidecar、包内 `SHA256SUMS`、版本号与 ELF 架构逐层校验。自动生成的是持续有效的管理员密码，不是一次性口令。stdout 为 TTY 时首次创建凭据会显示一次密码；管道、CI 等非 TTY 默认不显示，可由 root 查看 `/etc/agentsql/agentsql.env`，或明确传入 `--show-password`。v0.5.1 发行包构建放入匹配架构、经 SHA-256 校验的独立 YashanDB C 客户端，安装器将其放到 `/usr/local/lib/agentsql/yashandb`；正式资产还须通过包内内容检查。

常用生命周期命令（使用前核对目标版本的正式资产与签名）：

```bash
sudo ./install.sh install --version v1.2.0
sudo ./install.sh upgrade --version v1.2.0
sudo ./install.sh uninstall
sudo ./install.sh uninstall --purge --yes
```

默认卸载删除两个二进制、unit 和受管 YashanDB 客户端目录，保留 `/etc/agentsql`、`/var/lib/agentsql` 及系统账号。升级快照保存在 root 专用的 `/var/backups/agentsql`，即使 `--purge` 也会保留，确认不再需要后应由管理员单独归档或清理。`--purge` 只删除前述配置和数据两个固定目录，不跟随配置中的外部数据库、证书或其他路径；非交互环境必须同时传 `--yes`。只有账号确由安装器创建且已无其他文件归属时，额外的 `--remove-user` 才会删除它。失败升级留下的快照中，`agentsql.db` 等文件保留了 agentsql 属主（用于原样回滚），因此 `uninstall --purge --remove-user` 会据此拒绝删账号并点名残留文件；确认快照不再需要后，先执行 `sudo chown -R root:root /var/backups/agentsql`（或归档后删除该目录）再重跑卸载命令，即可连同系统账号一并删除。全新安装、从未发生过失败升级的主机不存在该快照，`uninstall --purge --yes --remove-user` 可一次清空二进制、unit、配置、数据和账号。

离线安装可把已解压包目录传给 `--from`，或传入 tarball；传 tarball 时同目录必须有 `<tarball>.sha256`。从发布包根执行 `sudo ./install.sh install` 会自动使用当前已校验的包，不访问网络。以下是 v1.2.0 资产路径格式示例；使用前须完成正式 Release 下载与验签：

```bash
sudo ./install.sh install --from /srv/releases/agentsql-v1.2.0-linux-amd64
sudo ./install.sh upgrade --from /srv/releases/agentsql-v1.2.0-linux-amd64.tar.gz
```

`--no-start` 只用于 chroot 或镜像预安装：允许在 systemd 不是 PID 1 时写入文件，但不会启动或健康检查，安装结果在 systemd 成功启动前不可用。升级默认只自动备份 `/var/lib/agentsql/agentsql.db` 及其 `-wal`、`-shm`；使用 PostgreSQL 控制面或自定义 SQLite 路径时，必须先完成外部一致性备份并明确传 `--external-backup-done`，安装器不会声称已备份外部数据库。若外部数据库升级后的健康检查失败，安装器会恢复二进制、unit、配置和环境文件，但会让旧服务保持停止；操作者必须先恢复外部数据库快照，再启动旧版本。

下载默认只使用 GitHub Release，不会在校验失败时切换镜像。受控网络可显式设置 `AGENTSQL_DOWNLOAD_BASE` 为自建 HTTPS 资产根；该根需按 `<base>/<version>/agentsql-<version>-linux-<amd64|arm64>.tar.gz[.sha256]` 提供同源文件，安装器按本机架构选择。安装 v1.2.0 时应明确 `--version`，例如：

```bash
sudo sh ./install.sh install --version v1.2.0
```

安装器若发现 `restorecon` 会恢复二进制、unit、配置和数据目录的 SELinux 默认上下文，不调用 `chcon`、不关闭 enforcing；遇到拒绝时用 `ausearch -m AVC` 调查。发布 tarball 的许可证位于包根 `LICENSE`，容器镜像内位于 `/usr/share/licenses/agentsql/LICENSE`。

## 方式三：systemd

以下以已下载、解压并完成验签的 v1.2.0 Linux 发行包根目录为工作目录，安装二进制及服务文件：

```bash
sudo useradd --system --user-group --home-dir /var/lib/agentsql --shell /usr/sbin/nologin agentsql
sudo install -d -o root -g agentsql -m 0750 /etc/agentsql
sudo install -d -o agentsql -g agentsql -m 0750 /var/lib/agentsql
sudo install -m 0755 ./agentsql ./agentsqlctl /usr/local/bin/
sudo install -m 0644 deploy/systemd/agentsql.service /etc/systemd/system/agentsql.service
sudo install -o root -g agentsql -m 0640 deploy/systemd/config.yaml /etc/agentsql/config.yaml
```

如果系统已经存在 `agentsql` 用户，跳过 `useradd`。systemd 专用配置模板已固定仅监听 `127.0.0.1:7780`，并将 SQLite 数据写入 `/var/lib/agentsql/agentsql.db`，无需再手工修改 `sqlite_path`。裸机默认只监听 `127.0.0.1`；如需远程访问，应使用 SSH 本地转发或受控反向代理（TLS/鉴权），不要直接把 `http_listen` 改成 `0.0.0.0` 暴露公网。

崖山 C 客户端再分发已获厂家口头授权；仓库无书面授权文件。v0.5.1 发行包构建使用独立 YashanDB C 客户端 23.4.7.100，以 SHA-256 校验后按架构打包；自行从源码构建时需取得同版客户端，使用 `CGO_ENABLED=1`、`-tags yashan` 编入 Go 驱动，并在启动前将客户端 `lib` 目录加入 `LD_LIBRARY_PATH`。服务器镜像内的 C 库在历史 Go 驱动探针中返回 `YAS-02143`，不作为发行运行库。

v0.5.1 GHCR 镜像构建使用的客户端目录为 `/opt/yashandb-client/lib`，最终以公开镜像的双架构内容验收为准。Go 驱动或客户端缺失时，YashanDB 连接失败关闭。标准 parser 与窄 SELECT 路径已完成接线；旧未发布 dry-run 包内的独立客户端通过直连真库参数绑定及只读账号写入拒绝探针。另以真实数据库执行器和测试夹具审计端口验证 SELECT、列血缘、R010、手机号脱敏及三条审计；HTTP/MCP、持久化审计和最小权限账号与流水线组合仍未测，写入、事务与 EXPLAIN 仍失败关闭。

创建仅 root 可读的环境文件：

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

升级前先执行完整备份，盘点 enabled `hash` 规则并确认对应 `AGENTSQL_REDACTION_HASH_KEY` 已安全注入，然后停止当前实例、替换两个二进制或容器镜像并重新启动。`store.auto_migrate: true`（默认值）时，AgentSQL 启动会对当前配置的 SQLite 或 PostgreSQL metadata 存储自动、幂等地执行尚未应用的 migration；分库部署的 audit-only PostgreSQL 走独立迁移流。`auto_migrate: false` 时启动只核对版本、不执行 DDL，必须先由 migration owner 运行 `agentsqlctl migrate`。enabled `hash` 无 key 会在对外服务前拒绝启动。

### `auto_migrate: false` 的升级前预检与显式迁移

使用**待部署版本**的 `agentsqlctl` 和将要用于服务的同一份配置、环境变量，先核对配置和数据库版本：

```bash
agentsqlctl check-config --config /etc/agentsql/config.yaml
agentsqlctl migrate --check --config /etc/agentsql/config.yaml
```

`migrate --check` 只读 `schema_migrations`，不执行 DDL；合并存储输出一行 `role=metadata`，独立审计库存储分别输出 `role=metadata` 和 `role=audit`。每行都有 `driver`、`current`、`latest`、`status`。退出码 `0` 表示所有已配置迁移流均为 `status=current`；`2` 表示至少一条为 `status=pending`，是升级前可以预期的结果，**不能据此启动新服务**；`1` 表示配置、连接、缺失版本表、迁移历史断档或版本超前等错误，应先查清原因。SQLite 路径不存在时检查会失败，不会创建空库。首次安装空库直接执行下文的 `migrate`，再执行 `--check`。

生产升级按以下顺序执行；PostgreSQL 的 DSN 通过受保护环境变量注入，不能写在命令行、配置仓库或终端日志中：

1. 确认待部署二进制、配置及上述预检结果，停止所有 AgentSQL 实例和写入，再对实际配置的 metadata 与 audit 存储做一致性备份，同时保留匹配的 `AGENTSQL_SECRET`。合并库备份一次；分库部署必须分别备份两库并记录协调恢复点。先演练恢复，再进入迁移窗口。
2. PostgreSQL 临时切换到具备 DDL 权限的 migration owner DSN；SQLite 使用可写且已备份的数据库文件。保持服务停止，运行 `agentsqlctl migrate --config /etc/agentsql/config.yaml`。该命令会执行 DDL；独立 audit 库在 metadata 后迁移，两库之间没有原子事务。任一步失败都不要启动新版本，保留现场并按备份恢复计划处理。
3. 继续用待部署版本的 `agentsqlctl migrate --check --config /etc/agentsql/config.yaml`，要求退出码为 `0`，且每行 `current=latest`、`status=current`。为新表、索引和序列补齐运行账号的最小权限，再把 DSN 切回运行账号，并再次执行同一检查，确认运行账号能读取两个版本表。
4. 替换服务二进制或镜像，启动后检查 `/readyz`，并验收适用的 allow、deny、脱敏、审批与持久审计写入。任一关键检查失败时停止服务，按升级前备份恢复数据库、对应 `AGENTSQL_SECRET` 和旧版程序；不要只回滚二进制后继续使用新 schema。

针对 v0.8.0 升级，先停止旧服务写入并成套备份 metadata、audit（若分库）和配对的 `AGENTSQL_SECRET`。SQLite 合并库必须使用一致性备份，不能只复制尚有 WAL 页面未合并的主数据库文件。隔离演练已验证从 v0.7.0 升到 v0.8.0，再恢复升级前备份并由 v0.7.0 启动：SQLite metadata 18 → 19 → 18；PostgreSQL metadata/audit 分库 17/6 → 18/6 → 17/6。旧程序不能直接读取新版 schema；生产部署仍须按目标环境权限和数据量独立演练恢复。

针对 v0.9.0 升级，先用 v0.8.0 正式签名包与配对密钥在隔离环境做成套备份，再停止写入并运行 `agentsqlctl migrate --check`。SQLite 合并库 metadata 19 → 20；PostgreSQL 独立 metadata 18 → 19，audit-only 保持 6。新增 `max_uses`（可空）与 `used_count`（默认 0）；旧授权空预算继续有效。回退必须恢复升级前 metadata、audit（分库时）与 `AGENTSQL_SECRET` 的同一快照，再由 v0.8.0 启动；直接执行新版 schema 上的旧二进制不受支持。SQLite 仍须一致性备份，生产环境需单独验证表锁、备份恢复和实际数据量。

针对 v1.0.0 升级，先备份 v0.9.0 的 metadata、audit（若分库）及配对 `AGENTSQL_SECRET`，再用新版 `agentsqlctl migrate --check` 验证两条迁移流。该版没有新增 schema 迁移，SQLite metadata 维持 20，PostgreSQL 独立 metadata 维持 19、audit-only 维持 6。启动前核对已有列策略和 PostgreSQL B2 健康：配置列策略但 B2 不可用的 SELECT 在 v1.0.0 会执行前拒绝。人员委托默认关闭；启用前先验证客户端可安全地设置第二个 HTTP 请求头。回退须停止新服务并恢复成套备份和旧版程序，不得只替换二进制。

针对 v1.2.0 升级，从 v1.1.0 正式包及其 metadata、audit（若分库）、`AGENTSQL_SECRET` 的同一快照开始。先运行 `agentsqlctl migrate --check`，停写并完成一致性备份，再执行新版本迁移。本版预计不增加 schema：SQLite 与 PostgreSQL 合并库保持 21，独立 PostgreSQL metadata 保持 20，audit-only 保持 6。回退仍需停止服务并恢复整套升级前快照和 v1.1.0 二进制，再验证审计链、审批状态及旧 MCP 入口。启用 OAuth 前要配置与控制台 OIDC 完全一致的 issuer 和 tenant、面向 `/mcp/oauth` 的资源 URI、客户端的 `agentsql:mcp` scope 与独立 Agent Key 请求头；先对缺失/错误凭据做拒绝验证。

历史 v0.7.0 回退：SQLite 合并库在恢复升级前数据库与配对密钥后可回 v0.6.0；PostgreSQL metadata/audit 分库回退到 [v0.6.1 正式版](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1)。原 v0.6.0 分库服务有管理审计路由缺陷，不能作为该拓扑的回退目标。历史细节见 [v0.7.0 发布说明](release-notes-v0.7.0.md)。

`agentsqlctl upgrade apply --dry-run` 只验证下载产物和文件计划，不检查数据库版本，也不会替代上述预检或备份。`migrate --check` 验证迁移版本记录的连续性，不是数据库结构、数据内容或跨库一致性的完整校验。

完整的值班操作与故障处理步骤见 [v0.7 运维手册](v0.7-operations-runbook.md)。

### v0.3 `range` 控制面迁移

v0.3 为保存数值分桶和日期截断参数，在 `mask_rules` 增加 `range_bucket_width INTEGER`、`range_bucket_offset INTEGER`、`range_granularity TEXT` 三个可空列。三列都没有 `NOT NULL`、没有数据库默认值，迁移不回填，既有规则的三列自然为 `NULL`。四份 `0004` 的作用和真实 DDL 如下。

- `internal/store/migrations/sqlite/0004_range_redaction.sql`：默认合并式 SQLite 控制面。

```sqlite
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

- `internal/store/migrations/postgres/0004_range_redaction.sql`：默认合并式 PostgreSQL 控制面。

```postgres
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

- `internal/store/migrations/metadata/sqlite/0004_range_redaction.sql`：分库部署的独立 SQLite metadata 库。

```sqlite
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

- `internal/store/migrations/metadata/postgres/0004_range_redaction.sql`：分库部署的独立 PostgreSQL metadata 库。

```postgres
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

audit-only 库没有 `mask_rules`，因此 `internal/store/migrations/audit` 不增加这三列，也没有对应的 `0004_range_redaction.sql`。

#### PostgreSQL 升级影响与操作顺序

PostgreSQL 中这三条 `ADD COLUMN` 都增加可空且无默认值的列，通常只更新系统目录，不重写 `mask_rules` 表；但 `ALTER TABLE` 仍会取得 `ACCESS EXCLUSIVE` 锁，可能等待现有事务并在执行期间短暂阻塞并发访问。不要把它描述为“无锁升级”。即使表很小，也建议在低峰或维护窗口执行，并按变更流程设置合适的锁等待上限。

推荐顺序是：停止 AgentSQL 写入；备份 SQLite 文件，或按前文分别备份 PostgreSQL metadata/audit 库；应用迁移（PostgreSQL 使用 migration owner）；执行下一节只读预检并处理返回行；最后换回最小权限运行账号，启动服务并检查 `/readyz`。分库部署只有 metadata 库发生本次 DDL，但仍应保留成对控制面备份和对应的 `AGENTSQL_SECRET`。

回滚不是只替换二进制。先停止服务，并在回到旧版本前停用或移除旧版本无法识别的 `range` 规则；schema 回滚应移除这三列，或直接恢复升级前备份。仅从列结构看，这三列可空、无默认且不改变旧列，对使用显式列清单的旧版本无害，但旧版本不识别 `range` 算法及其参数，不能据此假定 enabled `range` 规则也可兼容运行。恢复备份是需要完整回到升级前状态时的首选。

#### enabled `range` 规则启动前预检

在 `0004` 已应用、重启新版本前执行以下只读查询。只有 `algo='range'` 或任一 range 参数非 `NULL` 的规则才进入这组校验；查询只返回 enabled 脏规则，不修改数据。结果必须为空，否则 `validateEnabledRules` 会在启动装配时 fail-fast。disabled 脏草稿不会阻止启动，可在服务恢复后经管理 API 修复。

`number` 规则要求 `range_bucket_width` 为 `1..1,000,000,000` 的整数；`range_bucket_offset` 可为 `NULL`（规范化为 `0`），显式值须为 `-1,000,000,000..1,000,000,000` 的整数，且不得带 `range_granularity`。`date` 规则不得带 width/offset；`range_granularity` 可为 `NULL`（规范化为 `year`），显式值只允许 `year`、`quarter`、`month`，空串也会被查出。非 `range` 算法携带任一 range 参数同样是脏数据。

SQLite：

```sqlite
SELECT id,
       datasource_id,
       column_name,
       sensitive_type,
       algo,
       range_bucket_width,
       range_bucket_offset,
       range_granularity
FROM mask_rules
WHERE enabled = 1
  AND (
    (
      algo = 'range'
      AND (
        sensitive_type NOT IN ('number', 'date')
        OR (
          sensitive_type = 'number'
          AND (
            range_bucket_width IS NULL
            OR typeof(range_bucket_width) <> 'integer'
            OR range_bucket_width < 1
            OR range_bucket_width > 1000000000
            OR (
              range_bucket_offset IS NOT NULL
              AND (
                typeof(range_bucket_offset) <> 'integer'
                OR range_bucket_offset < -1000000000
                OR range_bucket_offset > 1000000000
              )
            )
            OR range_granularity IS NOT NULL
          )
        )
        OR (
          sensitive_type = 'date'
          AND (
            range_bucket_width IS NOT NULL
            OR range_bucket_offset IS NOT NULL
            OR (
              range_granularity IS NOT NULL
              AND range_granularity NOT IN ('year', 'quarter', 'month')
            )
          )
        )
      )
    )
    OR (
      algo <> 'range'
      AND (
        range_bucket_width IS NOT NULL
        OR range_bucket_offset IS NOT NULL
        OR range_granularity IS NOT NULL
      )
    )
  )
ORDER BY id;
```

PostgreSQL 的 `INTEGER` 列本身只保存整数，因此不需要 SQLite 的存储类型检查：

```postgres
SELECT id,
       datasource_id,
       column_name,
       sensitive_type,
       algo,
       range_bucket_width,
       range_bucket_offset,
       range_granularity
FROM mask_rules
WHERE enabled IS TRUE
  AND (
    (
      algo = 'range'
      AND (
        sensitive_type NOT IN ('number', 'date')
        OR (
          sensitive_type = 'number'
          AND (
            range_bucket_width IS NULL
            OR range_bucket_width < 1
            OR range_bucket_width > 1000000000
            OR range_bucket_offset < -1000000000
            OR range_bucket_offset > 1000000000
            OR range_granularity IS NOT NULL
          )
        )
        OR (
          sensitive_type = 'date'
          AND (
            range_bucket_width IS NOT NULL
            OR range_bucket_offset IS NOT NULL
            OR (
              range_granularity IS NOT NULL
              AND range_granularity NOT IN ('year', 'quarter', 'month')
            )
          )
        )
      )
    )
    OR (
      algo <> 'range'
      AND (
        range_bucket_width IS NOT NULL
        OR range_bucket_offset IS NOT NULL
        OR range_granularity IS NOT NULL
      )
    )
  )
ORDER BY id;
```

#### SQLite → PostgreSQL 搬迁

`migrate-sqlite-to-postgres` 的 `mask_rules` manifest 已在 `enabled` 后纳入 `range_bucket_width`、`range_bucket_offset`、`range_granularity`，由同一 manifest 生成源端 `SELECT` 与目标端写入。搬迁逐值保真：SQL `NULL` 与显式 `range_bucket_offset=0` 不会混淆，`range_bucket_width=25`、`range_granularity='quarter'` 等值也会原样携带，无需手工补列、补默认值或另行转换。

一般回滚同样不是只替换二进制：停止服务，将二进制或镜像恢复到旧版本，同时恢复升级前的 SQLite 或 PostgreSQL 备份，再启动服务。不要让旧版本直接读取包含其不识别规则的数据。

### v0.3 表.列感知脱敏控制面迁移（0005）

`0005_mask_rule_scope.sql` 只修改 AgentSQL 控制面中的 `mask_rules`，不会对受保护的 PostgreSQL/MySQL 业务库执行 DDL。它存在于四条含 metadata 的迁移流中；audit-only 库没有 `mask_rules`，因此没有 `0005`：

| 部署形态 | 实际迁移文件 | SHA-256 |
| --- | --- | --- |
| 默认合并式 SQLite（metadata + audit） | `internal/store/migrations/sqlite/0005_mask_rule_scope.sql` | `ea43d52616b75fcb3385d021b25a5bfbea08866ff46236026ba0413180c2784f` |
| 独立 metadata SQLite | `internal/store/migrations/metadata/sqlite/0005_mask_rule_scope.sql` | `ea43d52616b75fcb3385d021b25a5bfbea08866ff46236026ba0413180c2784f` |
| 默认合并式 PostgreSQL（metadata + audit） | `internal/store/migrations/postgres/0005_mask_rule_scope.sql` | `eb7946179a70742d6d19a1c45d4c2901c846d201a9f1a52764bca123b6eea848` |
| 独立 metadata PostgreSQL | `internal/store/migrations/metadata/postgres/0005_mask_rule_scope.sql` | `eb7946179a70742d6d19a1c45d4c2901c846d201a9f1a52764bca123b6eea848` |

#### 实际 DDL、执行顺序与存量语义

两份 SQLite 文件逐字相同，按以下顺序在一个 migration 事务中执行：

```sqlite
ALTER TABLE mask_rules ADD COLUMN schema_name TEXT;

UPDATE mask_rules SET schema_name = '', table_name = '';

DROP INDEX IF EXISTS ux_mask_rules_scope_column;

CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(TRIM(datasource_id),''),''),
  schema_name,
  table_name,
  LOWER(TRIM(column_name))
);
```

两份 PostgreSQL 文件也逐字相同，唯一的方言差异是使用 `BTRIM`：

```postgres
ALTER TABLE mask_rules ADD COLUMN schema_name TEXT;

UPDATE mask_rules SET schema_name = '', table_name = '';

DROP INDEX IF EXISTS ux_mask_rules_scope_column;

CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(BTRIM(datasource_id),''),''),
  schema_name,
  table_name,
  LOWER(BTRIM(column_name))
);
```

`schema_name` 在 DDL 层是可空 `TEXT`，没有默认值；应用的 INSERT/UPDATE 路径会把 `schema_name`、`table_name` 的空作用域写成空串。迁移中的 `UPDATE` 会把所有存量行的两列同时置为 `''`，不是保留旧 `table_name`：v0.3 之前运行时丢弃 `table_name`，所以这次一次性固化为“全局列规则”，保持升级前的实际生效语义。新唯一键中的 datasource 仍把 `NULL`、空串和纯空白归为同一作用域，schema/table 按原值精确区分，只有 `column_name` 做 trim 后小写归一化。

三档存储语义如下：

| `schema_name` | `table_name` | 作用域 |
| --- | --- | --- |
| `''` | `''` | 全局列规则 |
| `''` | 非空 | table-only 规则；PostgreSQL 匹配任意 schema，MySQL 表示当前库中的该表 |
| 非空 | 非空 | 精确 `schema.table.column` 规则 |

`schema_name` 非空而 `table_name` 为空不是合法作用域。schema/table 比较保留大小写并精确匹配；column 继续使用宽松的归一化列名。

#### 执行方式与版本核对

`auto_migrate: true` 时，默认合并式 SQLite、独立 metadata SQLite、默认合并式 PostgreSQL、独立 metadata PostgreSQL 都会在启动准备 metadata 存储时执行各自的 `0005`。生产 PostgreSQL 建议继续使用 `auto_migrate: false`：先停止写入，以 migration owner 显式执行迁移，再换回最小权限运行账号。

```bash
agentsqlctl migrate --config /etc/agentsql/config.yaml
```

合并式存储的成功输出应为对应 driver 的版本 5：

```text
migration driver=sqlite current=5 latest=5
```

或：

```text
migration driver=postgres current=5 latest=5
```

metadata/audit 分库时 metadata 行应为 `current=5 latest=5`；独立 audit PostgreSQL 的迁移流仍是 `current=2 latest=2`：

```text
metadata migration driver=postgres current=5 latest=5
audit migration driver=postgres current=2 latest=2
```

#### PostgreSQL 锁、耗时与升级前预检

PostgreSQL 的 `ALTER TABLE ... ADD COLUMN` 会取得 `ACCESS EXCLUSIVE` 锁；随后 `UPDATE` 会改写所有 `mask_rules` 存量行，普通 `DROP INDEX` / `CREATE UNIQUE INDEX` 也不是并发版本，新索引需要扫描并排序表数据，且 migration 事务提交前相关锁不会提前释放。`mask_rules` 通常很小，但大表、长事务、慢存储或锁等待仍可能延长维护窗口并阻塞并发读写。应先在预发用生产规模副本演练，记录耗时和锁等待，在低峰或维护窗口停止 AgentSQL 写入后升级，并按平台规范设置锁等待/语句超时。

升级前必须备份实际承载 metadata 的数据库；分库部署仍应按前文保留成对控制面备份与对应的 `AGENTSQL_SECRET`。例如 PostgreSQL：

```bash
pg_dump -Fc --dbname "$AGENTSQL_STORE_METADATA_DSN" --file agentsql-metadata-before-0005.dump
```

SQLite 应在停止写入后使用一致性备份；使用 `sqlite3` CLI 时可执行：

```bash
sqlite3 /var/lib/agentsql/agentsql.db ".backup 'agentsql-before-0005.db'"
```

PostgreSQL 升级前先记录规则数、旧 `table_name` 使用情况，并确认没有空列名、NULL 作用域或绕过约束造成的旧键重复。`legacy_nonempty_table_names` 只用于评估影响：这些值会被 `0005` 清空。

```postgres
SELECT COUNT(*) AS total_rules,
       COUNT(*) FILTER (WHERE COALESCE(table_name, '') <> '') AS legacy_nonempty_table_names,
       COUNT(*) FILTER (
         WHERE table_name IS NULL OR column_name IS NULL OR BTRIM(column_name) = ''
       ) AS invalid_rows
FROM mask_rules;

SELECT COALESCE(NULLIF(BTRIM(datasource_id),''),'') AS datasource_scope,
       LOWER(BTRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2
HAVING COUNT(*) > 1;
```

SQLite 等价预检：

```sqlite
SELECT COUNT(*) AS total_rules,
       SUM(CASE WHEN COALESCE(table_name, '') <> '' THEN 1 ELSE 0 END) AS legacy_nonempty_table_names,
       SUM(CASE
             WHEN table_name IS NULL OR column_name IS NULL OR TRIM(column_name) = '' THEN 1
             ELSE 0
           END) AS invalid_rows
FROM mask_rules;

SELECT COALESCE(NULLIF(TRIM(datasource_id),''),'') AS datasource_scope,
       LOWER(TRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2
HAVING COUNT(*) > 1;
```

`invalid_rows` 和重复查询都应返回零；否则先停止升级并通过受支持的管理 API 或经审计的人工变更修复。不要用删除重复行的通用脚本替代逐条确认规则意图。

#### 迁移后、启动前核对

PostgreSQL 用以下只读查询确认列、索引定义和新键唯一性。列查询应看到 `schema_name`、`table_name`；索引定义应包含 datasource、schema、table 和归一化 column 四部分；后两条异常查询应返回零行。

```postgres
SELECT column_name, data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'mask_rules'
  AND column_name IN ('schema_name', 'table_name')
ORDER BY ordinal_position;

SELECT indexname, indexdef
FROM pg_indexes
WHERE schemaname = 'public'
  AND tablename = 'mask_rules'
  AND indexname = 'ux_mask_rules_scope_column';

SELECT id, datasource_id, schema_name, table_name, column_name
FROM mask_rules
WHERE schema_name IS NULL
   OR table_name IS NULL
   OR (schema_name <> '' AND table_name = '')
   OR column_name IS NULL
   OR BTRIM(column_name) = '';

SELECT COALESCE(NULLIF(BTRIM(datasource_id),''),'') AS datasource_scope,
       schema_name,
       table_name,
       LOWER(BTRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2, 3, 4
HAVING COUNT(*) > 1;
```

SQLite 可用 `PRAGMA` 与 `sqlite_master` 做同样核对：

```sqlite
PRAGMA table_info(mask_rules);

SELECT name, sql
FROM sqlite_master
WHERE type = 'index'
  AND tbl_name = 'mask_rules'
  AND name = 'ux_mask_rules_scope_column';

SELECT id, datasource_id, schema_name, table_name, column_name
FROM mask_rules
WHERE schema_name IS NULL
   OR table_name IS NULL
   OR (schema_name <> '' AND table_name = '')
   OR column_name IS NULL
   OR TRIM(column_name) = '';

SELECT COALESCE(NULLIF(TRIM(datasource_id),''),'') AS datasource_scope,
       schema_name,
       table_name,
       LOWER(TRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2, 3, 4
HAVING COUNT(*) > 1;
```

最后再次运行 `agentsqlctl migrate`，断言 metadata `current=5 latest=5`，换回运行账号，以 `auto_migrate:false` 启动并检查 `/readyz`。

#### 回滚与手工 down

首选回滚路径是停止服务，恢复升级前备份及与其配对的 `AGENTSQL_SECRET`，再启动旧版本。仓库没有自动 down migration，不能只替换二进制并假定自动降级。

代码可确认：旧版本使用显式列清单，因此单独多出一个可空 `schema_name` 列不会让旧 SQL 立即报“列数不匹配”；但旧版本不理解 schema/table 作用域，且旧唯一键只允许每个 datasource 作用域中存在一条归一化同名列规则。新索引允许不同表各有一条同名列规则，旧版本加载多条 enabled 同名规则时会以重复列规则拒绝启动；即使只有一条 scoped 规则，旧版本也会忽略 `table_name`，把它按全局列规则执行。另有版本门禁差异：旧版本在 `auto_migrate:false` 时会因数据库 `current=5`、代码 `latest=4` 拒绝启动。由此，保留 `0005` schema 只回滚代码不属于受支持路径。

若无法恢复备份，必须在仍运行新版本且服务已停止写入时人工 down。先导出全部规则，逐条删除、合并或改写 `schema_name <> '' OR table_name <> ''` 的规则，使每个旧键只剩一条，并明确接受其在旧版本中变成全局列规则；不能无损保留 table-only 或 schema.table 语义。以下重复查询必须返回零行后才能继续：

```postgres
SELECT COALESCE(NULLIF(BTRIM(datasource_id),''),'') AS datasource_scope,
       LOWER(BTRIM(column_name)) AS column_key,
       STRING_AGG(id, ',' ORDER BY id) AS rule_ids
FROM mask_rules
GROUP BY 1, 2
HAVING COUNT(*) > 1;
```

PostgreSQL 手工 down 的结构步骤如下；应先在预发验证，并由 migration owner 在事务中执行：

```postgres
BEGIN;
DROP INDEX IF EXISTS ux_mask_rules_scope_column;
ALTER TABLE mask_rules DROP COLUMN schema_name;
CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(BTRIM(datasource_id),''),''),
  LOWER(BTRIM(column_name))
);
DELETE FROM schema_migrations WHERE version = 5;
COMMIT;
```

SQLite 先用上文等价的 `TRIM` 重复查询确认旧键唯一，再执行：

```sqlite
BEGIN IMMEDIATE;
DROP INDEX IF EXISTS ux_mask_rules_scope_column;
ALTER TABLE mask_rules DROP COLUMN schema_name;
CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(TRIM(datasource_id),''),''),
  LOWER(TRIM(column_name))
);
DELETE FROM schema_migrations WHERE version = 5;
COMMIT;
```

若运维环境的 SQLite CLI 不支持 `DROP COLUMN`，不要临时拼装未验证的重建表脚本；恢复升级前备份，或在预发验证“新建旧结构表、显式列复制、重建索引、原子换表”的等价流程。手工 down 完成后，以旧版 `agentsqlctl migrate` 核对 `current=latest=4`，再启动旧服务。

#### SQLite → PostgreSQL 搬迁

`migrate-sqlite-to-postgres` 的 `mask_rules` manifest 同时包含 `table_name` 和 `schema_name`，源端 `SELECT`、目标端 INSERT 与摘要哈希由同一列清单生成。搬迁逐值保留两列，SQL `NULL` 与空串不会混淆；目标 PostgreSQL 会先执行到 metadata 版本 5，再由目标端 `0005` 建立同构的 `(datasource, schema, table, normalized column)` 唯一索引。也就是说，规则数据逐值复制，索引语义由目标端 migration 重建，而不是复制 SQLite 的 `sqlite_master` 定义。搬迁前后都应执行本节的新键重复查询，并核对规则行数及 scope 分布。

## 发布与分发

发布者应按以下顺序构建、验证并分发 v1.2.0。版本范围和验收边界见[发布说明](release-notes-v1.2.0.md)，15 项资产见[发布资产清单](RELEASE_ASSETS.md)；v0.5.0 的签名示例见[历史发布日清单](release-day-checklist-v0.5.md)。正式生产准备须从最终封板提交的完整干净工作树启动，执行前后核对 `git rev-parse HEAD` 与 `git status --porcelain --untracked-files=all`；脚本还会检查关键输入哈希并在构建结束前再次拒绝源码漂移。正式发布必须使用 `scripts/release-dryrun.ps1` 已固定并审核的 Rocky Linux 与 Syft digest；禁止把未固定的 `rockylinux:8` 默认值用于正式产物：

```powershell
# 无私钥、无 push 的完整双架构预演：15 个不可发布 dry-run 资产 + 三个 OCI 证据
pwsh ./scripts/release-dryrun.ps1 -Version v1.2.0

# 发布准备：由受控正式公钥生成 11 个待签名生产候选；随后签名并组成 15 项
$trustedReleasePublicKey = '<独立核验的发布公钥绝对路径>'
pwsh ./scripts/release-dryrun.ps1 -Version v1.2.0 -ProductionPrepare -PublicKeyPath $trustedReleasePublicKey

# 负责人取得 write:packages 后，只推精确版本和辅助 tag，不移动 latest
pwsh ./scripts/build-ghcr-multiarch.ps1 -Version v1.2.0 -IncludeAuxiliaryTags -Push

# 本轮官网与公网 Demo 延至 v1.5.0，v1.2.0 不执行 -PromoteLatest。
```

多架构脚本默认只把 `linux/amd64` + `linux/arm64` manifest 导出到 `dist/ghcr-agentsql-v1.2.0-oci.tar`，不登录、不推送，并解析 OCI index 断言两个目标平台。`-IncludeAuxiliaryTags` 通过同一个 Buildx Bake 构建图生成 demo base 与叠加 `config.demo.yaml` / `demo-seed.yaml` 的 quickstart 镜像。`-Push` 只推精确 tag，并查询和报告 GHCR package 是否为 public；若不是 public，须在 package 设置中处理并重新做匿名拉取验收。推送前须具备 `write:packages` 权限，脚本会强制核对干净工作树、本地 annotated tag、远端 GitHub tag 与 HEAD 一致。本轮 v1.2.0–v1.4.0 的官网、Live Demo 和 `latest` 保持 v1.0.0，待 v1.5.0 一次更新。之后必须按顺序完成：

1. 核对 GHCR 中 `v1.2.0`、`v1.2.0-demo`、`v1.2.0-quickstart` 都是 public 且包含 `linux/amd64`、`linux/arm64`。quickstart 镜像必须实际包含两份 demo 配置。
2. 分别在 amd64 与 arm64 的无 GHCR 登录干净环境执行 `docker pull ghcr.io/cuipengdba/agentsql:v1.2.0`；两边均以回环端口启动，验证 `/healthz` 返回 `v1.2.0` 且 `/readyz` 就绪，并完成 quickstart seed/gateway/reset 验收。
3. 保留 `latest` 指向 v1.0.0，核对其摘要未因本版精确 tag 发布而变化。
4. GitHub Release 必须上传发布日清单定义的恰好 15 个文件，而不是仅上传 tar、sidecar 和安装器。draft 回下载后重算外层/包内 SHA-256、执行三份 Ed25519 密码学验签、核对 provenance commit、两个二进制版本及 Yashan driver/client 双架构内容；全部通过后才转正式。

`scripts/package-release.sh` 不读取或覆盖既有 `bin/SHA256SUMS`；`SOURCE_DATE_EPOCH` 可覆盖确定性 tar 的固定时间。GitHub 的 latest 不包含 prerelease，一键安装器也只接受严格的 `vX.Y.Z`。

| 环境 | 原生发布包 | Docker 快速启动 |
| --- | --- | --- |
| x86_64 + glibc 2.28+ + systemd PID 1 | 支持 | 支持 |
| x86_64 + glibc 2.28+，无 systemd PID 1 | 仅 `--no-start` 预安装 | 支持 |
| aarch64 / arm64 + glibc 2.28+ + systemd PID 1 | v0.4.0 起支持 | 支持，GHCR 自动选择 arm64 |
| aarch64 / arm64 + glibc 2.28+，无 systemd PID 1 | v0.4.0 起仅 `--no-start` 预安装 | 支持，GHCR 自动选择 arm64 |
| CentOS 7 / glibc 2.17 | 不支持 | 使用 Debian/glibc 镜像 |
| Alpine / musl | 不支持 | 使用 Debian/glibc 镜像 |
| amd64 / arm64 之外的架构 | 无原生包 | 本快速脚本不支持 |

## 备份铁律

必须成对备份：

1. `agentsql.db` SQLite 文件。
2. 与该数据库对应的 `AGENTSQL_SECRET`。

二者缺一不可。密钥丢失后，SQLite 中已经加密的数据源密码无法解密。复制 SQLite 文件前应停止写入或使用 SQLite 一致性备份机制；不要只复制正在写入的主文件。

更换 `AGENTSQL_SECRET` 会使既有数据源密码无法解密，这不是无损密钥轮换。v0.1 没有在线重加密流程；需要更换时，必须先规划数据源凭据重新录入与可回滚的 SQLite 备份。

## 本地测试模式

只有本地开发或测试需要兼容仓库历史公开测试凭据时，才可设置 `AGENTSQL_INSECURE=1`。它只放行“长度正确的公开测试 SECRET”和“非空弱管理员口令”；SECRET 缺失或不是 32 字节、管理员口令为空仍会拒绝启动。该开关不会、也不得关闭认证、安全规则或 fail-closed 行为，生产环境禁止设置。

## B2 PostgreSQL 列级授权（默认开启）

B2 列级 SELECT 在 PostgreSQL 上出厂默认 `enabled: true`；普通 schema-qualified 基表默认使用免安装扩展的 `CATALOG_CLOSED_V1`，原生 `agentsql_binder` 仅用于 view / matview / 复杂 lineage 的可选增强。MySQL 不进入 B2 PostgreSQL 路径。以下是等价的显式配置；只有需要回退到既有表级保护时才显式设为 `false`：

```yaml
column_authorization:
  enabled: true
  instance_id: gateway-prod-a
  lease_ms: 15000
  heartbeat_interval_ms: 5000
```

启动时会核对 PG14–18 catalog binding、enrollment、control fence 和 request reservation；使用原生 binder 时还会验证 `agentsql_binder` ABI/capability 与签名 digest。`/healthz` 与 `/readyz` 的 `b2.state/reason` 以及管理审计记录明确给出状态。默认 enforcement 激活失败或运行中 lease 失效时，PostgreSQL B2 列级入口 fail-closed 且 readiness 变为不可用，不会静默降为表级授权；只有显式 `enabled: false` 或 dry-run 才走既有表级路径。

## MCP 传输会话与 B5 PostgreSQL 会话（默认开启）

Streamable HTTP 传输会话通过 `mcp.http` 独立配置，默认开启，空闲超时为 10 分钟。它与 `open_session`/`close_session` 使用的 B5 逻辑会话不是同一层：关闭 `mcp.sessions.enabled` 不会关闭传输会话，设置 `mcp.http.stateful: false` 也不会隐藏 B5 工具。

B5 跨请求逻辑会话与 PostgreSQL 计划事务出厂默认开启；MySQL 跨请求事务固定不支持，配置 `mcp.transactions.mysql: true` 会拒绝启动。等价的显式配置如下；若要回退 B5，必须同时显式关闭依赖它的 PostgreSQL 事务开关：

```yaml
mcp:
  http:
    stateful: true
    session_timeout_ms: 600000
    event_store_enabled: true
    event_store_max_bytes: 67108864
    event_store_ttl_ms: 1800000
  sessions:
    enabled: true
  transactions:
    postgres: true
    mysql: false
```

`event_store_enabled` 仅在 stateful Streamable HTTP 且协议版本早于 `2026-07-28` 时由 go-sdk 使用。旧协议客户端可通过 `GET /mcp` + `Last-Event-ID` 重放断线期间的连续 SSE 事件；TTL 或容量回收造成缺口时服务端 fail-closed，不返回部分事件。进程重启不会恢复 go-sdk 的内存 session：旧 `Mcp-Session-Id` 收到 404 和 `Mcp-Session-Expired: 1` 后必须重新 `initialize`。残留事件由后续 `Open`/`Append` 惰性回收，不会启动清理 goroutine。

每个 operation 仍只允许一条顶层 SQL，并受预检计划、会话 owner、连接终态和审计屏障约束。`/healthz` 与 `/readyz` 会报告 B5 状态；依赖不可用时 readiness fail-closed，不会把 MySQL 或不受支持的事务形态静默降级执行。

## 常见问题

### 容器映射端口后仍无法访问

容器内监听地址必须是 `0.0.0.0:7780`。`127.0.0.1` 只绑定容器自身回环接口，宿主机端口映射无法访问它。使用 `examples/docker/config.yaml`。

### 为什么不能设置 `CGO_ENABLED=0` 或使用 Alpine

项目的 `pg_query_go` 解析器依赖 cgo。纯 Go 构建会缺少必要符号；Alpine 使用 musl，也不符合当前 glibc 构建与运行约束。官方容器镜像基于 Debian bookworm，并以 multi-arch manifest 同时提供 linux/amd64 与 linux/arm64；官方原生 glibc 包分别使用对应架构的 Rocky Linux 8（glibc 2.28）工具链构建。

### 7780 端口被占用

停止占用进程，或同时调整配置中的 `server.http_listen` 与 Compose 的宿主机端口映射。容器内部端口仍应与配置保持一致。

### 服务提示 AGENTSQL_SECRET 非法

`AGENTSQL_SECRET` 必须恰好 32 字节，不是 32 个任意 Unicode 字符。建议使用 32 个 ASCII 字节，并避免 shell 换行或额外引号进入值本身。

### 已有数据源突然无法连接

确认服务使用的 `AGENTSQL_SECRET` 与创建这些数据源时完全一致。恢复 SQLite 备份时必须同步恢复对应密钥。

## v1.3.0 MySQL 8 单表列策略 SELECT 子集

此功能默认关闭，只覆盖 MySQL 8.0/8.4 的显式列、单个 InnoDB 基础表 `SELECT`。在隔离测试库先准备两种独立账号：业务账号只授予所需表的 `SELECT`，元数据账号只授予实例级 `PROCESS`。二者须连接同一实例，且不要向业务账号授予 `PROCESS`。示例：

```sql
CREATE USER 'agentsql_reader'@'%' IDENTIFIED BY '<business-secret>';
GRANT SELECT ON app.customers TO 'agentsql_reader'@'%';
CREATE USER 'agentsql_identity'@'%' IDENTIFIED BY '<metadata-secret>';
GRANT PROCESS ON *.* TO 'agentsql_identity'@'%';
```

把业务账号放入正常数据源，把元数据密码放入环境变量 `AGENTSQL_MYSQL_IDENTITY_PASSWORD`。配置中以数据源 ID 精确关联：

```yaml
column_authorization:
  enabled: true
  mysql_single_table_enabled: true
  mysql_metadata_accounts:
    mysql-prod-1:
      username: agentsql_identity
      password_env: AGENTSQL_MYSQL_IDENTITY_PASSWORD
```

为同一 Agent 和数据源创建 `object_type: table`、`object_name: app.customers` 的 allow 策略，以及 `object_type: column`、`object_name: app.customers` 的 allow 策略。读取列策略响应的 `ETag`，用管理员或具备 `strategy.manage` 权限的身份提交真库列绑定：

```http
POST /api/v1/policies/{id}/bind-mysql
If-Match: "policy-id-r1"
Content-Type: application/json

{"schema":"app","table":"customers","grants":[{"column":"id","usage":"output"},{"column":"id","usage":"reference"},{"column":"phone","usage":"output"}]}
```

接口从真库发现后在控制事务中再次持 MDL 验证，提交健康绑定、对象 ID 和列权限；若版本变化或表已重建则返回冲突并需重新读取策略。启用开关后，持有列策略的 MySQL 请求只进入列授权路径；缺少元数据凭据、权限、健康绑定、列授权、审计或最终控制面检查时失败关闭。既有不含列策略的 MySQL 请求继续使用原表级路径。登记完成后重启 AgentSQL，使启动探针重新计算 B2 就绪状态。

## v1.4.0 双网关共享登录预算与升级

本版在 PostgreSQL metadata 中持久化控制台密码和 LDAP 登录尝试预算：租户与账号规范化后使用部署密钥计算 HMAC，表中只有 32 字节摘要、窗口起点和次数。同一账号从首次尝试起的 60 秒内最多 10 次；两个网关原子共享。metadata 连接或写入失败时返回 503，不回退到节点内存。SQLite 仍只支持单节点本地预算。预算表最多保留 8192 个未过期账号桶，达到上限时新账号登录失败关闭。

两个网关必须指向同一 PostgreSQL metadata 与 audit，使用一致的 `AGENTSQL_SECRET`、身份和审计配置，并给每个网关配置不同的 `mcp.sessions.instance_id`；若启用列授权，还须配置不同的 `column_authorization.instance_id`。上线前在受控环境核对两节点密钥版本和配置承诺值，执行跨节点登录限流、审计失败关闭及恢复后的链验证。不要混用 v1.3.0 和 v1.4.0 节点承接登录：旧节点只有限流内存桶，会绕过共享预算。

升级时先停止旧网关的登录和 SQL 流量，对 metadata、audit（若独立）及配对密钥做一致性备份。v1.3.0 的 SQLite/合并 PostgreSQL metadata 版本为 21，独立 PostgreSQL metadata 为 20，audit-only 为 6。用 v1.4.0 正式包执行 `agentsqlctl migrate --check` 并确认只有预计的待迁移版本，再执行 `agentsqlctl migrate`；迁移后 SQLite/合并 PostgreSQL metadata 为 22，独立 PostgreSQL metadata 为 21，audit-only 仍为 6。运行账号需要预算表的 SELECT、INSERT、UPDATE、DELETE 权限。再次执行只读 `migrate --check`，要求全部 `current=latest`，再先后启动两个 v1.4.0 网关。旧节点内存中的预算不会搬迁，因此切换必须在停流维护窗口内完成。

回退时停止全部 v1.4.0 网关，恢复升级前同一时点的 metadata、audit 和 `AGENTSQL_SECRET`，再启动 v1.3.0 程序并复核审计链及审批/授权状态。只替换旧二进制或仅恢复 metadata 不构成回退。双节点可靠性只覆盖经验证的共享 PostgreSQL 控制面拓扑，不改变业务数据库支持声明。
