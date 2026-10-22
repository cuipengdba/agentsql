# AgentSQL Local Live Demo

**Language:** [English](DEMO.md) | [Simplified Chinese](../DEMO.md)

> Current guidance: Applies to v3.0.2 and compatible 3.0.x maintenance releases. The historical introduction version and acceptance record retain the original facts; for daily entry, see [ document index ](README.md).

This environment is only used for local loopback demonstration. It creates independent PostgreSQL 16, MySQL 8, SQLite control plane, and AgentSQL gateway; it does not and should not connect to real data or real databases.

## Preconditions

- Docker Engine or Docker Desktop, and Docker Compose v2 supporting `--wait` and `service_completed_successfully`.
- Bash and `curl` for Linux/macOS; PowerShell 5.1 or higher for Windows.
- When building the image for the first time, you need to be able to obtain the existing dependencies of the project. The offline environment should prepare images and Go module build cache in advance.

## Prepare credentials

Copy the environment files from the repository root directory:

```bash
cp examples/docker/demo.env.example demo/demo.env
```

```powershell
Copy-Item examples/docker/demo.env.example demo/demo.env
```

The following public local example in `demo/demo.env` must be replaced before starting:

- `AGENTSQL_SECRET`: Exactly 32 ASCII bytes and kept with the generated SQLite volume.
- `AGENTSQL_ADMIN_PASSWORD`。
- `DEMO_PG_OWNER_PASSWORD` and `DEMO_MYSQL_ROOT_PASSWORD`.
- `AGENTSQL_DEMO_PG_PASSWORD` and `AGENTSQL_DEMO_MYSQL_PASSWORD`: independent read-only passwords for the `agentsql_demo_ro` account in the two databases.
- `AGENTSQL_DEMO_RO_KEY` and `AGENTSQL_DEMO_DML_KEY`: Replace the example values ​​with the legal product-generated `asql_` key.

Do not set `AGENTSQL_INSECURE`. The values ​​in the environment samples are public and are only suitable for short-term local verification on the loopback address and cannot be used in public networks or shared environments.

## One command to start or reset

The reset script always uses Compose project `agentsql-demo` and `demo/demo.env`. When the anchor is not specified, it automatically takes the UTC of the current day. It deletes the old demo volume and recreates the complete data:

```bash
bash ./demo/reset.sh
```

```powershell
.\demo\reset.ps1
```

When the environment file is placed in another location, `AGENTSQL_DEMO_ENV_FILE=/absolute/path/demo.env` (Bash) or `-EnvFile C:\path\demo.env` (PowerShell) can be used. `down -v` will permanently delete previous demo business data, SQLite metadata and audit records.

The manually launched equivalent command must provide the same UTC anchor itself:

```bash
export DEMO_ANCHOR_DATE="$(date -u +%F)"
docker compose -p agentsql-demo -f docker-compose.demo.yml --env-file demo/demo.env up -d --build --wait
```

```powershell
$env:DEMO_ANCHOR_DATE = [DateTime]::UtcNow.ToString('yyyy-MM-dd')
docker compose -p agentsql-demo -f docker-compose.demo.yml --env-file demo/demo.env up -d --build --wait
```

When shipping with Prometheus and Grafana, add `--profile observability` to the manual command.

## Access and data scope

Default address:

- AgentSQL console: <http://127.0.0.1:17880>, log in using `AGENTSQL_ADMIN_USER` and `AGENTSQL_ADMIN_PASSWORD`.
- PostgreSQL: `127.0.0.1:5432`; MySQL: `127.0.0.1:3306`. They are only for local inspection, and the ports can be changed in the environment file.
- Optional Prometheus: <http://127.0.0.1:9090>; Grafana: <http://127.0.0.1:3000>.

Each of the two business libraries has 128 customers, 64 products, 2400 orders, and 16 internal_notes. The control plane includes 2 data sources, 2 Agents, 10 policies, 4 masking rules, 300 audits in the past 30 days, and 24 approvals. Data required for read-only, write-blocking, sensitive columns, low-cardinality scans, unauthorized tables, and audit lookbacks are all in place.

## Real trial run with six scripts

The console only displays the Live Demo UI when `/healthz` returns strictly `demo.enabled=true` and the string `demo.banner`. The real trial run calls `POST /api/v1/playground/run`; this endpoint is only registered in demo mode, the data source is fixed as `ds-demo-pg`/`ds-demo-mysql`, and the identity is fixed as `ro`/`dml`. The front end does not receive, cache, or allow entry of the demo Agent key. Non-demo deployments do not have this endpoint and the Live control, and the Playground continues to use static evaluation.

The six boot cards are demonstrated respectively:

1. PostgreSQL read-only queries are released normally, returning up to 5 rows and writing audits.
2. MySQL's UPDATE without WHERE is intercepted by R002 and the demo read-only barrier, and does not touch the business database.
3. MySQL queries the phone/email of customers and returns all masked values.
4. PostgreSQL large result scan hits R005, displays up to 20 rows and marks truncation.
5. PostgreSQL access to internal_notes is not authorized, denied by R010 and no content is returned.
6. Use the most recent `audit_id` of this session to open the audit details and jump to the overview live stream to review.

See `docs/images/demo-scenario-1.png` to `docs/images/demo-scenario-6.png` for actual demo screenshots of the six scripts (see also `docs/images/demo-scenario-cards.png` with console overview and audit page screenshots).

## Reset daily at 04:00

There is no timer within the app. Linux crontab example (`%` must be escaped in crontab):

```cron
0 4 * * * cd /opt/agentsql && DEMO_ANCHOR_DATE=$(date -u +\%F) bash ./demo/reset.sh >> /var/log/agentsql-demo-reset.log 2>&1
```

Example of task scheduler in Windows administrator terminal; the script takes today's UTC each time it is run:

```powershell
schtasks /Create /SC DAILY /ST 04:00 /TN "AgentSQL Demo Reset" /TR "powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\agentsql\demo\reset.ps1" /F
```

## Public deployment security checklist

- Replace SECRET, admin password, database owner/root/read-only password, and two demo keys; do not reuse environment samples.
- Do not set `AGENTSQL_INSECURE` and do not bring public sample values ​​to the public network.
- Delete the PostgreSQL/MySQL host `ports`, or continue to bind only controlled loopback/private network interfaces; never directly expose the business database.
- Keep `demo.enabled: true`, fixed data source whitelist, low QPS, and visible banner; banner cannot be turned off.
- Only access these two synthesis demo libraries. Do not modify the manifest to point to the real host, and do not import real, personal, or production data.
- Add TLS, access control and flow limiting at the reverse proxy layer; confirm that `/healthz` does not leak passwords, DSN or `asql_` key.
- The database read-only account is the last line of defense; before going online, it was tested that its INSERT, UPDATE, DELETE, TRUNCATE and DDL were all rejected.

## Troubleshooting

### There is no change in data after modifying SQL

Official database mirroring only executes `/docker-entrypoint-initdb.d` when an empty data volume is first started. You must run reset after modifying the initialization file, or execute it explicitly:

```bash
docker compose -p agentsql-demo -f docker-compose.demo.yml --env-file demo/demo.env down -v --remove-orphans
```

This will delete all demo data and then `up`.

### The seed is not completed or the gateway is not started.

View the logs for `demo-postgres`, `demo-mysql`, and the one-time `demo-seed`. The anchor is missing/illegal, the read-only password does not comply with ASCII constraints, the database is unhealthy, the manifest is inconsistent with the configuration banner, all will fail-closed; `agentsql-demo` will only start after the seed exits successfully.

### Port occupied

Modify `DEMO_PG_PORT`, `DEMO_MYSQL_PORT` or `DEMO_GATEWAY_PORT` at `demo/demo.env`. Also check common conflict ports `5432`, `3306`, `7780`, `55432`, `55433`; the default demonstration gateway uses `17880` to avoid conflicts with `7780` of ordinary gateways.

### Verify health and read-only permissions

reset will check`/healthz`middle`demo.enabled=true`、`/readyz`, seed fixed distribution and fixed count of two databases and four tables. If any step fails, the stage will be reported to be non-zero exit. After deployment, they should also be`agentsql_demo_ro`Log in to both databases and confirm that the SELECT is successful and that all writes and DDL are rejected by the database.
