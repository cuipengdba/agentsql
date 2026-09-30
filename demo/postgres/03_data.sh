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

psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  -v anchor="$DEMO_ANCHOR_DATE" <<'SQL'
BEGIN;

INSERT INTO customers (id, full_name, phone, email, region, created_at)
SELECT id,
       'Demo Customer ' || lpad(id::text, 3, '0'),
       '138' || lpad(id::text, 8, '0'),
       'user' || id || '@example.test',
       (ARRAY['North', 'South', 'East', 'West'])[(id % 4) + 1],
       (:'anchor'::date - (id % 30) * INTERVAL '1 day'
           + make_interval(hours => (id * 7) % 24,
                           mins => (id * 11) % 60,
                           secs => (id * 13) % 60)) AT TIME ZONE 'UTC'
FROM generate_series(1, 128) AS series(id);

INSERT INTO products (id, sku, name, price, stock, created_at)
SELECT id,
       'SKU-' || lpad(id::text, 4, '0'),
       'Demo Product ' || lpad(id::text, 2, '0'),
       (10 + ((id * 137) % 9000) / 100.0)::NUMERIC(12,2),
       (id * 17) % 200,
       (:'anchor'::date - (id % 30) * INTERVAL '1 day'
           + make_interval(hours => (id * 5) % 24,
                           mins => (id * 3) % 60,
                           secs => (id * 19) % 60)) AT TIME ZONE 'UTC'
FROM generate_series(1, 64) AS series(id);

INSERT INTO orders (id, customer_id, product_id, quantity, amount, status, ordered_at)
SELECT id,
       ((id - 1) % 128) + 1,
       ((id - 1) % 64) + 1,
       1 + (id % 5),
       ((1 + (id % 5))
           * (10 + (((((id - 1) % 64) + 1) * 137) % 9000) / 100.0))::NUMERIC(12,2),
       (ARRAY['pending', 'paid', 'shipped', 'cancelled'])[(id % 4) + 1],
       (:'anchor'::date - (id % 30) * INTERVAL '1 day'
           + make_interval(hours => (id * 7) % 24,
                           mins => (id * 17) % 60,
                           secs => (id * 23) % 60)) AT TIME ZONE 'UTC'
FROM generate_series(1, 2400) AS series(id);

INSERT INTO internal_notes (id, note, created_at)
SELECT id,
       'Restricted demo note ' || lpad(id::text, 2, '0'),
       (:'anchor'::date - (id % 30) * INTERVAL '1 day'
           + make_interval(hours => (id * 2) % 24,
                           mins => (id * 29) % 60,
                           secs => (id * 31) % 60)) AT TIME ZONE 'UTC'
FROM generate_series(1, 16) AS series(id);

INSERT INTO demo_b2_customers (id, full_name, phone, email, region)
SELECT id, full_name, phone, email, region FROM customers;

INSERT INTO demo_b2_orders (id, customer_id, status)
SELECT id, customer_id, status FROM orders;

INSERT INTO demo_tx_accounts (id, balance, status) VALUES
    (1, 1000, 'ready'),
    (2, 2000, 'ready'),
    (3, 3000, 'ready'),
    (4, 4000, 'ready');

COMMIT;

ANALYZE customers;
ANALYZE products;
ANALYZE orders;
ANALYZE demo_b2_customers;
ANALYZE demo_b2_orders;
ANALYZE demo_tx_accounts;
SQL
