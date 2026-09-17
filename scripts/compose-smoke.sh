#!/bin/sh
set -eu

# Config-only Compose regression check. It never starts containers, never
# touches the Live Demo, and does not require a running stack.

fail() {
  printf 'compose smoke: FAIL: %s\n' "$*" >&2
  exit 1
}

project_name=agentsql-smoke
secret=12345678901234567890123456789012
admin_password=agentsql-smoke-admin-password
metadata_password=agentsql-smoke-metadata-password
audit_password=agentsql-smoke-audit-password
metadata_dsn=postgres://agentsql_meta_migrator:agentsql-smoke-metadata-password@metadata-db:5432/agentsql_metadata
audit_dsn=postgres://agentsql_audit_migrator:agentsql-smoke-audit-password@audit-db:5432/agentsql_audit

base_config() {
  COMPOSE_PROJECT_NAME=$project_name \
  COMPOSE_PROFILES= \
  AGENTSQL_SECRET=$secret \
  AGENTSQL_ADMIN_PASSWORD=$admin_password \
    docker compose -f docker-compose.yml "$@"
}

controlplane_config() {
  COMPOSE_PROJECT_NAME=$project_name \
  COMPOSE_PROFILES= \
  AGENTSQL_SECRET=$secret \
  AGENTSQL_ADMIN_PASSWORD=$admin_password \
  AGENTSQL_METADATA_MIGRATION_PASSWORD=$metadata_password \
  AGENTSQL_AUDIT_MIGRATION_PASSWORD=$audit_password \
  AGENTSQL_STORE_METADATA_DSN=$metadata_dsn \
  AGENTSQL_STORE_AUDIT_DSN=$audit_dsn \
    docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane "$@"
}

base_config config --quiet || fail 'base file did not parse with only AGENTSQL_SECRET and AGENTSQL_ADMIN_PASSWORD'

base_services=$(base_config config --services) || fail 'could not list base services'
[ "$base_services" = agentsql ] || fail "base services must be exactly 'agentsql'; got: $base_services"

if missing_output=$(
  COMPOSE_PROJECT_NAME=$project_name \
  COMPOSE_PROFILES= \
  AGENTSQL_SECRET=$secret \
  AGENTSQL_ADMIN_PASSWORD=$admin_password \
  AGENTSQL_METADATA_MIGRATION_PASSWORD= \
  AGENTSQL_AUDIT_MIGRATION_PASSWORD= \
  AGENTSQL_STORE_METADATA_DSN= \
  AGENTSQL_STORE_AUDIT_DSN= \
    docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane config --quiet 2>&1
); then
  fail 'control-plane config unexpectedly accepted empty required variables'
fi

for variable_name in \
  AGENTSQL_METADATA_MIGRATION_PASSWORD \
  AGENTSQL_AUDIT_MIGRATION_PASSWORD \
  AGENTSQL_STORE_METADATA_DSN \
  AGENTSQL_STORE_AUDIT_DSN
do
  printf '%s\n' "$missing_output" | grep -F "$variable_name" >/dev/null || \
    fail "missing-variable error did not name $variable_name"
done

controlplane_config config --quiet || fail 'merged control-plane config did not parse with fake required values'

merged_services=$(controlplane_config config --services) || fail 'could not list merged services'
for required_service in agentsql metadata-db audit-db agentsql-controlplane
do
  printf '%s\n' "$merged_services" | grep -Fx "$required_service" >/dev/null || \
    fail "merged services are missing $required_service"
done

merged_volumes=$(controlplane_config config --volumes) || fail 'could not list merged volumes'
for required_volume in agentsql-metadata-pgdata agentsql-audit-pgdata
do
  printf '%s\n' "$merged_volumes" | grep -Fx "$required_volume" >/dev/null || \
    fail "merged volumes are missing $required_volume"
done

printf 'compose smoke: PASS\n'
