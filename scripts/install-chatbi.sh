#!/bin/sh
# Called only from install.sh after the containing package has been verified.
# Keep a dedicated binary so a gateway-only repair cannot silently change the
# running Q&A service. Both components remain in the same signed release.
set -eu
umask 077
ROOT=${1:?verified package root required}
MODE=${2:-start}
MODEL_SOURCE=${3:-}
case "$MODE" in start|no-start|upgrade) ;; *) exit 2 ;; esac
REQUESTED_MODE=$MODE
[ "$(id -u)" -eq 0 ] || { echo 'ChatBI installation requires root.' >&2; exit 1; }
CONFIG=/etc/agentsql/chatbi.yaml
ENVFILE=/etc/agentsql/chatbi.env
DB=/var/lib/agentsql/chatbi.db
UNIT=/etc/systemd/system/agentsql-chatbi.service
ASSISTANT_UNIT=/etc/systemd/system/agentsql-chatbi-assistant.service
BIN=/usr/local/lib/agentsql/chatbi/agentsql
MODEL=/usr/local/lib/agentsql/chatbi/model
STATE=/etc/agentsql/chatbi-install-state
BACKUP=
MODEL_STAGE=
MODEL_CHANGED=0
ACTIVE=0
ENABLED=0
ASSISTANT_ACTIVE=0
ASSISTANT_ENABLED=0
TRANSACTION=0
SYSTEMD=0
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] && SYSTEMD=1
[ "$MODE" = no-start ] || [ "$SYSTEMD" -eq 1 ] || { echo 'systemd is required.' >&2; exit 1; }
for path in /etc/agentsql /var/lib/agentsql /var/backups/agentsql /usr/local/lib/agentsql /usr/local/lib/agentsql/chatbi \
            "$CONFIG" "$ENVFILE" "$DB" "$DB-wal" "$DB-shm" "$UNIT" "$ASSISTANT_UNIT" "$BIN" "$MODEL" "$STATE"; do
  [ ! -L "$path" ] || { echo 'Refusing a symbolic link in managed ChatBI paths.' >&2; exit 1; }
done
if [ -e "$UNIT" ] || [ -e "$ASSISTANT_UNIT" ] || [ -e "$BIN" ]; then
  [ -f "$STATE" ] || { echo 'An unmanaged ChatBI installation already exists.' >&2; exit 1; }
fi
# Imported/custom deployments use their own explicit backup procedure; never
# guess a database or secret location from shell-evaluated configuration.
if [ -f "$CONFIG" ]; then
  grep -Eq '^store_path: "/var/lib/agentsql/chatbi.db"$' "$CONFIG" &&
  grep -Eq '^secret_env: "AGENTSQL_CHATBI_SECRET"$' "$CONFIG" &&
  grep -Eq '^listen: "127.0.0.1:8000"$' "$CONFIG" || {
    echo 'Managed upgrade requires the default ChatBI store, secret variable and listener.' >&2; exit 1;
  }
fi
VERSION=$(cat "$ROOT/VERSION")
[ "$("$ROOT/agentsql" --version)" = "$VERSION" ] || exit 1
if [ "$(printf '%s\n%s\n' v4.5.0 "$VERSION" | sort -V | head -n 1)" = v4.5.0 ]; then
  [ -n "$MODEL_SOURCE" ] || { echo 'ChatBI v4.5.0+ requires a verified local model directory.' >&2; exit 1; }
fi
if [ -n "$MODEL_SOURCE" ]; then
  [ ! -L "$MODEL_SOURCE" ] && [ -d "$MODEL_SOURCE" ] || { echo 'Model source must be a regular directory.' >&2; exit 1; }
  "$ROOT/agentsql" chatbi verify-embedding-model --path "$MODEL_SOURCE" >/dev/null || {
    echo 'Pinned ChatBI model source failed verification.' >&2; exit 1;
  }
  if [ -e "$MODEL" ] && [ ! -d "$MODEL" ]; then
    echo 'Managed ChatBI model path is not a directory.' >&2; exit 1
  fi
  if [ -d "$MODEL" ]; then
    "$ROOT/agentsql" chatbi verify-embedding-model --path "$MODEL" >/dev/null || {
      echo 'Existing model directory is not the managed pinned model.' >&2; exit 1;
    }
  fi
  if [ -f "$CONFIG" ] && grep -Eq '^embedding_model_path:' "$CONFIG" &&
     ! grep -Fxq "embedding_model_path: \"$MODEL\"" "$CONFIG"; then
    echo 'Existing ChatBI configuration uses a different model path.' >&2; exit 1
  fi
fi
if [ -f "$STATE" ]; then
  # Never use this optional component to bypass the main installer's downgrade gate.
  old=$(cat "$STATE")
  first=$(printf '%s\n%s\n' "${old#v}" "${VERSION#v}" | sort -V | head -n 1)
  [ "$first" = "${old#v}" ] || { echo 'ChatBI downgrade refused.' >&2; exit 1; }
fi
restore() {
  if [ "$SYSTEMD" -eq 1 ] && [ -f "$ASSISTANT_UNIT" ]; then
    systemctl stop agentsql-chatbi-assistant >/dev/null 2>&1 || return 1
  fi
  if [ "$SYSTEMD" -eq 1 ] && [ -f "$UNIT" ]; then
    systemctl stop agentsql-chatbi >/dev/null 2>&1 || return 1
  fi
  if [ "$MODEL_CHANGED" -eq 1 ]; then
    if [ -d "$MODEL" ]; then rm -r -- "$MODEL" || return 1; fi
    if [ -d "$BACKUP/model" ]; then cp -pR "$BACKUP/model" "$MODEL" || return 1; fi
  fi
  for name in binary unit assistant-unit state chatbi.yaml chatbi.env chatbi.db chatbi.db-wal chatbi.db-shm; do
    case "$name" in
      binary) target=$BIN ;;
      unit) target=$UNIT ;;
      assistant-unit) target=$ASSISTANT_UNIT ;;
      state) target=$STATE ;;
      chatbi.yaml) target=$CONFIG ;;
      chatbi.env) target=$ENVFILE ;;
      *) target=/var/lib/agentsql/$name ;;
    esac
    if [ -f "$BACKUP/$name" ]; then cp -p "$BACKUP/$name" "$target" || return 1
    else rm -f "$target" || return 1; fi
  done
  if [ "$SYSTEMD" -eq 1 ]; then
    systemctl daemon-reload || return 1
    if [ "$ENABLED" -eq 1 ]; then systemctl enable agentsql-chatbi >/dev/null 2>&1 || return 1
    else systemctl disable agentsql-chatbi >/dev/null 2>&1 || true; fi
    [ "$ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi || return 1
    if [ "$ASSISTANT_ENABLED" -eq 1 ]; then systemctl enable agentsql-chatbi-assistant >/dev/null 2>&1 || return 1
    else systemctl disable agentsql-chatbi-assistant >/dev/null 2>&1 || true; fi
    [ "$ASSISTANT_ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi-assistant || return 1
  fi
}
cleanup() {
  code=$?
  trap - EXIT HUP INT TERM
  if [ "$TRANSACTION" -eq 1 ]; then
    if restore; then echo "ChatBI previous files and service state restored; backup: $BACKUP" >&2
    else echo "ChatBI automatic restoration failed; preserve backup: $BACKUP" >&2; fi
  fi
  if [ -n "$MODEL_STAGE" ] && [ -d "$MODEL_STAGE" ]; then rm -r -- "$MODEL_STAGE" || true; fi
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
if [ "$SYSTEMD" -eq 1 ]; then
  systemctl is-active --quiet agentsql-chatbi 2>/dev/null && ACTIVE=1 || true
  systemctl is-enabled --quiet agentsql-chatbi 2>/dev/null && ENABLED=1 || true
  systemctl is-active --quiet agentsql-chatbi-assistant 2>/dev/null && ASSISTANT_ACTIVE=1 || true
  systemctl is-enabled --quiet agentsql-chatbi-assistant 2>/dev/null && ASSISTANT_ENABLED=1 || true
fi
if [ "$MODE" = upgrade ]; then
  MODE=start
  if [ -f "$STATE" ] && [ "$ACTIVE" -eq 0 ]; then MODE=no-start; fi
fi
install -d -o root -g root -m 0755 /usr/local/lib/agentsql/chatbi
install -d -o root -g root -m 0700 /var/backups/agentsql
BACKUP=$(mktemp -d /var/backups/agentsql/chatbi-upgrade.XXXXXXXX)
if [ "$SYSTEMD" -eq 1 ] && [ -f "$ASSISTANT_UNIT" ]; then
  systemctl stop agentsql-chatbi-assistant || {
    [ "$ASSISTANT_ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi-assistant || true
    echo 'Cannot stop isolated assistant; nothing replaced.' >&2; exit 1;
  }
  if systemctl is-active --quiet agentsql-chatbi-assistant; then echo 'Isolated assistant is still active.' >&2; exit 1; fi
fi
if [ "$SYSTEMD" -eq 1 ] && [ -f "$UNIT" ]; then
  systemctl stop agentsql-chatbi || {
    [ "$ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi || true
    [ "$ASSISTANT_ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi-assistant || true
    echo 'Cannot stop ChatBI; nothing replaced.' >&2; exit 1;
  }
  if systemctl is-active --quiet agentsql-chatbi; then echo 'ChatBI is still active.' >&2; exit 1; fi
fi
# Even a failure during backup must restore the prior service state, without
# restoring an incomplete database snapshot.
for pair in "$BIN:binary" "$UNIT:unit" "$ASSISTANT_UNIT:assistant-unit" "$STATE:state" "$CONFIG:chatbi.yaml" "$ENVFILE:chatbi.env" "$DB:chatbi.db" "$DB-wal:chatbi.db-wal" "$DB-shm:chatbi.db-shm"; do
  path=${pair%:*}; name=${pair##*:}
  if [ -f "$path" ] && ! cp -p "$path" "$BACKUP/$name"; then
    [ "$ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi || true
    [ "$ASSISTANT_ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi-assistant || true
    echo 'ChatBI backup failed; nothing replaced.' >&2; exit 1
  fi
done
if [ -d "$MODEL" ] && ! cp -pR "$MODEL" "$BACKUP/model"; then
  [ "$ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi || true
  [ "$ASSISTANT_ACTIVE" -eq 0 ] || systemctl start agentsql-chatbi-assistant || true
  echo 'ChatBI model backup failed; nothing replaced.' >&2; exit 1
fi
TRANSACTION=1
if [ ! -e "$ENVFILE" ]; then
  [ ! -e "$DB" ] || { echo 'Restore the existing ChatBI encryption key before installation.' >&2; exit 1; }
  secret=$(head -c 24 /dev/urandom | base64 | tr -d '\r\n')
  password=$(head -c 24 /dev/urandom | base64 | tr -d '\r\n')
  [ "${#secret}" -eq 32 ] && [ "${#password}" -eq 32 ] || exit 1
  printf 'AGENTSQL_CHATBI_SECRET=%s\nAGENTSQL_CHATBI_ADMIN_PASSWORD=%s\n' "$secret" "$password" > "$ENVFILE"
  chmod 0600 "$ENVFILE"
fi
[ -e "$CONFIG" ] || install -o root -g agentsql -m 0640 "$ROOT/deploy/systemd/chatbi.yaml" "$CONFIG"
if [ -n "$MODEL_SOURCE" ]; then
  MODEL_STAGE="/usr/local/lib/agentsql/chatbi/.model.new.$$"
  [ ! -e "$MODEL_STAGE" ] && [ ! -L "$MODEL_STAGE" ] || exit 1
  install -d -o root -g root -m 0700 "$MODEL_STAGE"
  cp -pR "$MODEL_SOURCE/." "$MODEL_STAGE/"
  chown -R root:root "$MODEL_STAGE"
  chmod -R a+rX "$MODEL_STAGE"
  "$ROOT/agentsql" chatbi verify-embedding-model --path "$MODEL_STAGE" >/dev/null || {
    echo 'Copied ChatBI model failed verification.' >&2; exit 1
  }
  MODEL_CHANGED=1
  if [ -d "$MODEL" ]; then rm -r -- "$MODEL"; fi
  mv "$MODEL_STAGE" "$MODEL"
  MODEL_STAGE=
  if ! grep -Fxq "embedding_model_path: \"$MODEL\"" "$CONFIG"; then
    printf 'embedding_model_path: "%s"\n' "$MODEL" >> "$CONFIG"
  fi
fi
install -o root -g root -m 0755 "$ROOT/agentsql" "$BIN.new"
mv "$BIN.new" "$BIN"
install -o root -g root -m 0644 "$ROOT/deploy/systemd/agentsql-chatbi.service" "$UNIT"
install -o root -g root -m 0644 "$ROOT/deploy/systemd/agentsql-chatbi-assistant.service" "$ASSISTANT_UNIT"
if [ ! -e "$DB" ]; then
  # Read only known scalar values, never source/execute an environment file.
  AGENTSQL_CHATBI_SECRET=$(sed -n 's/^AGENTSQL_CHATBI_SECRET=//p' "$ENVFILE")
  AGENTSQL_CHATBI_ADMIN_PASSWORD=$(sed -n 's/^AGENTSQL_CHATBI_ADMIN_PASSWORD=//p' "$ENVFILE")
  export AGENTSQL_CHATBI_SECRET AGENTSQL_CHATBI_ADMIN_PASSWORD
  "$BIN" chatbi init --config "$CONFIG"
  unset AGENTSQL_CHATBI_SECRET AGENTSQL_CHATBI_ADMIN_PASSWORD
  chown agentsql:agentsql "$DB"
  chmod 0600 "$DB"
fi
if [ "$SYSTEMD" -eq 1 ]; then
  systemctl daemon-reload
  if [ "$MODE" = start ]; then
    if [ "$REQUESTED_MODE" != upgrade ] || [ ! -f "$STATE" ] || [ "$ENABLED" -eq 1 ]; then
      systemctl enable agentsql-chatbi >/dev/null 2>&1
    fi
    systemctl start agentsql-chatbi
    attempt=0; healthy=0
    while [ "$attempt" -lt 30 ]; do
      if /usr/local/bin/agentsqlctl health --url http://127.0.0.1:8000/healthz > "$BACKUP/health.json" 2>/dev/null &&
        tr -d ' \t\r\n' < "$BACKUP/health.json" | grep -F '"version":"'"$VERSION"'"' >/dev/null; then healthy=1; break; fi
      attempt=$((attempt + 1)); sleep 2
    done
    [ "$healthy" -eq 1 ] || { echo 'ChatBI health/version verification failed.' >&2; exit 1; }
    if [ "$ASSISTANT_ENABLED" -eq 1 ]; then
      systemctl enable agentsql-chatbi-assistant >/dev/null 2>&1
      if [ "$ASSISTANT_ACTIVE" -eq 1 ]; then
        systemctl start agentsql-chatbi-assistant
        attempt=0; healthy=0
        while [ "$attempt" -lt 30 ]; do
          if /usr/local/bin/agentsqlctl health --url http://127.0.0.1:8002/healthz > "$BACKUP/assistant-health.json" 2>/dev/null &&
            tr -d ' \t\r\n' < "$BACKUP/assistant-health.json" | grep -F '"version":"'"$VERSION"'"' >/dev/null; then healthy=1; break; fi
          attempt=$((attempt + 1)); sleep 2
        done
        [ "$healthy" -eq 1 ] || { echo 'Isolated assistant health/version verification failed.' >&2; exit 1; }
      fi
    fi
  fi
fi
printf '%s\n' "$VERSION" > "$STATE"
chmod 0644 "$STATE"
TRANSACTION=0
echo "ChatBI $VERSION installed. Credentials: $ENVFILE (root only). Backup: $BACKUP"
echo 'Configure a model and per-user gateway mappings before asking data questions.'
if [ "$MODE" = no-start ]; then echo 'ChatBI was not started or health-checked.'; fi
