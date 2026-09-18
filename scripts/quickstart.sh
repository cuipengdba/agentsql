#!/bin/sh

set -eu

GITHUB_REPO="cuipengdba/agentsql"
IMAGE_REPO="ghcr.io/cuipengdba/agentsql"
VERSION=
CONTAINER_NAME=agentsql
PORT_SPEC=127.0.0.1:7780
BUILD_LOCAL=0
SHOW_PASSWORD=0
ENV_CREATED=0
GENERATED_PASSWORD=
CREATED_CONTAINER=0

say() {
  printf '%s\n' "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
Usage: quickstart.sh [--version vX.Y.Z] [--name NAME] [--port PORT|127.0.0.1:PORT] [--build] [--show-password]
EOF
  exit 2
}

is_version() {
  printf '%s\n' "$1" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' >/dev/null 2>&1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || usage
      VERSION=$2
      shift 2
      ;;
    --name)
      [ "$#" -ge 2 ] || usage
      CONTAINER_NAME=$2
      shift 2
      ;;
    --port)
      [ "$#" -ge 2 ] || usage
      qs_port_arg=$2
      case "$qs_port_arg" in
        127.0.0.1:*) qs_port=${qs_port_arg#127.0.0.1:} ;;
        *:*) die "--port may bind only to 127.0.0.1." ;;
        *) qs_port=$qs_port_arg ;;
      esac
      printf '%s\n' "$qs_port" | grep -E '^[0-9]+$' >/dev/null 2>&1 || die "--port must be a local TCP port or 127.0.0.1:PORT."
      qs_port_normalized=$(printf '%s\n' "$qs_port" | sed 's/^0*//')
      [ -n "$qs_port_normalized" ] || qs_port_normalized=0
      [ "$qs_port_normalized" -ge 1 ] && [ "$qs_port_normalized" -le 65535 ] || die "--port must be between 1 and 65535."
      PORT_SPEC="127.0.0.1:$qs_port_normalized"
      shift 2
      ;;
    --build)
      BUILD_LOCAL=1
      shift
      ;;
    --show-password)
      SHOW_PASSWORD=1
      shift
      ;;
    *) usage ;;
  esac
done

printf '%s\n' "$CONTAINER_NAME" | grep -E '^[A-Za-z0-9][A-Za-z0-9_.-]*$' >/dev/null 2>&1 || die "Container name contains unsupported characters."
command -v docker >/dev/null 2>&1 || die "Docker CLI is required. Install Docker and retry."
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable or the current user cannot access it."

qs_arch=$(uname -m)
case "$qs_arch" in
  x86_64|amd64) ;;
  *) die "This quickstart supports amd64 hosts only. ARM64 and other hosts require explicit x86 emulation and are not supported by this script." ;;
esac

if [ -z "$VERSION" ]; then
  command -v curl >/dev/null 2>&1 || die "curl is required to resolve the latest GitHub Release."
  qs_latest=$(curl -fsSLo /dev/null -w '%{url_effective}' "https://github.com/${GITHUB_REPO}/releases/latest") || die "Could not resolve the latest GitHub Release."
  qs_prefix="https://github.com/${GITHUB_REPO}/releases/tag/"
  case "$qs_latest" in
    "${qs_prefix}"*) VERSION=${qs_latest#"$qs_prefix"} ;;
    *) die "GitHub latest redirected to an unexpected URL: $qs_latest" ;;
  esac
  is_version "$VERSION" || die "GitHub latest did not resolve to a stable vX.Y.Z release."
  [ "$qs_latest" = "${qs_prefix}${VERSION}" ] || die "GitHub latest URL contains unexpected trailing data."
else
  is_version "$VERSION" || die "Version must match vX.Y.Z exactly."
fi

IMAGE="${IMAGE_REPO}:${VERSION}"
if [ "$BUILD_LOCAL" -eq 1 ]; then
  [ -f Dockerfile ] && [ -f go.mod ] && [ -f scripts/install.sh ] && [ -f scripts/quickstart.sh ] || die "--build requires the complete AgentSQL source tree as the current directory."
  grep -F 'module github.com/cuipengdba/agentsql' go.mod >/dev/null 2>&1 || die "The current source tree is not github.com/cuipengdba/agentsql."
  qs_source_version=$(sed -n 's/^VERSION ?= \(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$/\1/p' Makefile | sed -n '1p')
  [ "$qs_source_version" = "$VERSION" ] || die "Source tree default version '$qs_source_version' does not match requested version '$VERSION'."
  docker build --platform linux/amd64 --build-arg "VERSION=$VERSION" -t "$IMAGE" . || die "Docker build failed."
else
  if ! docker pull "$IMAGE"; then
    die "Could not pull $IMAGE. Check network access, GHCR package visibility, authentication, and whether the tag exists. No source build was attempted."
  fi
fi

qs_image_arch=$(docker image inspect --format '{{.Architecture}}' "$IMAGE" 2>/dev/null) || die "Could not inspect image $IMAGE."
[ "$qs_image_arch" = amd64 ] || die "Image $IMAGE has architecture '$qs_image_arch', expected amd64."
qs_desired_image_id=$(docker image inspect --format '{{.Id}}' "$IMAGE")

qs_container_exists=0
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  qs_container_exists=1
  qs_managed=$(docker container inspect --format '{{ index .Config.Labels "io.agentsql.quickstart" }}' "$CONTAINER_NAME" 2>/dev/null || true)
  [ "$qs_managed" = 1 ] || die "Container '$CONTAINER_NAME' exists but is not managed by this quickstart script."
  qs_existing_image=$(docker container inspect --format '{{.Config.Image}}' "$CONTAINER_NAME")
  qs_existing_image_id=$(docker container inspect --format '{{.Image}}' "$CONTAINER_NAME")
  qs_existing_health=$(docker container inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER_NAME")
  qs_existing_port=$(docker container inspect --format '{{ index .Config.Labels "io.agentsql.port" }}' "$CONTAINER_NAME" 2>/dev/null || true)
  if [ "$qs_existing_image" = "$IMAGE" ] && [ "$qs_existing_image_id" = "$qs_desired_image_id" ] && [ "$qs_existing_health" = healthy ] && [ "$qs_existing_port" = "$PORT_SPEC" ]; then
    say "AgentSQL $VERSION is already healthy at http://$PORT_SPEC"
    exit 0
  fi
fi

validate_env_file() {
  vef_file=$1
  [ ! -L "$vef_file" ] || die "$vef_file must not be a symbolic link."
  for vef_key in AGENTSQL_SECRET AGENTSQL_ADMIN_USER AGENTSQL_ADMIN_PASSWORD; do
    vef_count=$(grep -c "^${vef_key}=" "$vef_file" || true)
    [ "$vef_count" -eq 1 ] || die "$vef_file must contain exactly one $vef_key entry."
  done
}

generate_random_value() {
  grv_bytes=$1
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$grv_bytes" | tr -d '\r\n'
  else
    command -v base64 >/dev/null 2>&1 || die "openssl or base64 is required to generate credentials."
    head -c "$grv_bytes" /dev/urandom | base64 | tr -d '\r\n'
  fi
}

if [ -e .env ]; then
  [ -f .env ] || die ".env exists but is not a regular file."
  validate_env_file .env
  chmod 0600 .env
else
  [ "$qs_container_exists" -eq 0 ] || die "The managed container needs replacement, but .env is missing. Restore the original .env before recreating it."
  qs_secret=$(generate_random_value 24)
  GENERATED_PASSWORD=$(generate_random_value 18)
  [ "$(printf '%s' "$qs_secret" | wc -c | tr -d ' ')" -eq 32 ] || die "Generated AGENTSQL_SECRET did not contain exactly 32 ASCII bytes."
  [ "$(printf '%s' "$GENERATED_PASSWORD" | wc -c | tr -d ' ')" -ge 12 ] || die "Generated administrator password was unexpectedly short."
  qs_old_umask=$(umask)
  umask 077
  qs_env_tmp=".env.new.$$"
  trap 'rm -f ".env.new.$$"' EXIT HUP INT TERM
  printf 'AGENTSQL_SECRET=%s\nAGENTSQL_ADMIN_USER=admin\nAGENTSQL_ADMIN_PASSWORD=%s\n' "$qs_secret" "$GENERATED_PASSWORD" > "$qs_env_tmp"
  chmod 0600 "$qs_env_tmp"
  mv "$qs_env_tmp" .env
  umask "$qs_old_umask"
  trap - EXIT HUP INT TERM
  ENV_CREATED=1
  qs_secret=
fi

if [ "$qs_container_exists" -eq 1 ]; then
  docker rm -f "$CONTAINER_NAME" >/dev/null || die "Could not replace managed container '$CONTAINER_NAME'."
fi

if ! docker run -d \
  --name "$CONTAINER_NAME" \
  --label io.agentsql.quickstart=1 \
  --label "io.agentsql.version=$VERSION" \
  --label "io.agentsql.port=$PORT_SPEC" \
  --restart unless-stopped \
  --security-opt no-new-privileges:true \
  -p "${PORT_SPEC}:7780" \
  --env-file .env \
  -v agentsql-data:/var/lib/agentsql \
  "$IMAGE" >/dev/null; then
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  die "Docker could not create the AgentSQL container. The .env file and agentsql-data volume were preserved."
fi
CREATED_CONTAINER=1
cleanup_created_container() {
  qcc_status=$?
  trap - EXIT HUP INT TERM
  if [ "$CREATED_CONTAINER" -eq 1 ]; then
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  fi
  exit "$qcc_status"
}
trap cleanup_created_container EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

qs_attempt=0
qs_health=
while [ "$qs_attempt" -lt 40 ]; do
  qs_health=$(docker container inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER_NAME" 2>/dev/null || true)
  [ "$qs_health" = healthy ] && break
  [ "$qs_health" = unhealthy ] && break
  qs_attempt=$((qs_attempt + 1))
  sleep 2
done

if [ "$qs_health" != healthy ]; then
  die "Container health verification failed (status: $qs_health). The container was removed; .env and agentsql-data were preserved. Inspect the image configuration and retry locally without sharing secrets."
fi

CREATED_CONTAINER=0
trap - EXIT HUP INT TERM
say "AgentSQL $VERSION is healthy at http://$PORT_SPEC"
if [ "$ENV_CREATED" -eq 1 ]; then
  if [ "$SHOW_PASSWORD" -eq 1 ] || [ -t 1 ]; then
    say "Automatically generated administrator password: $GENERATED_PASSWORD"
    say "Credentials are stored in .env with mode 0600."
  else
    say "Administrator credentials were generated but are not displayed on non-interactive output. Inspect .env locally."
  fi
fi
say "The service is bound to loopback only. Use SSH local forwarding for remote access."
GENERATED_PASSWORD=
