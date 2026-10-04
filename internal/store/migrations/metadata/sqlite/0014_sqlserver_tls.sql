ALTER TABLE datasources ADD COLUMN tls_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE datasources ADD COLUMN tls_server_name TEXT NOT NULL DEFAULT '';
ALTER TABLE datasources ADD COLUMN tls_ca_file TEXT NOT NULL DEFAULT '';
ALTER TABLE datasources ADD COLUMN trust_server_certificate INTEGER NOT NULL DEFAULT 0 CHECK (trust_server_certificate IN (0, 1));
