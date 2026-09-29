#!/usr/bin/env bash
# Assemble the Live Demo deployment directory from the repository sources.
#
# Run from the deploy/ecs-demo directory inside a checkout of the repo:
#   bash prepare.sh
#
# It copies the fixed demo config/manifest and the database init scripts next
# to docker-compose.yml so the bind mounts resolve. It does not create
# credentials; copy .env.example to .env and edit it first.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Repo root: deploy/ecs-demo -> up two levels.
repo_root="$(cd "${here}/../.." && pwd)"

echo "repo root: ${repo_root}"

# Remove any stale files or directories (Docker may have created empty
# directories at bind-mount points if the stack was started before prepare).
rm -rf "${here}/config.demo.yaml" "${here}/demo-seed.yaml"
cp -f "${repo_root}/examples/docker/config.demo.yaml" "${here}/config.demo.yaml"
cp -f "${repo_root}/examples/docker/demo-seed.yaml" "${here}/demo-seed.yaml"

rm -rf "${here}/postgres" "${here}/mysql"
cp -R "${repo_root}/demo/postgres" "${here}/postgres"
cp -R "${repo_root}/demo/mysql" "${here}/mysql"

echo "Prepared demo files in ${here}"
echo "Next: cp .env.example .env  # edit values, then docker compose up -d postgres mysql"
