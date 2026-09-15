# AgentSQL 可观测性示例

## 1. 直接抓取指标

AgentSQL 的 `/metrics` 与 `/healthz` 同级且免鉴权：

```bash
curl --fail http://127.0.0.1:7780/metrics
```

## 2. 启动独立观测栈

先按仓库根目录的部署说明启动 `agentsql`，再从仓库根目录合并 Compose 文件启动 Prometheus 与 Grafana：

```bash
docker compose \
  -f docker-compose.yml \
  -f examples/observability/docker-compose.observability.yml \
  --profile observability up -d prometheus grafana
```

Prometheus：<http://127.0.0.1:9090>。Grafana：<http://127.0.0.1:3000>，示例账号为 `admin/agentsql_observability`，仅限本地演示，生产必须替换。

## 3. 导入 Grafana 面板

在 Grafana 中添加 Prometheus 数据源，URL 使用 `http://prometheus:9090`；然后选择 **Dashboards → New → Import**，导入 `examples/observability/grafana_dashboard.json`，并为 `DS_PROMETHEUS` 选择刚创建的数据源。

面板包含 QPS、决策分布、规则命中 Top10、HTTP P99、限流速率和连接池占用。停止独立观测栈：

```bash
docker compose \
  -f docker-compose.yml \
  -f examples/observability/docker-compose.observability.yml \
  --profile observability down
```
