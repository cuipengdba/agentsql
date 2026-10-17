CREATE TABLE login_attempt_budgets (
  key_hash BLOB PRIMARY KEY CHECK (length(key_hash) = 32),
  started_at TIMESTAMP NOT NULL,
  attempts INTEGER NOT NULL CHECK (attempts BETWEEN 1 AND 10)
);
CREATE INDEX idx_login_attempt_budgets_started ON login_attempt_budgets(started_at);
