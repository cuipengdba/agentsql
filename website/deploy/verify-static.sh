#!/usr/bin/env bash
set -uo pipefail

BASE_URL=${1:-http://127.0.0.1:8080}
MODE=${2:-stage-a}
SITE_ROOT=${SITE_ROOT:-/var/www/agentsql/current/public}
local_only=0

if [[ ${1:-} == --root ]]; then
  [[ $# -ge 2 ]] || { echo "用法: $0 --root <public目录>" >&2; exit 2; }
  SITE_ROOT=$2
  local_only=1
elif [[ -d ${1:-} ]]; then
  SITE_ROOT=$1
  local_only=1
fi

[[ -d $SITE_ROOT ]] || { echo "静态站目录不存在: $SITE_ROOT" >&2; exit 2; }
SITE_ROOT=$(cd -- "$SITE_ROOT" && pwd)

pass_count=0
fail_count=0
tmp_dir=$(mktemp -d)
trap 'rm -rf -- "$tmp_dir"' EXIT

pass() { printf 'PASS  %s\n' "$1"; pass_count=$((pass_count + 1)); }
fail() { printf 'FAIL  %s\n' "$1"; fail_count=$((fail_count + 1)); }

fetch() {
  local url=$1 name=$2
  curl -ksS -D "$tmp_dir/$name.headers" -o "$tmp_dir/$name.body" -w '%{http_code}' "$url" 2>"$tmp_dir/$name.error" || true
}

if [[ $local_only -eq 0 ]]; then
  status=$(fetch "$BASE_URL/" home)
  [[ $status == 200 ]] && pass "首页返回 200" || fail "首页返回状态为 $status（期望 200）"

  status=$(fetch "$BASE_URL/not-found-for-static-check" missing)
  if [[ $status == 404 ]] && grep -q '页面不存在' "$tmp_dir/missing.body"; then pass "自定义 404 页面返回 404"; else fail "自定义 404 状态或内容不正确"; fi

  for path in '/.git/config' '/.env' '/deploy/Caddyfile'; do
    key=$(printf '%s' "$path" | tr '/.' '__')
    status=$(fetch "$BASE_URL$path" "$key")
    [[ $status == 404 ]] && pass "$path 被隐藏为 404" || fail "$path 返回 $status（期望 404）"
  done

  headers="$tmp_dir/home.headers"
  for spec in 'content-security-policy:' 'x-frame-options: *DENY' 'x-content-type-options: *nosniff' 'referrer-policy: *strict-origin-when-cross-origin'; do
    if grep -Eiq "^$spec" "$headers"; then pass "安全响应头存在: ${spec%%:*}"; else fail "缺少安全响应头: ${spec%%:*}"; fi
  done

  status=$(fetch "$BASE_URL/assets/" directory)
  if [[ $status != 200 ]] && ! grep -Eqi '<title>Index of|directory listing' "$tmp_dir/directory.body"; then pass "目录列举未启用"; else fail "assets 目录疑似可列举"; fi

  if [[ $MODE == --stage-b || $MODE == stage-b ]]; then
    status=$(curl -sS -o /dev/null -w '%{http_code}' http://agentsql.cn/ || true)
    if [[ $status == 301 || $status == 308 ]]; then pass "HTTP 跳转到 HTTPS"; else fail "HTTP 返回 $status（期望 301/308）"; fi
    status=$(fetch 'https://agentsql.cn/' stage_b_https)
    [[ $status == 200 ]] && pass "HTTPS 首页返回 200" || fail "HTTPS 首页返回 $status"
    if grep -Eiq '^strict-transport-security: *max-age=300' "$tmp_dir/stage_b_https.headers"; then pass "HSTS 初始 max-age=300"; else fail "缺少阶段 B HSTS"; fi
  else
    pass "阶段 A：跳过公网 HTTPS 与跳转检查"
  fi
else
  pass "本地模式：跳过 HTTP、TLS 与响应头检查"
fi

origin_fail=0
while IFS= read -r hit; do
  case "$hit" in
    https://agentsql.cn/*|https://github.com/*|https://beian.miit.gov.cn*|http://127.0.0.1:*|mailto:*) ;;
    *) printf '      非白名单引用: %s\n' "$hit"; origin_fail=1 ;;
  esac
done < <(grep -RhoE 'https?://[^"<>()[:space:]]+|mailto:[^"<>()[:space:]]+' "$SITE_ROOT"/*.html "$SITE_ROOT"/.well-known/security.txt 2>/dev/null | sort -u)
[[ $origin_fail -eq 0 ]] && pass "HTML 外部引用仅含同源、GitHub、备案站、回环地址与邮箱" || fail "发现非白名单外部引用"

if grep -RniE --include='*.html' --include='*.css' '@import|url\(|preconnect|<iframe' "$SITE_ROOT" >/dev/null; then
  fail "发现 @import、url()、preconnect 或 iframe"
else
  pass "未发现远程样式导入、预连接或 iframe"
fi

reference_fail=0
validate_reference() {
  local raw=$1 source_file=$2 label=$3 target path fragment candidate
  target=${raw//&amp;/&}
  if [[ -z $target ]]; then
    printf '      空引用: %s (%s)\n' "$label" "$source_file"
    reference_fail=1
    return
  fi
  case "$target" in
    data:*|mailto:*|tel:*) return ;;
    https://agentsql.cn/*) target=/${target#https://agentsql.cn/} ;;
    http://*|https://*) return ;;
    //*) printf '      协议相对引用不允许: %s\n' "$target"; reference_fail=1; return ;;
  esac

  fragment=
  if [[ $target == *#* ]]; then
    fragment=${target#*#}
    target=${target%%#*}
  fi
  target=${target%%\?*}

  if [[ -z $target ]]; then
    candidate=$source_file
  elif [[ $target == / ]]; then
    candidate="$SITE_ROOT/index.html"
  elif [[ $target == /* ]]; then
    path=${target#/}
    candidate="$SITE_ROOT/$path"
  else
    candidate="$(dirname -- "$source_file")/$target"
  fi
  [[ $candidate == */ ]] && candidate="${candidate}index.html"

  if [[ $candidate != "$SITE_ROOT"/* ]]; then
    printf '      引用越出站点根目录: %s (%s)\n' "$raw" "$source_file"
    reference_fail=1
    return
  fi
  if [[ ! -f $candidate ]]; then
    printf '      缺失引用: %s -> %s (%s)\n' "$raw" "$candidate" "$label"
    reference_fail=1
    return
  fi
  if [[ -n $fragment ]] && ! grep -Eq "[[:space:]]id=[\"']$fragment[\"']" "$candidate"; then
    printf '      缺失锚点: %s#%s (%s)\n' "$candidate" "$fragment" "$label"
    reference_fail=1
  fi
}

while IFS= read -r html_file; do
  while IFS= read -r attribute; do
    value=$(printf '%s\n' "$attribute" | sed -E 's/^[^=]+="(.*)"$/\1/')
    validate_reference "$value" "$html_file" "HTML ${attribute%%=*}"
  done < <(grep -oE '(href|src)="[^"]*"' "$html_file" || true)

  while IFS= read -r meta_line; do
    value=$(printf '%s\n' "$meta_line" | grep -oE 'content="[^"]*"' | head -n 1)
    value=$(printf '%s\n' "$value" | sed -E 's/^content="(.*)"$/\1/')
    validate_reference "$value" "$html_file" "分享图 meta"
  done < <(grep -Ei '<meta[^>]+(property|name)="(og:image|twitter:image)"' "$html_file" || true)
done < <(find "$SITE_ROOT" -maxdepth 1 -type f -name '*.html' -print)

for required_asset in assets/app.css assets/app.js assets/og-cover.png favicon.svg assets/apple-touch-icon.png; do
  [[ -f "$SITE_ROOT/$required_asset" ]] || { printf '      缺失核心资产: %s\n' "$required_asset"; reference_fail=1; }
done

if grep -Ehi '<(script|img)[^>]+src="https?://|<link[^>]+rel="(stylesheet|icon|apple-touch-icon)"[^>]+href="https?://' "$SITE_ROOT"/*.html >/dev/null; then
  printf '      发现外部运行时资源。\n'
  reference_fail=1
fi

[[ $reference_fail -eq 0 ]] && pass "同源 href/src、CSS/JS、分享图与图标引用完整且锚点有效" || fail "存在缺失、空白或外部运行时引用"

pdf_fail=0
for pdf in \
  assets/docs/agentsql-getting-started-v0.3.0.pdf \
  assets/docs/agentsql-user-guide-v0.3.0.pdf \
  assets/docs/agentsql-mcp-integrations-v0.3.0.pdf; do
  pdf_path="$SITE_ROOT/$pdf"
  if [[ ! -f $pdf_path ]]; then
    printf '      缺失 PDF: %s\n' "$pdf"
    pdf_fail=1
    continue
  fi
  if [[ $(head -c 5 "$pdf_path") != '%PDF-' ]]; then
    printf '      PDF 头无效: %s\n' "$pdf"
    pdf_fail=1
  fi
  if ! tail -c 4096 "$pdf_path" | grep -aq '%%EOF'; then
    printf '      PDF 尾无效: %s\n' "$pdf"
    pdf_fail=1
  fi
done
[[ $pdf_fail -eq 0 ]] && pass "三份版本化 PDF 均存在且包含 %PDF- 头与 %%EOF 尾" || fail "PDF 缺失或最小完整性检查失败"

printf '\nSUMMARY  PASS=%d  FAIL=%d\n' "$pass_count" "$fail_count"
[[ $fail_count -eq 0 ]]
