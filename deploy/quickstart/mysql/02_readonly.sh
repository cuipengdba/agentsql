#!/bin/sh
set -eu

: "${DEMO_MYSQL_RO_PASSWORD:?DEMO_MYSQL_RO_PASSWORD is required}"

# Password interpolation below is intentionally limited to a conservative
# ASCII set so no quote or backslash can alter the SQL statement.
case "$DEMO_MYSQL_RO_PASSWORD" in
  *[!A-Za-z0-9._~!@#%^+=-]*)
    echo "DEMO_MYSQL_RO_PASSWORD must contain only safe ASCII characters" >&2
    exit 1
    ;;
esac

mysql --protocol=socket \
  --user=root \
  --password="$MYSQL_ROOT_PASSWORD" \
  --database="$MYSQL_DATABASE" \
  --batch \
  --execute="CREATE USER IF NOT EXISTS 'agentsql_demo_ro'@'%'; ALTER USER 'agentsql_demo_ro'@'%' IDENTIFIED BY '${DEMO_MYSQL_RO_PASSWORD}'; GRANT SELECT ON agentsql_demo.* TO 'agentsql_demo_ro'@'%';"
