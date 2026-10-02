# AgentSQL Live Demo on ECS (Aliyun Linux 3)

This directory deploys the public **Live Demo** as a Docker Compose stack:
a PostgreSQL instance (with the `agentsql_binder` native extension), a MySQL
instance, and the AgentSQL gateway. The demo control plane is deliberately
fixed — both datasources and the B2 column policies are part of the demo
contract, so the stack cannot be trimmed to a single database.

## Prerequisites

- Docker Engine 24+ and the Compose plugin (`docker compose version`).
- Outbound access to `ghcr.io` (the packages are public after release).
- A host with ~2 vCPU and ~1.5 GB free RAM; the stack uses ~170 MB once
  tuned. Add a swap file if the host is memory constrained:
  ```bash
  dd if=/dev/zero of=/var/swapfile bs=1M count=2048
  chmod 600 /var/swapfile && mkswap /var/swapfile && swapon /var/swapfile
  echo '/var/swapfile none swap sw 0 0' >> /etc/fstab
  ```

## Deploy

From this directory, on the host:

```bash
# 1. Assemble the config, manifest and database init scripts.
bash prepare.sh

# 2. Create and edit credentials. Compose auto-loads this file.
cp .env.example .env
vi .env

# 3. Start the databases and wait for them to become healthy.
docker compose up -d postgres mysql
docker compose ps

# 4. Seed the fixed demo data (runs once).
docker compose run --rm seed

# 5. Start the gateway (bound to 127.0.0.1:17880).
docker compose up -d gateway
curl -s http://127.0.0.1:17880/readyz
```

## TLS / reverse proxy

The gateway is bound to loopback only. Caddy terminates TLS on
`demo.agentsql.cn` and proxies to `127.0.0.1:17880`. The matching Caddy
configuration is in `website/deploy/Caddyfile.stage-b`.

## Reset

The demo data is disposable. To rebuild from scratch:

```bash
docker compose down -v
docker compose run --rm seed
docker compose up -d gateway
```

## Images

- `ghcr.io/cuipengdba/agentsql:v0.5.0` — gateway / agentsqlctl.
- `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5` — PostgreSQL 16 with
  the `agentsql_binder` extension.
- `mysql:8` — upstream MySQL.
