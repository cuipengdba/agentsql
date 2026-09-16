# AgentSQL 部署指南

AgentSQL 是面向 AI Agent 的数据库安全网关和生产级 MCP Server。元数据与审计记录保存在本地 SQLite，业务数据库仍使用各自的 PostgreSQL 或 MySQL 连接。

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

`/healthz` 是免鉴权存活检查；`/readyz` 会在 1 秒内检查 SQLite 是否可用。

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
