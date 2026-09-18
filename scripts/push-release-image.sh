#!/bin/sh

set -eu

IMAGE_REPO="ghcr.io/cuipengdba/agentsql"
VERSION=
PUSH_LATEST=0

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  printf 'Usage: push-release-image.sh vX.Y.Z [--latest]\n' >&2
  exit 2
}

[ "$#" -ge 1 ] || usage
VERSION=$1
shift
printf '%s\n' "$VERSION" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' >/dev/null 2>&1 || die "VERSION must match vX.Y.Z exactly; prerelease tags are not accepted."
while [ "$#" -gt 0 ]; do
  case "$1" in
    --latest) PUSH_LATEST=1 ;;
    *) usage ;;
  esac
  shift
done

command -v docker >/dev/null 2>&1 || die "Docker CLI is required."
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable or the current user cannot access it."
docker buildx version >/dev/null 2>&1 || die "Docker buildx is required."

pri_home=${HOME:-}
[ -n "${DOCKER_CONFIG:-}" ] || [ -n "$pri_home" ] || die "Neither DOCKER_CONFIG nor HOME is set; Docker credential configuration cannot be located."
pri_config_dir=${DOCKER_CONFIG:-"$pri_home/.docker"}
pri_config_file="$pri_config_dir/config.json"
if [ ! -f "$pri_config_file" ] || ! grep -F 'ghcr.io' "$pri_config_file" >/dev/null 2>&1; then
  die "No GHCR login entry was found. Run 'docker login ghcr.io' as the publishing user, then retry."
fi

set -- docker buildx build --platform linux/amd64 --build-arg "VERSION=$VERSION" -t "${IMAGE_REPO}:${VERSION}"
if [ "$PUSH_LATEST" -eq 1 ]; then
  set -- "$@" -t "${IMAGE_REPO}:latest"
fi
set -- "$@" --push .
"$@"

printf 'Published %s:%s\n' "$IMAGE_REPO" "$VERSION"
if [ "$PUSH_LATEST" -eq 1 ]; then
  printf 'Published %s:latest by explicit request.\n' "$IMAGE_REPO"
fi
cat <<EOF
Manual release checklist:
  1. Set the GHCR package visibility to public.
  2. From a logged-out environment, run: docker pull ${IMAGE_REPO}:${VERSION}
  3. Confirm the pulled image architecture is amd64.
  4. Start it with loopback-only port publishing and confirm container health.
  5. Publish the GitHub Release only after all checks pass.
EOF
