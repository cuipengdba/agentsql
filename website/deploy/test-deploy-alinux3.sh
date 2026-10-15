#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
deploy_script="$script_dir/deploy-alinux3.sh"
bash -n "$deploy_script"

line_of() {
  local needle=$1
  grep -nFx -- "$needle" "$deploy_script" | head -n 1 | cut -d: -f1
}

assert_before() {
  local first second
  first=$(line_of "$1")
  second=$(line_of "$2")
  [[ -n $first && -n $second && $first -lt $second ]] || {
    echo "发布顺序不安全: $1 必须先于 $2" >&2
    exit 1
  }
}

# A copied release must be relabeled and validated before the public symlink changes.
assert_before '    restorecon -Rv "$release_dir"' '  atomic_current_link "$release_dir"'
assert_before '  caddy validate --config /etc/caddy/Caddyfile' '  atomic_current_link "$release_dir"'
assert_before '  trap rollback_content_only EXIT' '  atomic_current_link "$release_dir"'
assert_before '  atomic_current_link "$release_dir"' '  systemctl reload caddy'

# Exercise the rollback code with command shims, so this test needs no root or server.
functions=$(sed -n '/^  atomic_current_link() {/,/^  }/p; /^  rollback_content_only() {/,/^  }/p' "$deploy_script")
[[ $functions == *'atomic_current_link()'* && $functions == *'rollback_content_only()'* ]]

if trace=$(
  set +e
  SITE_DIR=/isolated-site
  release_stamp=20261016T000000Z
  previous_release=/old-release
  ln() { printf 'ln:%s\n' "$3"; }
  mv() { printf 'mv:%s\n' "$4"; }
  readlink() { printf '/new-release\n'; }
  chown() { printf 'chown\n'; }
  systemctl() { printf 'reload\n'; }
  eval "$functions"
  false
  rollback_content_only
  ) 2>/dev/null; then
  echo '失败的发布被错误地报告为成功。' >&2
  exit 1
else
  status=$?
  [[ $status -eq 1 ]] || { echo "回滚返回状态不正确: $status" >&2; exit 1; }
fi
[[ $trace == *$'ln:/old-release\nmv:/isolated-site/current\nchown\nreload' ]] || {
  echo "失败后未恢复旧链接并 reload: $trace" >&2
  exit 1
}

echo 'PASS content-only relabel, validation order and failure rollback'
