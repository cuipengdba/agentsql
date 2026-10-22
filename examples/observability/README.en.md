# AgentSQL Observability Example

**Language:** [English](README.en.md) | [Simplified Chinese](README.md)

`docker-compose.yml` in the root directory is the main file of the default SQLite, observability and simple demo profiles; the PostgreSQL control plane needs to be superimposed with `docker-compose.controlplane.yml`. The following command starts AgentSQL, Prometheus, and Grafana using only the main file:

```bash
AGENTSQL_SECRET='N7vK2mQ9xR4tY8pL6cW3sD5fH1jB0zUa' \
AGENTSQL_ADMIN_PASSWORD='S9afe-Admin-Passphrase-2026' \
docker compose --profile observability up -d --build
```

The example value meets the length and weak password verification, but is only used for local experience; for real deployment, please generate and save your own random SECRET and strong administrator password. `AGENTSQL_SECRET` must be exactly 32 ASCII bytes.

- AgentSQL：<http://127.0.0.1:7780>
- Prometheus targets: <http://127.0.0.1:9090/targets>, where `agentsql` should be `UP`
- Grafana: <http://127.0.0.1:3000>, after logging in to `admin` / `agentsql_observability`, the home page directly displays the AgentSQL six panels (this is the default password for the local demonstration set by `GF_SECURITY_ADMIN_PASSWORD` in compose, and the port is only bound to loopback; if Grafana is to be externally reachable, it must be changed to a strong password first)

Prometheus data sources and AgentSQL dashboard are automatically provisioned, eliminating the need to manually add data sources or import JSON. The six panels respectively display QPS, decision distribution, rule hits Top10, HTTP P99, current limit rate and connection pool occupancy. `/metrics` is at the same level as `/healthz` and does not require authentication. You can directly check:

```bash
curl --fail http://127.0.0.1:7780/metrics
```

AgentSQL listens to `0.0.0.0:7780` in the container for Prometheus in the same Compose network to crawl; 7780, 9090, and 3000 on the host side are only bound to `127.0.0.1`.

SQLite metadata and audit records are saved in the named volume `agentsql-data`, and root permission files will not be generated in the Linux host repository directory. Stop the service but keep the data:

```bash
docker compose --profile observability down
```

Along with a thorough clean of the named volume (which permanently deletes AgentSQL metadata and audit records):

```bash
docker compose --profile observability down -v
```

If you want to start the PostgreSQL/MySQL demo library at the same time, add `demo` profile:

```bash
docker compose --profile observability --profile demo up -d --build
```
