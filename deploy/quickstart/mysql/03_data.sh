#!/bin/sh
set -eu

: "${DEMO_ANCHOR_DATE:?DEMO_ANCHOR_DATE is required}"

case "$DEMO_ANCHOR_DATE" in
  [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
  *)
    echo "DEMO_ANCHOR_DATE must use YYYY-MM-DD" >&2
    exit 1
    ;;
esac

canonical_anchor="$(date -u -d "$DEMO_ANCHOR_DATE" +%F 2>/dev/null || true)"
if [ "$canonical_anchor" != "$DEMO_ANCHOR_DATE" ]; then
  echo "DEMO_ANCHOR_DATE must be a real calendar date" >&2
  exit 1
fi

# The checks above limit interpolation to a validated date containing only
# digits and hyphens. The variable and SOURCE must share this MySQL session.
mysql --protocol=socket \
  --user=root \
  --password="$MYSQL_ROOT_PASSWORD" \
  --database="$MYSQL_DATABASE" \
  --batch <<SQL
SET @anchor = STR_TO_DATE('${DEMO_ANCHOR_DATE}', '%Y-%m-%d');
SOURCE /docker-entrypoint-initdb.d/03_data.sql.inc;
SQL
