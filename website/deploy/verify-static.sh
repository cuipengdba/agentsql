#!/usr/bin/env bash
set -u

BASE_URL=${1:-http://127.0.0.1:8080}
MODE=${2:-stage-a}
SITE_ROOT=${SITE_ROOT:-/var/www/agentsql/current/public}
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

image_fail=0
while IFS= read -r src; do
  case "$src" in
    data:*|http://*|https://*) ;;
    /*) [[ -f "$SITE_ROOT$src" ]] || { printf '      缺失图片: %s\n' "$src"; image_fail=1; } ;;
    *) [[ -f "$SITE_ROOT/$src" ]] || { printf '      缺失图片: %s\n' "$src"; image_fail=1; } ;;
  esac
done < <(grep -RhoE '<img[^>]+src="[^"]+"' "$SITE_ROOT"/*.html | sed -E 's/.*src="([^"]+)".*/\1/' | sort -u)
[[ $image_fail -eq 0 ]] && pass "所有 img src 均有对应文件" || fail "存在缺失图片"

printf '\nSUMMARY  PASS=%d  FAIL=%d\n' "$pass_count" "$fail_count"
[[ $fail_count -eq 0 ]]
