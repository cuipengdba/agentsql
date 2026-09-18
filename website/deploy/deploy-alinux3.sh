#!/usr/bin/env bash
set -euo pipefail

SITE_DIR=/var/www/agentsql
WEB_USER=caddy
stage=
src=
content_only=0

usage() {
  echo "用法: $0 --stage <a|b> --src <本地 public 目录或包含 public/ 的 release 目录> [--content-only]" >&2
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

if [[ ${EUID} -ne 0 ]]; then
  echo "请以 root 运行此脚本。" >&2
  exit 1
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

required=(index.html 404.html favicon.svg robots.txt sitemap.xml assets .well-known/security.txt)
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

install -d -o root -g root -m 0755 "$SITE_DIR" "$SITE_DIR/releases"
release_stamp=$(date -u +%Y%m%dT%H%M%SZ)
release_dir="$SITE_DIR/releases/$release_stamp"
release_public="$release_dir/public"
install -d -o root -g root -m 0755 "$release_public"

# Publish only the explicit public allowlist. Never copy deploy/, .git/ or a release root wholesale.
install -m 0644 "$public_dir/index.html" "$release_public/index.html"
install -m 0644 "$public_dir/404.html" "$release_public/404.html"
install -m 0644 "$public_dir/favicon.svg" "$release_public/favicon.svg"
install -m 0644 "$public_dir/robots.txt" "$release_public/robots.txt"
install -m 0644 "$public_dir/sitemap.xml" "$release_public/sitemap.xml"
cp -a -- "$public_dir/assets" "$release_public/assets"
cp -a -- "$public_dir/.well-known" "$release_public/.well-known"
chown -R root:root "$release_dir"
find "$release_dir" -type d -exec chmod 0755 {} +
find "$release_dir" -type f -exec chmod 0644 {} +

bash "$script_dir/verify-static.sh" "$release_public"

# GNU ln replaces the current release symlink without touching prior releases.
if [[ -e "$SITE_DIR/current" && ! -L "$SITE_DIR/current" ]]; then
  echo "拒绝覆盖非符号链接: $SITE_DIR/current" >&2
  exit 1
fi
ln -sfn "$release_dir" "$SITE_DIR/current"
chown -h root:root "$SITE_DIR/current"

if [[ $content_only -eq 1 ]]; then
  caddy validate --config /etc/caddy/Caddyfile
  systemctl reload caddy
  echo "阶段 B 内容发布完成（未修改 /etc/caddy/Caddyfile）: $release_dir"
  exit 0
fi

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
