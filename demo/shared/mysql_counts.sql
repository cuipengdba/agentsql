-- Fed to the demo MySQL container over stdin by demo/reset.{sh,ps1}.
-- It is intentionally kept outside demo/mysql so the initdb loader never
-- executes it. Prints the four expected business-table row counts.
SELECT CONCAT('customers=', COUNT(*)) FROM customers
UNION ALL SELECT CONCAT('products=', COUNT(*)) FROM products
UNION ALL SELECT CONCAT('orders=', COUNT(*)) FROM orders
UNION ALL SELECT CONCAT('internal_notes=', COUNT(*)) FROM internal_notes;
