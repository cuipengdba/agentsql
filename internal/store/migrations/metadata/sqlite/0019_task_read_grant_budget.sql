ALTER TABLE task_read_grant_requests ADD COLUMN max_uses INTEGER CHECK (max_uses IS NULL OR max_uses BETWEEN 1 AND 10);
ALTER TABLE task_read_grants ADD COLUMN max_uses INTEGER CHECK (max_uses IS NULL OR max_uses BETWEEN 1 AND 10);
ALTER TABLE task_read_grants ADD COLUMN used_count INTEGER NOT NULL DEFAULT 0 CHECK (used_count >= 0);
