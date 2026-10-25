#!/bin/sh
# Run in an expendable Linux container with --network none. Requires a built
# native binary as $1 and the repository at $2. No host installation paths.
set -eu
umask 077
BINARY=${1:?native test binary required}
SOURCE=${2:?repository required}
[ -f /.dockerenv ] || { echo 'This installation test must run in an isolated Docker container.' >&2; exit 1; }
groupadd agentsql
useradd -g agentsql -M -s /usr/sbin/nologin agentsql
install -d -m 0750 -o root -g agentsql /etc/agentsql
install -d -m 0750 -o agentsql -g agentsql /var/lib/agentsql
install -d /etc/systemd/system /package/deploy/systemd
cp "$BINARY" /package/agentsql
"$BINARY" --version > /package/VERSION
cp "$SOURCE/deploy/systemd/chatbi.yaml" "$SOURCE/deploy/systemd/agentsql-chatbi.service" /package/deploy/systemd/
sh "$SOURCE/scripts/install-chatbi.sh" /package no-start
test "$(stat -c %a /etc/agentsql/chatbi.env)" = 600
test "$(stat -c %U /var/lib/agentsql/chatbi.db)" = agentsql
test "$(stat -c %a /var/lib/agentsql/chatbi.db)" = 600
cp /etc/agentsql/chatbi.env /tmp/original.env
sha256sum /var/lib/agentsql/chatbi.db > /tmp/store-before
sh "$SOURCE/scripts/install-chatbi.sh" /package no-start
cmp /tmp/original.env /etc/agentsql/chatbi.env
sha256sum -c /tmp/store-before

# Start the installed service binary under its real unprivileged account.
# This validates the offline installation without claiming a systemd boot test.
AGENTSQL_CHATBI_SECRET=$(sed -n 's/^AGENTSQL_CHATBI_SECRET=//p' /etc/agentsql/chatbi.env)
export AGENTSQL_CHATBI_SECRET
runuser -u agentsql --preserve-environment -- /usr/local/lib/agentsql/chatbi/agentsql chatbi serve --config /etc/agentsql/chatbi.yaml > /tmp/chatbi.log 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true' EXIT
attempt=0
until curl -fsS http://127.0.0.1:8000/healthz > /tmp/health.json; do
  attempt=$((attempt + 1)); [ "$attempt" -lt 20 ] || exit 1; sleep 1
done
password=$(sed -n 's/^AGENTSQL_CHATBI_ADMIN_PASSWORD=//p' /etc/agentsql/chatbi.env)
printf '{"username":"admin","password":"%s"}' "$password" > /tmp/login.private.json
curl -fsS -H 'Content-Type: application/json' --data-binary @/tmp/login.private.json http://127.0.0.1:8000/api/v1/agentsql/login > /tmp/session.private.json
grep -q '"access_token"' /tmp/session.private.json
kill "$pid"; wait "$pid" || true
trap - EXIT
unset AGENTSQL_CHATBI_SECRET password

# Inject a service-start failure to prove rollback preserves both data and the
# prior unit. This is fault injection, distinct from a real systemd boot test.
sha256sum /var/lib/agentsql/chatbi.db /etc/systemd/system/agentsql-chatbi.service > /tmp/rollback-before
printf '\n# synthetic candidate unit\n' >> /package/deploy/systemd/agentsql-chatbi.service
mkdir -p /run/systemd/system /tmp/fault-bin
cat > /tmp/fault-bin/systemctl <<'EOF'
#!/bin/sh
case "$1" in is-active|is-enabled|start) exit 1 ;; *) exit 0 ;; esac
EOF
chmod 0755 /tmp/fault-bin/systemctl
if PATH="/tmp/fault-bin:$PATH" sh "$SOURCE/scripts/install-chatbi.sh" /package start > /tmp/rollback.log 2>&1; then
  echo 'Injected startup failure incorrectly succeeded.' >&2; exit 1
fi
sha256sum -c /tmp/rollback-before
cmp /tmp/original.env /etc/agentsql/chatbi.env
grep -q 'previous files and service state restored' /tmp/rollback.log
echo 'PASS: offline fresh install, private credentials, idempotent repair, real unprivileged login, injected startup rollback.'
