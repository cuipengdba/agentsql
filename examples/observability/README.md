# AgentSQL 可观测性示例

根目录的 `docker-compose.yml` 是唯一 Compose 权威文件。以下一条命令会启动 AgentSQL、Prometheus 和 Grafana：

```bash
AGENTSQL_SECRET='N7vK2mQ9xR4tY8pL6cW3sD5fH1jB0zUa' \
AGENTSQL_ADMIN_PASSWORD='S9afe-Admin-Passphrase-2026' \
docker compose --profile observability up -d --build
```

示例值满足长度与弱口令校验，但只用于本地体验；真实部署请生成并保存自己的随机 SECRET 与强管理员口令。`AGENTSQL_SECRET` 必须恰好为 32 个 ASCII 字节。

- AgentSQL：<http://127.0.0.1:7780>
- Prometheus targets：<http://127.0.0.1:9090/targets>，其中 `agentsql` 应为 `UP`
- Grafana：<http://127.0.0.1:3000>，登录 `admin` / `agentsql_observability` 后首页直接显示 AgentSQL 六面板

Prometheus 数据源和 AgentSQL dashboard 已自动 provisioning，无需手工添加数据源或导入 JSON。六面板分别展示 QPS、决策分布、规则命中 Top10、HTTP P99、限流速率和连接池占用。`/metrics` 与 `/healthz` 同级且免鉴权，可直接检查：

```bash
curl --fail http://127.0.0.1:7780/metrics
```

AgentSQL 在容器内监听 `0.0.0.0:7780` 供同一 Compose 网络中的 Prometheus 抓取；宿主侧的 7780、9090、3000 均只绑定 `127.0.0.1`。

SQLite 元数据与审计记录保存在命名卷 `agentsql-data`，不会在 Linux 宿主仓库目录生成 root 权限文件。停止服务但保留数据：

```bash
docker compose --profile observability down
```

连同命名卷彻底清理（会永久删除 AgentSQL 元数据与审计记录）：

```bash
docker compose --profile observability down -v
```

如需同时启动 PostgreSQL/MySQL 演示库，再加 `demo` profile：

```bash
docker compose --profile observability --profile demo up -d --build
```
