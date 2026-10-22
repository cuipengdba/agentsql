#!/usr/bin/env bash
set -euo pipefail

SITE_DIR=/var/www/agentsql
WEB_USER=caddy
stage=
src=
content_only=0
expected_current=

usage() {
  echo "用法: $0 --stage <a|b> --src <本地 public 目录或包含 public/ 的 release 目录> [--content-only] [--expected-current <绝对 release 路径>]" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stage)
      [[ $# -ge 2 ]] || usage
      stage=$2
      shift 2
      ;;
    --src)
      [[ $# -ge 2 ]] || usage
      src=$2
      shift 2
      ;;
    --content-only)
      content_only=1
      shift
      ;;
    --expected-current)
      [[ $# -ge 2 ]] || usage
      expected_current=$2
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "未知参数: $1" >&2
      usage
      ;;
  esac
done

[[ $stage == a || $stage == b ]] || usage
[[ -n $src ]] || usage
if [[ $content_only -eq 1 && $stage != b ]]; then
  echo "--content-only 仅用于阶段 B 日常内容发布，请使用 --stage b --content-only。" >&2
  exit 2
fi
if [[ -n $expected_current ]]; then
  if [[ $content_only -ne 1 || ! $expected_current =~ ^/var/www/agentsql/releases/[0-9]{8}T[0-9]{6}Z$ ]]; then
    echo "--expected-current 仅接受阶段 B 内容发布的绝对 release 路径。" >&2
    exit 2
  fi
fi

if [[ ${EUID} -ne 0 ]]; then
  echo "请以 root 运行此脚本。" >&2
  exit 1
fi

if [[ $content_only -eq 1 ]]; then
  # Serialize content-only deployments before reading current and keep the
  # lock until any failure rollback has finished.
  [[ -d $SITE_DIR ]] || { echo "阶段 B 内容发布目录不存在: $SITE_DIR" >&2; exit 1; }
  command -v flock >/dev/null 2>&1 || { echo "阶段 B 内容发布要求 flock。" >&2; exit 1; }
  exec {deploy_lock_fd}>"$SITE_DIR/.deploy.lock"
  flock -x -w 120 "$deploy_lock_fd" || { echo "阶段 B 内容发布锁超时。" >&2; exit 1; }
fi

[[ -d $src ]] || { echo "输入目录不存在: $src" >&2; exit 1; }
input_dir=$(readlink -f -- "$src")
if [[ -f "$input_dir/index.html" ]]; then
  public_dir=$input_dir
elif [[ -f "$input_dir/public/index.html" ]]; then
  public_dir=$input_dir/public
else
  echo "输入目录中未找到 index.html 或 public/index.html。" >&2
  exit 1
fi

required=(index.html 404.html favicon.svg robots.txt sitemap.xml demo/index.html en/index.html en/demo/index.html assets .well-known/security.txt)
for item in "${required[@]}"; do
  if [[ ! -e "$public_dir/$item" ]]; then
    echo "缺少发布文件: $public_dir/$item" >&2
    exit 1
  fi
done

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

if [[ $content_only -eq 0 ]]; then
  # Alibaba Cloud Linux 3 / RHEL 8: prefer EPEL and use COPR only as a fallback.
  if command -v caddy >/dev/null 2>&1; then
    echo "Caddy 已安装，跳过软件包安装。"
  else
    if ! dnf install -y caddy; then
      echo "EPEL 未能安装 Caddy，回退到官方 COPR。"
      dnf install -y 'dnf-command(copr)'
      dnf copr enable -y @caddy/caddy
      dnf install -y caddy
    fi
  fi
else
  command -v caddy >/dev/null 2>&1 || { echo "阶段 B 内容发布要求 Caddy 已安装。" >&2; exit 1; }
fi
caddy version

previous_release=
assert_current_matches_expected() {
  local actual=$1 expected=$2
  if [[ -n $expected && $actual != "$expected" ]]; then
    echo "current 已从预检版本 $expected 漂移到 $actual；拒绝覆盖其他发布。" >&2
    return 1
  fi
}
if [[ $content_only -eq 1 ]]; then
  [[ -L "$SITE_DIR/current" ]] || { echo "阶段 B 内容发布要求 current 指向现有版本。" >&2; exit 1; }
  previous_release=$(readlink -f -- "$SITE_DIR/current")
  [[ -f "$previous_release/public/index.html" ]] || { echo "现有版本不完整: $previous_release" >&2; exit 1; }
  assert_current_matches_expected "$previous_release" "$expected_current"
fi

install -d -o root -g root -m 0755 "$SITE_DIR" "$SITE_DIR/releases"
release_stamp=$(date -u +%Y%m%dT%H%M%SZ)
release_dir="$SITE_DIR/releases/$release_stamp"
release_public="$release_dir/public"
[[ ! -e "$release_dir" && ! -L "$release_dir" ]] || { echo "发布目录已存在: $release_dir" >&2; exit 1; }
install -d -o root -g root -m 0755 "$release_public"

# Publish only the explicit public allowlist. Never copy deploy/, .git/ or a release root wholesale.
install -m 0644 "$public_dir/index.html" "$release_public/index.html"
install -m 0644 "$public_dir/404.html" "$release_public/404.html"
install -m 0644 "$public_dir/favicon.svg" "$release_public/favicon.svg"
install -m 0644 "$public_dir/robots.txt" "$release_public/robots.txt"
install -m 0644 "$public_dir/sitemap.xml" "$release_public/sitemap.xml"
cp -a -- "$public_dir/assets" "$release_public/assets"
cp -a -- "$public_dir/demo" "$release_public/demo"
cp -a -- "$public_dir/en" "$release_public/en"
cp -a -- "$public_dir/.well-known" "$release_public/.well-known"
chown -R root:root "$release_dir"
find "$release_dir" -type d -exec chmod 0755 {} +
find "$release_dir" -type f -exec chmod 0644 {} +

bash "$script_dir/verify-static.sh" "$release_public"

if [[ -e "$SITE_DIR/current" && ! -L "$SITE_DIR/current" ]]; then
  echo "拒绝覆盖非符号链接: $SITE_DIR/current" >&2
  exit 1
fi

if [[ $content_only -eq 1 ]]; then
  # cp -a can preserve the staging directory's SELinux label; relabel before exposing the release.
  if command -v getenforce >/dev/null 2>&1 && [[ $(getenforce) != Disabled ]]; then
    command -v restorecon >/dev/null 2>&1 || { echo "SELinux 已启用，但缺少 restorecon。" >&2; exit 1; }
    command -v matchpathcon >/dev/null 2>&1 || { echo "SELinux 已启用，但缺少 matchpathcon。" >&2; exit 1; }
    expected_context=$(matchpathcon -n "$release_public/index.html")
    [[ $expected_context == *:httpd_sys_content_t:* ]] || { echo "官网目录缺少 httpd_sys_content_t 文件上下文规则。" >&2; exit 1; }
    restorecon -Rv "$release_dir"
  fi

  # Validate before the switch, then use a same-directory rename for an atomic current update.
  caddy validate --config /etc/caddy/Caddyfile

  atomic_current_link() {
    local target=$1 pending="$SITE_DIR/.current-$release_stamp-$$-$RANDOM"
    ln -s -- "$target" "$pending" || return 1
    if ! mv -Tf -- "$pending" "$SITE_DIR/current"; then
      rm -f -- "$pending" || true
      return 1
    fi
  }

  rollback_content_only() {
    local status=$? active
    trap - EXIT HUP INT TERM
    if [[ $status -ne 0 ]]; then
      active=$(readlink -f -- "$SITE_DIR/current" 2>/dev/null || true)
      if [[ $active == "$release_dir" ]]; then
        if atomic_current_link "$previous_release"; then
          chown -h root:root "$SITE_DIR/current" || true
          echo "内容发布失败，已恢复原站点版本: $previous_release" >&2
          systemctl reload caddy || echo "Caddy 回滚 reload 失败，请检查服务状态。" >&2
        else
          echo "内容发布失败，current 自动回滚失败，请手动恢复: $previous_release" >&2
        fi
      elif [[ $active != "$previous_release" ]]; then
        echo "内容发布失败，但 current 已指向其他版本 $active；拒绝覆盖其他发布，请人工检查。" >&2
      fi
    fi
    exit "$status"
  }

  trap rollback_content_only EXIT
  trap 'exit 1' HUP INT TERM
  assert_current_matches_expected "$(readlink -f -- "$SITE_DIR/current")" "$previous_release"
  atomic_current_link "$release_dir"
  chown -h root:root "$SITE_DIR/current"
  caddy validate --config /etc/caddy/Caddyfile
  systemctl reload caddy
  trap - EXIT HUP INT TERM
  echo "阶段 B 内容发布完成（未修改 /etc/caddy/Caddyfile）: $release_dir"
  exit 0
fi

# Initial stage deployment retains its existing install and service setup flow.
ln -sfn "$release_dir" "$SITE_DIR/current"
chown -h root:root "$SITE_DIR/current"

install -d -o root -g root -m 0755 /etc/caddy
install -m 0644 "$script_dir/Caddyfile.common" /etc/caddy/Caddyfile.common
install -m 0644 "$script_dir/Caddyfile.stage-$stage" /etc/caddy/Caddyfile
install -d -o root -g root -m 0755 /etc/systemd/system/caddy.service.d
install -m 0644 "$script_dir/systemd/caddy.service.d/hardening.conf" /etc/systemd/system/caddy.service.d/hardening.conf
install -d -o "$WEB_USER" -g "$WEB_USER" -m 0750 /var/log/caddy /var/lib/caddy

if command -v getenforce >/dev/null 2>&1 && [[ $(getenforce) != Disabled ]]; then
  if ! command -v semanage >/dev/null 2>&1; then
    dnf install -y policycoreutils-python-utils
  fi
  # -a handles first install; -m makes repeated deployments idempotent when the rule exists.
  semanage fcontext -a -t httpd_sys_content_t '/var/www/agentsql(/.*)?' 2>/dev/null || \
    semanage fcontext -m -t httpd_sys_content_t '/var/www/agentsql(/.*)?'
  restorecon -Rv "$SITE_DIR"
fi

caddy validate --config /etc/caddy/Caddyfile
systemctl daemon-reload
systemctl enable --now caddy
systemctl reload-or-restart caddy

if [[ $stage == a ]]; then
  echo "阶段 A 部署完成: $release_dir"
  echo "本机自测: curl -I http://127.0.0.1:8080/"
else
  echo "阶段 B 首次切换完成: $release_dir"
  echo "域名自测: curl -I https://agentsql.cn/"
fi
