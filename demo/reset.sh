#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/docker-compose.demo.yml"
ENV_FILE="${AGENTSQL_DEMO_ENV_FILE:-$SCRIPT_DIR/demo.env}"
PROJECT_NAME="agentsql-demo"
export COMPOSE_PROJECT_NAME="$PROJECT_NAME"
STAGE="startup"

trap 'code=$?; printf "[demo-reset] FAILED stage=%s exit=%s\n" "$STAGE" "$code" >&2' EXIT

if [[ ! -f "$ENV_FILE" ]]; then
  printf 'Missing %s\nCopy examples/docker/demo.env.example to demo/demo.env and replace every credential.\n' "$ENV_FILE" >&2
  exit 1
fi

# Load the local env file for host-side port selection. An already exported
# DEMO_ANCHOR_DATE wins, which is useful for cron and reproducible acceptance.
exported_anchor="${DEMO_ANCHOR_DATE-}"
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
if [[ -n "$exported_anchor" ]]; then
  DEMO_ANCHOR_DATE="$exported_anchor"
fi
if [[ -z "${DEMO_ANCHOR_DATE:-}" ]]; then
  DEMO_ANCHOR_DATE="$(date -u +%F)"
fi
case "$DEMO_ANCHOR_DATE" in
  [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
  *) printf 'DEMO_ANCHOR_DATE must use YYYY-MM-DD\n' >&2; exit 1 ;;
esac
export DEMO_ANCHOR_DATE
DEMO_GATEWAY_PORT="${DEMO_GATEWAY_PORT:-17880}"

compose() {
  docker compose --project-name "$PROJECT_NAME" --file "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@"
}

require_line() {
  local output="$1" expected="$2"
  if ! grep -Fqx "$expected" <<<"$output"; then
    printf 'Expected count %s, got:\n%s\n' "$expected" "$output" >&2
    return 1
  fi
}

STAGE="remove-old-stack-and-volumes"
printf '[demo-reset] %s (anchor=%s)\n' "$STAGE" "$DEMO_ANCHOR_DATE"
compose down -v --remove-orphans

STAGE="build-and-start"
printf '[demo-reset] %s\n' "$STAGE"
compose up -d --build --wait

STAGE="http-health"
printf '[demo-reset] %s\n' "$STAGE"
health_body="$(curl --fail --silent --show-error "http://127.0.0.1:${DEMO_GATEWAY_PORT}/healthz")"
grep -Fq '"status":"ok"' <<<"$health_body"
grep -Fq '"enabled":true' <<<"$health_body"
ready_body="$(curl --fail --silent --show-error "http://127.0.0.1:${DEMO_GATEWAY_PORT}/readyz")"
grep -Fq '"status":"ready"' <<<"$ready_body"

STAGE="verify-control-plane-seed"
printf '[demo-reset] %s\n' "$STAGE"
verify_output="$(compose run --rm --no-deps demo-seed \
  demo-seed \
  --config /etc/agentsql/config.demo.yaml \
  --manifest /etc/agentsql/demo-seed.yaml \
  --anchor-date "$DEMO_ANCHOR_DATE" \
  --verify-only)"
for expected in \
  DEMO_SEED_VERIFY_OK \
  datasources=2 \
  agents=2 \
  policies=10 \
  mask_rules=4 \
  audits=300 \
  approvals=24; do
  grep -Eq "(^| )${expected}( |$)" <<<"$verify_output"
done
# verify-only also validates allow/deny/warn/approve=180/60/36/24 and all 24
# approval audit references (including zero missing/orphaned references).

STAGE="verify-postgres-counts"
printf '[demo-reset] %s\n' "$STAGE"
pg_container="$(compose ps -q demo-postgres)"
[[ -n "$pg_container" ]]
# Count SQL is piped over stdin (demo/shared/*.sql) to avoid nested quoting.
pg_owner="${DEMO_PG_OWNER_USER:-agentsql_demo_owner}"
pg_counts="$(docker exec -i "$pg_container" psql -U "$pg_owner" -d agentsql_demo -tA < "$SCRIPT_DIR/shared/pg_counts.sql")"
require_line "$pg_counts" 'customers=128'
require_line "$pg_counts" 'products=64'
require_line "$pg_counts" 'orders=2400'
require_line "$pg_counts" 'internal_notes=16'

STAGE="verify-mysql-counts"
printf '[demo-reset] %s\n' "$STAGE"
mysql_container="$(compose ps -q demo-mysql)"
[[ -n "$mysql_container" ]]
mysql_counts="$(docker exec -i -e MYSQL_PWD="$DEMO_MYSQL_ROOT_PASSWORD" "$mysql_container" mysql -uroot --database agentsql_demo --batch --skip-column-names < "$SCRIPT_DIR/shared/mysql_counts.sql")"
require_line "$mysql_counts" 'customers=128'
require_line "$mysql_counts" 'products=64'
require_line "$mysql_counts" 'orders=2400'
require_line "$mysql_counts" 'internal_notes=16'

trap - EXIT
printf '[demo-reset] OK anchor=%s gateway=http://127.0.0.1:%s\n' "$DEMO_ANCHOR_DATE" "$DEMO_GATEWAY_PORT"
