CREATE TABLE tenants (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  status     TEXT NOT NULL CHECK (status IN ('active','disabled')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE users (
  id               TEXT PRIMARY KEY,
  tenant_id        TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  username         TEXT NOT NULL,
  display_name     TEXT NOT NULL,
  password_hash    TEXT NOT NULL,
  status           TEXT NOT NULL CHECK (status IN ('active','disabled')),
  auth_provider    TEXT NOT NULL DEFAULT 'local' CHECK (auth_provider IN ('local','oidc')),
  external_subject TEXT,
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (tenant_id, username),
  UNIQUE (id, tenant_id)
);

CREATE TABLE permissions (
  code        TEXT PRIMARY KEY,
  description TEXT NOT NULL
);

CREATE TABLE roles (
  id          TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  builtin     INTEGER NOT NULL DEFAULT 0 CHECK (builtin IN (0,1)),
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (tenant_id, name),
  UNIQUE (id, tenant_id)
);

CREATE TABLE user_roles (
  tenant_id TEXT NOT NULL,
  user_id   TEXT NOT NULL,
  role_id   TEXT NOT NULL,
  PRIMARY KEY (tenant_id, user_id, role_id),
  FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (role_id, tenant_id) REFERENCES roles(id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE role_permissions (
  tenant_id      TEXT NOT NULL,
  role_id        TEXT NOT NULL,
  permission_code TEXT NOT NULL REFERENCES permissions(code) ON DELETE RESTRICT,
  PRIMARY KEY (tenant_id, role_id, permission_code),
  FOREIGN KEY (role_id, tenant_id) REFERENCES roles(id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE role_inheritance (
  tenant_id      TEXT NOT NULL,
  role_id        TEXT NOT NULL,
  parent_role_id TEXT NOT NULL,
  PRIMARY KEY (tenant_id, role_id, parent_role_id),
  CHECK (role_id <> parent_role_id),
  FOREIGN KEY (role_id, tenant_id) REFERENCES roles(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (parent_role_id, tenant_id) REFERENCES roles(id, tenant_id) ON DELETE CASCADE
);

CREATE INDEX idx_users_tenant ON users(tenant_id, created_at, id);
CREATE INDEX idx_roles_tenant ON roles(tenant_id, created_at, id);
CREATE INDEX idx_user_roles_user ON user_roles(tenant_id, user_id);
CREATE INDEX idx_role_permissions_role ON role_permissions(tenant_id, role_id);

INSERT OR IGNORE INTO permissions(code, description) VALUES
  ('datasource.manage', 'Create, inspect, update, and delete data sources and agents'),
  ('strategy.manage', 'Manage policies, rules, masking, discovery, and notifications'),
  ('query.execute', 'Assess or execute queries and decide approvals'),
  ('audit.view', 'View dashboards, audit records, streams, and integrity status'),
  ('user.manage', 'Manage users and user-role assignments in the current tenant'),
  ('role.manage', 'Manage roles, inheritance, and role permissions in the current tenant'),
  ('tenant.manage', 'Manage tenant records through the bootstrap administrator boundary');
