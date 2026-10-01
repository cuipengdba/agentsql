package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

// RBACRepository persists tenant-scoped control-plane identities and grants.
// Every user and role lookup requires a non-empty tenant ID and includes it in
// the SQL predicate; callers cannot accidentally fall back to a global query.
type RBACRepository struct{ repositoryBase }

func rbacScope(tenantID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return errors.New("tenant context is required")
	}
	return nil
}

func (repository *RBACRepository) CreateTenant(ctx context.Context, tenant model.Tenant) (model.Tenant, error) {
	if ctx == nil || strings.TrimSpace(tenant.ID) == "" || strings.TrimSpace(tenant.Name) == "" {
		return model.Tenant{}, errors.New("create tenant: context, id, and name are required")
	}
	if tenant.Status == "" {
		tenant.Status = "active"
	}
	_, err := repository.db.ExecContext(ctx, repository.bind(`
INSERT INTO tenants(id,name,status) VALUES(?,?,?)`), tenant.ID, tenant.Name, tenant.Status)
	if err != nil {
		return model.Tenant{}, fmt.Errorf("create tenant %q: %w", tenant.ID, err)
	}
	return repository.GetTenant(ctx, tenant.ID)
}

func (repository *RBACRepository) GetTenant(ctx context.Context, id string) (model.Tenant, error) {
	if ctx == nil || strings.TrimSpace(id) == "" {
		return model.Tenant{}, errors.New("get tenant: context and id are required")
	}
	item, err := scanTenant(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT id,name,status,created_at,updated_at FROM tenants WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Tenant{}, fmt.Errorf("get tenant %q: %w", id, errors.Join(ErrNotFound, err))
	}
	return item, err
}

func (repository *RBACRepository) ListTenants(ctx context.Context) ([]model.Tenant, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list tenants: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, `SELECT id,name,status,created_at,updated_at FROM tenants ORDER BY created_at,id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	items := make([]model.Tenant, 0)
	for rows.Next() {
		item, scanErr := scanTenant(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list tenants: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *RBACRepository) UpdateTenant(ctx context.Context, tenant model.Tenant) (model.Tenant, error) {
	if ctx == nil || strings.TrimSpace(tenant.ID) == "" {
		return model.Tenant{}, errors.New("update tenant: context and id are required")
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE tenants SET name=?,status=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`), tenant.Name, tenant.Status, tenant.ID)
	if err != nil {
		return model.Tenant{}, fmt.Errorf("update tenant %q: %w", tenant.ID, err)
	}
	if err := checkRowsAffected(result, "tenant", tenant.ID); err != nil {
		return model.Tenant{}, err
	}
	return repository.GetTenant(ctx, tenant.ID)
}

func (repository *RBACRepository) DeleteTenant(ctx context.Context, id string) error {
	if ctx == nil || strings.TrimSpace(id) == "" {
		return errors.New("delete tenant: context and id are required")
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`DELETE FROM tenants WHERE id=?`), id)
	if err != nil {
		return fmt.Errorf("delete tenant %q: %w", id, err)
	}
	return checkRowsAffected(result, "tenant", id)
}

func scanTenant(scanner rowScanner) (model.Tenant, error) {
	var item model.Tenant
	var created, updated databaseTimestamp
	if err := scanner.Scan(&item.ID, &item.Name, &item.Status, &created, &updated); err != nil {
		return model.Tenant{}, err
	}
	var err error
	if item.CreatedAt, err = created.required("tenants.created_at"); err != nil {
		return model.Tenant{}, err
	}
	item.UpdatedAt, err = updated.required("tenants.updated_at")
	return item, err
}

func (repository *RBACRepository) CreateUser(ctx context.Context, user model.User) (model.User, error) {
	if ctx == nil || rbacScope(user.TenantID) != nil || strings.TrimSpace(user.ID) == "" || strings.TrimSpace(user.Username) == "" || user.PasswordHash == "" {
		return model.User{}, errors.New("create user: context, tenant, id, username, and password hash are required")
	}
	if user.Status == "" {
		user.Status = "active"
	}
	if user.AuthProvider == "" {
		user.AuthProvider = "local"
	}
	_, err := repository.db.ExecContext(ctx, repository.bind(`
INSERT INTO users(id,tenant_id,username,display_name,password_hash,status,auth_provider,external_subject)
VALUES(?,?,?,?,?,?,?,?)`), user.ID, user.TenantID, user.Username, user.DisplayName, user.PasswordHash,
		user.Status, user.AuthProvider, optionalString(user.ExternalSubject))
	if err != nil {
		return model.User{}, fmt.Errorf("create user %q: %w", user.ID, err)
	}
	return repository.GetUser(ctx, user.TenantID, user.ID)
}

func (repository *RBACRepository) GetUser(ctx context.Context, tenantID, id string) (model.User, error) {
	if ctx == nil || rbacScope(tenantID) != nil || strings.TrimSpace(id) == "" {
		return model.User{}, errors.New("get user: context, tenant, and id are required")
	}
	item, err := scanUser(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT id,tenant_id,username,display_name,password_hash,status,auth_provider,external_subject,created_at,updated_at
FROM users WHERE tenant_id=? AND id=?`), tenantID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, fmt.Errorf("get user %q: %w", id, errors.Join(ErrNotFound, err))
	}
	return item, err
}

func (repository *RBACRepository) GetUserByUsername(ctx context.Context, tenantID, username string) (model.User, error) {
	if ctx == nil || rbacScope(tenantID) != nil || strings.TrimSpace(username) == "" {
		return model.User{}, errors.New("get user by username: context, tenant, and username are required")
	}
	item, err := scanUser(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT id,tenant_id,username,display_name,password_hash,status,auth_provider,external_subject,created_at,updated_at
FROM users WHERE tenant_id=? AND username=?`), tenantID, username))
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, fmt.Errorf("get user by username: %w", errors.Join(ErrNotFound, err))
	}
	return item, err
}

func (repository *RBACRepository) ListUsers(ctx context.Context, tenantID string) ([]model.User, error) {
	if ctx == nil || rbacScope(tenantID) != nil {
		return nil, errors.New("list users: context and tenant are required")
	}
	rows, err := repository.db.QueryContext(ctx, repository.bind(`
SELECT id,tenant_id,username,display_name,password_hash,status,auth_provider,external_subject,created_at,updated_at
FROM users WHERE tenant_id=? ORDER BY created_at,id`), tenantID)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	items := make([]model.User, 0)
	for rows.Next() {
		item, scanErr := scanUser(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list users: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *RBACRepository) UpdateUser(ctx context.Context, user model.User) (model.User, error) {
	if ctx == nil || rbacScope(user.TenantID) != nil || strings.TrimSpace(user.ID) == "" || user.PasswordHash == "" {
		return model.User{}, errors.New("update user: context, tenant, id, and password hash are required")
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE users SET username=?,display_name=?,password_hash=?,status=?,auth_provider=?,external_subject=?,updated_at=CURRENT_TIMESTAMP
WHERE tenant_id=? AND id=?`), user.Username, user.DisplayName, user.PasswordHash, user.Status, user.AuthProvider,
		optionalString(user.ExternalSubject), user.TenantID, user.ID)
	if err != nil {
		return model.User{}, fmt.Errorf("update user %q: %w", user.ID, err)
	}
	if err := checkRowsAffected(result, "user", user.ID); err != nil {
		return model.User{}, err
	}
	return repository.GetUser(ctx, user.TenantID, user.ID)
}

func (repository *RBACRepository) DeleteUser(ctx context.Context, tenantID, id string) error {
	if ctx == nil || rbacScope(tenantID) != nil || strings.TrimSpace(id) == "" {
		return errors.New("delete user: context, tenant, and id are required")
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`DELETE FROM users WHERE tenant_id=? AND id=?`), tenantID, id)
	if err != nil {
		return fmt.Errorf("delete user %q: %w", id, err)
	}
	return checkRowsAffected(result, "user", id)
}

func scanUser(scanner rowScanner) (model.User, error) {
	var item model.User
	var subject sql.NullString
	var created, updated databaseTimestamp
	if err := scanner.Scan(&item.ID, &item.TenantID, &item.Username, &item.DisplayName, &item.PasswordHash,
		&item.Status, &item.AuthProvider, &subject, &created, &updated); err != nil {
		return model.User{}, err
	}
	item.ExternalSubject = stringPointer(subject)
	var err error
	if item.CreatedAt, err = created.required("users.created_at"); err != nil {
		return model.User{}, err
	}
	item.UpdatedAt, err = updated.required("users.updated_at")
	return item, err
}

func (repository *RBACRepository) CreateRole(ctx context.Context, role model.Role) (model.Role, error) {
	if ctx == nil || rbacScope(role.TenantID) != nil || strings.TrimSpace(role.ID) == "" || strings.TrimSpace(role.Name) == "" {
		return model.Role{}, errors.New("create role: context, tenant, id, and name are required")
	}
	for _, parent := range role.ParentIDs {
		if parent == role.ID {
			return model.Role{}, errors.New("role cannot inherit itself")
		}
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Role{}, err
	}
	_, err = tx.ExecContext(ctx, repository.bind(`
INSERT INTO roles(id,tenant_id,name,description,builtin) VALUES(?,?,?,?,?)`), role.ID, role.TenantID, role.Name, role.Description, role.Builtin)
	if err != nil {
		return model.Role{}, fmt.Errorf("create role %q: %w", role.ID, rollbackMigration(tx, err))
	}
	if err := insertRoleAssociations(ctx, tx, repository, "role_permissions", "permission_code", role.TenantID, role.ID, role.Permissions); err != nil {
		return model.Role{}, rollbackMigration(tx, err)
	}
	if err := insertRoleAssociations(ctx, tx, repository, "role_inheritance", "parent_role_id", role.TenantID, role.ID, role.ParentIDs); err != nil {
		return model.Role{}, rollbackMigration(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return model.Role{}, err
	}
	return repository.GetRole(ctx, role.TenantID, role.ID)
}

func (repository *RBACRepository) GetRole(ctx context.Context, tenantID, id string) (model.Role, error) {
	if ctx == nil || rbacScope(tenantID) != nil || strings.TrimSpace(id) == "" {
		return model.Role{}, errors.New("get role: context, tenant, and id are required")
	}
	item, err := scanRole(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT id,tenant_id,name,description,builtin,created_at,updated_at FROM roles WHERE tenant_id=? AND id=?`), tenantID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Role{}, fmt.Errorf("get role %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.Role{}, err
	}
	item.Permissions, err = repository.rolePermissions(ctx, tenantID, id)
	if err != nil {
		return model.Role{}, err
	}
	item.ParentIDs, err = repository.roleParents(ctx, tenantID, id)
	return item, err
}

func (repository *RBACRepository) ListRoles(ctx context.Context, tenantID string) ([]model.Role, error) {
	if ctx == nil || rbacScope(tenantID) != nil {
		return nil, errors.New("list roles: context and tenant are required")
	}
	rows, err := repository.db.QueryContext(ctx, repository.bind(`
SELECT id,tenant_id,name,description,builtin,created_at,updated_at FROM roles WHERE tenant_id=? ORDER BY created_at,id`), tenantID)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	items := make([]model.Role, 0)
	for rows.Next() {
		item, scanErr := scanRole(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Permissions, err = repository.rolePermissions(ctx, tenantID, items[index].ID)
		if err != nil {
			return nil, err
		}
		items[index].ParentIDs, err = repository.roleParents(ctx, tenantID, items[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (repository *RBACRepository) UpdateRole(ctx context.Context, role model.Role) (model.Role, error) {
	if ctx == nil || rbacScope(role.TenantID) != nil || strings.TrimSpace(role.ID) == "" {
		return model.Role{}, errors.New("update role: context, tenant, and id are required")
	}
	for _, parent := range role.ParentIDs {
		if parent == role.ID {
			return model.Role{}, errors.New("role cannot inherit itself")
		}
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Role{}, err
	}
	result, err := tx.ExecContext(ctx, repository.bind(`
UPDATE roles SET name=?,description=?,updated_at=CURRENT_TIMESTAMP WHERE tenant_id=? AND id=?`),
		role.Name, role.Description, role.TenantID, role.ID)
	if err != nil {
		return model.Role{}, fmt.Errorf("update role %q: %w", role.ID, rollbackMigration(tx, err))
	}
	if err := checkRowsAffected(result, "role", role.ID); err != nil {
		return model.Role{}, rollbackMigration(tx, err)
	}
	for _, association := range [][2]string{{"role_permissions", "permission_code"}, {"role_inheritance", "parent_role_id"}} {
		if _, err := tx.ExecContext(ctx, repository.bind(`DELETE FROM `+association[0]+` WHERE tenant_id=? AND role_id=?`), role.TenantID, role.ID); err != nil {
			return model.Role{}, rollbackMigration(tx, err)
		}
	}
	if err := insertRoleAssociations(ctx, tx, repository, "role_permissions", "permission_code", role.TenantID, role.ID, role.Permissions); err != nil {
		return model.Role{}, rollbackMigration(tx, err)
	}
	if err := insertRoleAssociations(ctx, tx, repository, "role_inheritance", "parent_role_id", role.TenantID, role.ID, role.ParentIDs); err != nil {
		return model.Role{}, rollbackMigration(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return model.Role{}, err
	}
	return repository.GetRole(ctx, role.TenantID, role.ID)
}

func (repository *RBACRepository) DeleteRole(ctx context.Context, tenantID, id string) error {
	role, err := repository.GetRole(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if role.Builtin {
		return errors.New("built-in role cannot be deleted")
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`DELETE FROM roles WHERE tenant_id=? AND id=?`), tenantID, id)
	if err != nil {
		return fmt.Errorf("delete role %q: %w", id, err)
	}
	return checkRowsAffected(result, "role", id)
}

func scanRole(scanner rowScanner) (model.Role, error) {
	var item model.Role
	var created, updated databaseTimestamp
	if err := scanner.Scan(&item.ID, &item.TenantID, &item.Name, &item.Description, &item.Builtin, &created, &updated); err != nil {
		return model.Role{}, err
	}
	var err error
	if item.CreatedAt, err = created.required("roles.created_at"); err != nil {
		return model.Role{}, err
	}
	item.UpdatedAt, err = updated.required("roles.updated_at")
	return item, err
}

func (repository *RBACRepository) ListPermissions(ctx context.Context) ([]model.Permission, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list permissions: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, `SELECT code,description FROM permissions ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Permission, 0)
	for rows.Next() {
		var item model.Permission
		if err := rows.Scan(&item.Code, &item.Description); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *RBACRepository) SetUserRoles(ctx context.Context, tenantID, userID string, roleIDs []string) error {
	if ctx == nil || rbacScope(tenantID) != nil || strings.TrimSpace(userID) == "" {
		return errors.New("set user roles: context, tenant, and user are required")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var userCount int
	if err = tx.QueryRowContext(ctx, repository.bind(`SELECT count(*) FROM users WHERE tenant_id=? AND id=?`), tenantID, userID).Scan(&userCount); err != nil || userCount != 1 {
		if err == nil {
			err = ErrNotFound
		}
		return rollbackMigration(tx, err)
	}
	if _, err = tx.ExecContext(ctx, repository.bind(`DELETE FROM user_roles WHERE tenant_id=? AND user_id=?`), tenantID, userID); err != nil {
		return rollbackMigration(tx, err)
	}
	for _, roleID := range uniqueStrings(roleIDs) {
		if _, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO user_roles(tenant_id,user_id,role_id) VALUES(?,?,?)`), tenantID, userID, roleID); err != nil {
			return rollbackMigration(tx, fmt.Errorf("assign role %q: %w", roleID, err))
		}
	}
	return tx.Commit()
}

func (repository *RBACRepository) UserRoleIDs(ctx context.Context, tenantID, userID string) ([]string, error) {
	if ctx == nil || rbacScope(tenantID) != nil || strings.TrimSpace(userID) == "" {
		return nil, errors.New("list user roles: context, tenant, and user are required")
	}
	rows, err := repository.db.QueryContext(ctx, repository.bind(`
SELECT role_id FROM user_roles WHERE tenant_id=? AND user_id=? ORDER BY role_id`), tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

func (repository *RBACRepository) SetRolePermissions(ctx context.Context, tenantID, roleID string, permissions []string) error {
	return repository.replaceRoleStrings(ctx, "role_permissions", "permission_code", tenantID, roleID, permissions)
}

func (repository *RBACRepository) SetRoleParents(ctx context.Context, tenantID, roleID string, parents []string) error {
	for _, parent := range parents {
		if parent == roleID {
			return errors.New("role cannot inherit itself")
		}
	}
	return repository.replaceRoleStrings(ctx, "role_inheritance", "parent_role_id", tenantID, roleID, parents)
}

func (repository *RBACRepository) replaceRoleStrings(ctx context.Context, table, column, tenantID, roleID string, values []string) error {
	if ctx == nil || rbacScope(tenantID) != nil || strings.TrimSpace(roleID) == "" {
		return errors.New("replace role associations: context, tenant, and role are required")
	}
	allowed := table == "role_permissions" && column == "permission_code" || table == "role_inheritance" && column == "parent_role_id"
	if !allowed {
		return errors.New("replace role associations: invalid association")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var roleCount int
	if err = tx.QueryRowContext(ctx, repository.bind(`SELECT count(*) FROM roles WHERE tenant_id=? AND id=?`), tenantID, roleID).Scan(&roleCount); err != nil || roleCount != 1 {
		if err == nil {
			err = ErrNotFound
		}
		return rollbackMigration(tx, err)
	}
	if _, err = tx.ExecContext(ctx, repository.bind(`DELETE FROM `+table+` WHERE tenant_id=? AND role_id=?`), tenantID, roleID); err != nil {
		return rollbackMigration(tx, err)
	}
	if err = insertRoleAssociations(ctx, tx, repository, table, column, tenantID, roleID, values); err != nil {
		return rollbackMigration(tx, err)
	}
	return tx.Commit()
}

func insertRoleAssociations(ctx context.Context, tx *sql.Tx, repository *RBACRepository, table, column, tenantID, roleID string, values []string) error {
	statement := repository.bind(`INSERT INTO ` + table + `(tenant_id,role_id,` + column + `) VALUES(?,?,?)`)
	for _, value := range uniqueStrings(values) {
		if _, err := tx.ExecContext(ctx, statement, tenantID, roleID, value); err != nil {
			return fmt.Errorf("associate role %q with %q: %w", roleID, value, err)
		}
	}
	return nil
}

func (repository *RBACRepository) rolePermissions(ctx context.Context, tenantID, roleID string) ([]string, error) {
	return repository.roleStrings(ctx, `SELECT permission_code FROM role_permissions WHERE tenant_id=? AND role_id=? ORDER BY permission_code`, tenantID, roleID)
}

func (repository *RBACRepository) roleParents(ctx context.Context, tenantID, roleID string) ([]string, error) {
	return repository.roleStrings(ctx, `SELECT parent_role_id FROM role_inheritance WHERE tenant_id=? AND role_id=? ORDER BY parent_role_id`, tenantID, roleID)
}

func (repository *RBACRepository) roleStrings(ctx context.Context, query, tenantID, roleID string) ([]string, error) {
	rows, err := repository.db.QueryContext(ctx, repository.bind(query), tenantID, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
