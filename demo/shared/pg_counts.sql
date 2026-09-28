-- Fed to the demo PostgreSQL container over stdin by demo/reset.{sh,ps1}.
-- It is intentionally kept outside demo/postgres so the initdb loader never
-- executes it. Prints the four expected business-table row counts.
SELECT 'customers=' || count(*) FROM customers
UNION ALL SELECT 'products=' || count(*) FROM products
UNION ALL SELECT 'orders=' || count(*) FROM orders
UNION ALL SELECT 'internal_notes=' || count(*) FROM internal_notes
UNION ALL SELECT 'demo_b2_customers=' || count(*) FROM demo_b2_customers
UNION ALL SELECT 'demo_b2_orders=' || count(*) FROM demo_b2_orders
UNION ALL SELECT 'demo_tx_accounts=' || count(*) FROM demo_tx_accounts;
