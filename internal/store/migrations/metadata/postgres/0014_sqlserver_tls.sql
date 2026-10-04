ALTER TABLE datasources ADD COLUMN tls_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE datasources ADD COLUMN tls_server_name TEXT NOT NULL DEFAULT '';
ALTER TABLE datasources ADD COLUMN tls_ca_file TEXT NOT NULL DEFAULT '';
ALTER TABLE datasources ADD COLUMN trust_server_certificate BOOLEAN NOT NULL DEFAULT FALSE;
