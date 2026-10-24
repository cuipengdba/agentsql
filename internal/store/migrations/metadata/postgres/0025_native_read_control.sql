ALTER TABLE datasources ADD COLUMN read_control_json TEXT NOT NULL DEFAULT '{}' CHECK(length(read_control_json) <= 16384);
