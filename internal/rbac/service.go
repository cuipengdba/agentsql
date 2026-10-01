// Package rbac implements local control-plane identities, tenant isolation,
// role inheritance, and fail-closed permission evaluation.
package rbac

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	DefaultTenantID = "tenant_default"
	BootstrapUserID = "user_bootstrap_admin"
	AdminRoleID     = "role_default_admin"
	AuditorRoleID   = "role_default_auditor"
	OperatorRoleID  = "role_default_operator"
	ViewerRoleID    = "role_default_viewer"

	PermissionDatasourceManage = "datasource.manage"
	PermissionStrategyManage   = "strategy.manage"
	PermissionQueryExecute     = "query.execute"
	PermissionAuditView        = "audit.view"
	PermissionUserManage       = "user.manage"
	PermissionRoleManage       = "role.manage"
	PermissionTenantManage     = "tenant.manage"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrForbidden          = errors.New("permission denied")
	ErrInvalidTenant      = errors.New("invalid tenant context")
)

var allPermissions = []string{
	PermissionAuditView,
	PermissionDatasourceManage,
	PermissionQueryExecute,
	PermissionRoleManage,
	PermissionStrategyManage,
	PermissionTenantManage,
	PermissionUserManage,
}

// Principal is the authenticated, tenant-bound identity placed in a request.
type Principal struct {
	UserID      string
	TenantID    string
	Username    string
	Permissions []string
	RoleIDs     []string
}

func (principal Principal) Has(permission string) bool {
	for _, granted := range principal.Permissions {
		if subtle.ConstantTimeCompare([]byte(granted), []byte(permission)) == 1 {
			return true
		}
	}
	return false
}

func (principal Principal) IsBootstrapAdmin() bool {
	return principal.UserID == BootstrapUserID && principal.TenantID == DefaultTenantID && principal.Has(PermissionTenantManage)
}

// Service composes password verification and the persisted RBAC graph.
type Service struct{ repository *store.RBACRepository }

func NewService(repository *store.RBACRepository) *Service { return &Service{repository: repository} }

// HashPassword produces a bcrypt password hash suitable for persistence.
func HashPassword(password string) (string, error) {
	if len(password) == 0 || len(password) > 1024 {
		return "", errors.New("password must be between 1 and 1024 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword compares a password with its bcrypt hash and rejects malformed
// hashes without exposing the underlying parser error.
func VerifyPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Bootstrap creates the default tenant, preset roles, and initial administrator
// exactly when missing. The password is accepted from the existing environment
// credential source and is persisted only as a bcrypt hash.
func (service *Service) Bootstrap(ctx context.Context, username, password string) error {
	if service == nil || service.repository == nil || ctx == nil {
		return errors.New("bootstrap RBAC: service is unavailable")
	}
	if _, err := service.repository.GetTenant(ctx, DefaultTenantID); errors.Is(err, store.ErrNotFound) {
		if _, err = service.repository.CreateTenant(ctx, model.Tenant{ID: DefaultTenantID, Name: "Default", Status: "active"}); err != nil {
			return fmt.Errorf("bootstrap default tenant: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read default tenant: %w", err)
	}
	presets := []model.Role{
		{ID: AdminRoleID, TenantID: DefaultTenantID, Name: "admin", Description: "Full control-plane administrator", Builtin: true, Permissions: allPermissions},
		{ID: AuditorRoleID, TenantID: DefaultTenantID, Name: "auditor", Description: "Read audit and integrity information", Builtin: true, Permissions: []string{PermissionAuditView}},
		{ID: OperatorRoleID, TenantID: DefaultTenantID, Name: "operator", Description: "Operate data sources, strategies, and queries", Builtin: true, Permissions: []string{PermissionDatasourceManage, PermissionStrategyManage, PermissionQueryExecute}},
		{ID: ViewerRoleID, TenantID: DefaultTenantID, Name: "viewer", Description: "Read-only audit viewer", Builtin: true, Permissions: []string{PermissionAuditView}},
	}
	for _, role := range presets {
		if _, err := service.repository.GetRole(ctx, DefaultTenantID, role.ID); errors.Is(err, store.ErrNotFound) {
			if _, err = service.repository.CreateRole(ctx, role); err != nil {
				return fmt.Errorf("bootstrap role %q: %w", role.Name, err)
			}
		} else if err != nil {
			return fmt.Errorf("read bootstrap role %q: %w", role.Name, err)
		}
	}
	user, err := service.repository.GetUser(ctx, DefaultTenantID, BootstrapUserID)
	if errors.Is(err, store.ErrNotFound) {
		hash, hashErr := HashPassword(password)
		if hashErr != nil {
			return fmt.Errorf("bootstrap administrator password: %w", hashErr)
		}
		user, err = service.repository.CreateUser(ctx, model.User{
			ID: BootstrapUserID, TenantID: DefaultTenantID, Username: username,
			DisplayName: "Administrator", PasswordHash: hash, Status: "active", AuthProvider: "local",
		})
	}
	if err != nil {
		return fmt.Errorf("bootstrap administrator: %w", err)
	}
	if user.Username != username {
		return fmt.Errorf("bootstrap administrator username differs from configured username")
	}
	if err := service.repository.SetUserRoles(ctx, DefaultTenantID, BootstrapUserID, []string{AdminRoleID}); err != nil {
		return fmt.Errorf("bootstrap administrator role: %w", err)
	}
	return nil
}

// ProvisionTenant installs the same public preset role vocabulary for a newly
// created tenant. It does not create a user or accept credentials implicitly.
func (service *Service) ProvisionTenant(ctx context.Context, tenantID string) ([]model.Role, error) {
	if service == nil || service.repository == nil || ctx == nil || strings.TrimSpace(tenantID) == "" {
		return nil, ErrInvalidTenant
	}
	presets := []model.Role{
		{ID: NewID("role"), TenantID: tenantID, Name: "admin", Description: "Tenant administrator", Builtin: true, Permissions: allPermissions},
		{ID: NewID("role"), TenantID: tenantID, Name: "auditor", Description: "Read audit and integrity information", Builtin: true, Permissions: []string{PermissionAuditView}},
		{ID: NewID("role"), TenantID: tenantID, Name: "operator", Description: "Operate data sources, strategies, and queries", Builtin: true, Permissions: []string{PermissionDatasourceManage, PermissionStrategyManage, PermissionQueryExecute}},
		{ID: NewID("role"), TenantID: tenantID, Name: "viewer", Description: "Read-only audit viewer", Builtin: true, Permissions: []string{PermissionAuditView}},
	}
	created := make([]model.Role, 0, len(presets))
	for _, role := range presets {
		item, err := service.repository.CreateRole(ctx, role)
		if err != nil {
			return nil, fmt.Errorf("provision tenant role %q: %w", role.Name, err)
		}
		created = append(created, item)
	}
	return created, nil
}

func (service *Service) Authenticate(ctx context.Context, tenantID, username, password string) (Principal, error) {
	if service == nil || service.repository == nil || ctx == nil || strings.TrimSpace(tenantID) == "" || username == "" || password == "" {
		return Principal{}, ErrInvalidCredentials
	}
	user, err := service.repository.GetUserByUsername(ctx, tenantID, username)
	if err != nil || user.Status != "active" || user.AuthProvider != "local" || !VerifyPassword(user.PasswordHash, password) {
		return Principal{}, ErrInvalidCredentials
	}
	return service.Principal(ctx, tenantID, user.ID)
}

// Principal re-loads user, tenant, roles, inheritance, and permissions on each
// request. Revocations therefore take effect immediately even for live tokens.
func (service *Service) Principal(ctx context.Context, tenantID, userID string) (Principal, error) {
	if service == nil || service.repository == nil || ctx == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(userID) == "" {
		return Principal{}, ErrForbidden
	}
	tenant, err := service.repository.GetTenant(ctx, tenantID)
	if err != nil || tenant.Status != "active" {
		return Principal{}, ErrForbidden
	}
	user, err := service.repository.GetUser(ctx, tenantID, userID)
	if err != nil || user.Status != "active" {
		return Principal{}, ErrForbidden
	}
	assigned, err := service.repository.UserRoleIDs(ctx, tenantID, userID)
	if err != nil || len(assigned) == 0 {
		return Principal{}, ErrForbidden
	}
	roles, err := service.repository.ListRoles(ctx, tenantID)
	if err != nil {
		return Principal{}, ErrForbidden
	}
	permissions, expandedRoles, err := resolvePermissions(roles, assigned)
	if err != nil || len(permissions) == 0 {
		return Principal{}, ErrForbidden
	}
	return Principal{UserID: user.ID, TenantID: user.TenantID, Username: user.Username,
		Permissions: permissions, RoleIDs: expandedRoles}, nil
}

func (service *Service) Authorize(ctx context.Context, tenantID, userID, permission string) (Principal, error) {
	principal, err := service.Principal(ctx, tenantID, userID)
	if err != nil || !principal.Has(permission) {
		return Principal{}, ErrForbidden
	}
	return principal, nil
}

func resolvePermissions(roles []model.Role, assigned []string) ([]string, []string, error) {
	byID := make(map[string]model.Role, len(roles))
	for _, role := range roles {
		if strings.TrimSpace(role.ID) == "" || strings.TrimSpace(role.TenantID) == "" {
			return nil, nil, ErrForbidden
		}
		byID[role.ID] = role
	}
	state := make(map[string]uint8, len(roles))
	grants := make(map[string]struct{})
	visited := make(map[string]struct{})
	var visit func(string) error
	visit = func(id string) error {
		role, ok := byID[id]
		if !ok || state[id] == 1 {
			return ErrForbidden
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		visited[id] = struct{}{}
		for _, permission := range role.Permissions {
			if strings.TrimSpace(permission) == "" {
				return ErrForbidden
			}
			grants[permission] = struct{}{}
		}
		for _, parent := range role.ParentIDs {
			if err := visit(parent); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, roleID := range assigned {
		if err := visit(roleID); err != nil {
			return nil, nil, err
		}
	}
	permissions := make([]string, 0, len(grants))
	for permission := range grants {
		permissions = append(permissions, permission)
	}
	roleIDs := make([]string, 0, len(visited))
	for roleID := range visited {
		roleIDs = append(roleIDs, roleID)
	}
	sort.Strings(permissions)
	sort.Strings(roleIDs)
	return permissions, roleIDs, nil
}

func NewID(prefix string) string { return prefix + "_" + uuid.NewString() }

func AllPermissions() []string { return append([]string(nil), allPermissions...) }
