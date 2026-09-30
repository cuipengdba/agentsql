START TRANSACTION;

INSERT INTO customers (id, full_name, phone, email, region, created_at)
WITH digits AS (
    SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
    UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9
), seq AS (
    SELECT ones.d + tens.d * 10 + hundreds.d * 100 + thousands.d * 1000 + 1 AS id
    FROM digits ones CROSS JOIN digits tens CROSS JOIN digits hundreds CROSS JOIN digits thousands
)
SELECT id,
       CONCAT('Demo Customer ', LPAD(id, 3, '0')),
       CONCAT('138', LPAD(id, 8, '0')),
       CONCAT('user', id, '@example.test'),
       ELT(MOD(id, 4) + 1, 'North', 'South', 'East', 'West'),
       TIMESTAMPADD(SECOND, MOD(id * 13, 60),
         TIMESTAMPADD(MINUTE, MOD(id * 11, 60),
           TIMESTAMPADD(HOUR, MOD(id * 7, 24), DATE_SUB(@anchor, INTERVAL MOD(id, 30) DAY))))
FROM seq WHERE id <= 128;

INSERT INTO products (id, sku, name, price, stock, created_at)
WITH digits AS (
    SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
    UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9
), seq AS (
    SELECT ones.d + tens.d * 10 + hundreds.d * 100 + 1 AS id
    FROM digits ones CROSS JOIN digits tens CROSS JOIN digits hundreds
)
SELECT id,
       CONCAT('SKU-', LPAD(id, 4, '0')),
       CONCAT('Demo Product ', LPAD(id, 2, '0')),
       CAST(10 + MOD(id * 137, 9000) / 100 AS DECIMAL(12,2)),
       MOD(id * 17, 200),
       TIMESTAMPADD(SECOND, MOD(id * 19, 60),
         TIMESTAMPADD(MINUTE, MOD(id * 3, 60),
           TIMESTAMPADD(HOUR, MOD(id * 5, 24), DATE_SUB(@anchor, INTERVAL MOD(id, 30) DAY))))
FROM seq WHERE id <= 64;

INSERT INTO orders (id, customer_id, product_id, quantity, amount, status, ordered_at)
WITH digits AS (
    SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
    UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9
), seq AS (
    SELECT ones.d + tens.d * 10 + hundreds.d * 100 + thousands.d * 1000 + 1 AS id
    FROM digits ones CROSS JOIN digits tens CROSS JOIN digits hundreds CROSS JOIN digits thousands
)
SELECT id,
       MOD(id - 1, 128) + 1,
       MOD(id - 1, 64) + 1,
       1 + MOD(id, 5),
       CAST((1 + MOD(id, 5))
         * (10 + MOD((MOD(id - 1, 64) + 1) * 137, 9000) / 100) AS DECIMAL(12,2)),
       ELT(MOD(id, 4) + 1, 'pending', 'paid', 'shipped', 'cancelled'),
       TIMESTAMPADD(SECOND, MOD(id * 23, 60),
         TIMESTAMPADD(MINUTE, MOD(id * 17, 60),
           TIMESTAMPADD(HOUR, MOD(id * 7, 24), DATE_SUB(@anchor, INTERVAL MOD(id, 30) DAY))))
FROM seq WHERE id <= 2400;

INSERT INTO internal_notes (id, note, created_at)
WITH digits AS (
    SELECT 0 AS d UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
    UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9
), seq AS (
    SELECT ones.d + tens.d * 10 + 1 AS id
    FROM digits ones CROSS JOIN digits tens
)
SELECT id,
       CONCAT('Restricted demo note ', LPAD(id, 2, '0')),
       TIMESTAMPADD(SECOND, MOD(id * 31, 60),
         TIMESTAMPADD(MINUTE, MOD(id * 29, 60),
           TIMESTAMPADD(HOUR, MOD(id * 2, 24), DATE_SUB(@anchor, INTERVAL MOD(id, 30) DAY))))
FROM seq WHERE id <= 16;

COMMIT;

ANALYZE TABLE customers;
ANALYZE TABLE products;
ANALYZE TABLE orders;

