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
assert_before '  flock -x -w 120 "$deploy_lock_fd" || { echo "阶段 B 内容发布锁超时。" >&2; exit 1; }' '  previous_release=$(readlink -f -- "$SITE_DIR/current")'
assert_before '  assert_current_matches_expected "$previous_release" "$expected_current"' '  atomic_current_link "$release_dir"'

preflight_function=$(sed -n '/^assert_current_matches_expected() {/,/^}/p' "$deploy_script")
[[ $preflight_function == *'assert_current_matches_expected()'* ]]
(
  eval "$preflight_function"
  assert_current_matches_expected /old-release /old-release
) || { echo '相同 current 被错误地拒绝。' >&2; exit 1; }
if preflight_trace=$(
  eval "$preflight_function"
  assert_current_matches_expected /third-party-release /old-release 2>&1
); then
  echo '预检后的 current 漂移被错误地接受。' >&2
  exit 1
fi
[[ $preflight_trace == *'拒绝覆盖其他发布'* ]] || {
  echo "预检漂移缺少拒绝信息: $preflight_trace" >&2
  exit 1
}

# Exercise the rollback code with command shims, so this test needs no root or server.
functions=$(sed -n '/^  atomic_current_link() {/,/^  }/p; /^  rollback_content_only() {/,/^  }/p' "$deploy_script")
[[ $functions == *'atomic_current_link()'* && $functions == *'rollback_content_only()'* ]]

if trace=$(
  set +e
  SITE_DIR=/isolated-site
  release_stamp=20261016T000000Z
  release_dir=/new-release
  previous_release=/old-release
  ln() { printf 'ln:%s\n' "$3"; }
  mv() { printf 'mv:%s\n' "$4"; }
  readlink() { printf '/new-release\n'; }
  chown() { printf 'chown\n'; }
  systemctl() { printf 'reload\n'; }
  eval "$functions"
  false
  rollback_content_only 2>/dev/null
  ); then
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

# If another release owns current, rollback must leave it untouched.
if drift_trace=$(
  set +e
  SITE_DIR=/isolated-site
  release_stamp=20261016T000001Z
  release_dir=/new-release
  previous_release=/old-release
  ln() { printf 'unexpected ln\n'; }
  mv() { printf 'unexpected mv\n'; }
  readlink() { printf '/third-party-release\n'; }
  chown() { printf 'unexpected chown\n'; }
  systemctl() { printf 'unexpected reload\n'; }
  eval "$functions"
  false
  rollback_content_only 2>&1
  ); then
  echo '并发漂移被错误地报告为成功。' >&2
  exit 1
else
  status=$?
  [[ $status -eq 1 ]] || { echo "并发漂移返回状态不正确: $status" >&2; exit 1; }
fi
[[ $drift_trace == *'拒绝覆盖其他发布'* && $drift_trace != *'unexpected'* ]] || {
  echo "并发漂移时错误地覆盖了其他发布: $drift_trace" >&2
  exit 1
}

echo 'PASS content-only relabel, validation order, failure rollback and concurrent drift refusal'
