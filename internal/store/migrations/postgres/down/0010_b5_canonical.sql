DROP INDEX IF EXISTS idx_b5_tx_events_audit;
DROP TABLE IF EXISTS b5_tx_events;
DROP INDEX IF EXISTS idx_b5_result_receipts_delivery;
DROP INDEX IF EXISTS idx_b5_result_receipts_reconcile;
DROP INDEX IF EXISTS idx_b5_result_receipts_event;
DROP TABLE IF EXISTS b5_result_receipts;
DROP INDEX IF EXISTS idx_b5_dml_grants_lookup;
DROP INDEX IF EXISTS idx_b5_dml_grants_identity;
DROP TABLE IF EXISTS b5_dml_grants;
DROP INDEX IF EXISTS idx_b5_transactions_datasource;
DROP INDEX IF EXISTS idx_b5_transactions_deadline;
DROP INDEX IF EXISTS idx_b5_transactions_one_live_session;
DROP TABLE IF EXISTS b5_transactions;
DROP INDEX IF EXISTS idx_b5_sessions_ttl;
DROP INDEX IF EXISTS idx_b5_sessions_owner;
DROP TABLE IF EXISTS b5_sessions;

