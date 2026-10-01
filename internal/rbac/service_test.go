package rbac

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPasswordHashVerification(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	require.NotContains(t, hash, "correct horse")
	require.True(t, VerifyPassword(hash, "correct horse battery staple"))
	require.False(t, VerifyPassword(hash, "wrong password"))
	require.False(t, VerifyPassword("malformed", "correct horse battery staple"))
}

func TestAuthorizationMultipleRolesInheritanceAndDefaultDeny(t *testing.T) {
	service, repository := testService(t)
	ctx := context.Background()
	_, err := repository.CreateTenant(ctx, model.Tenant{ID: "tenant_a", Name: "A", Status: "active"})
	require.NoError(t, err)
	_, err = repository.CreateRole(ctx, model.Role{ID: "parent", TenantID: "tenant_a", Name: "parent", Permissions: []string{PermissionAuditView}})
	require.NoError(t, err)
	_, err = repository.CreateRole(ctx, model.Role{ID: "child", TenantID: "tenant_a", Name: "child", Permissions: []string{PermissionQueryExecute}, ParentIDs: []string{"parent"}})
	require.NoError(t, err)
	_, err = repository.CreateRole(ctx, model.Role{ID: "extra", TenantID: "tenant_a", Name: "extra", Permissions: []string{PermissionUserManage}})
	require.NoError(t, err)
	hash, err := HashPassword("this is a strong test password")
	require.NoError(t, err)
	_, err = repository.CreateUser(ctx, model.User{ID: "user_a", TenantID: "tenant_a", Username: "alice", DisplayName: "Alice", PasswordHash: hash, Status: "active", AuthProvider: "local"})
	require.NoError(t, err)
	require.NoError(t, repository.SetUserRoles(ctx, "tenant_a", "user_a", []string{"child", "extra"}))

	principal, err := service.Principal(ctx, "tenant_a", "user_a")
	require.NoError(t, err)
	require.True(t, principal.Has(PermissionAuditView))
	require.True(t, principal.Has(PermissionQueryExecute))
	require.True(t, principal.Has(PermissionUserManage))
	require.False(t, principal.Has(PermissionTenantManage))
	_, err = service.Authorize(ctx, "tenant_a", "user_a", PermissionTenantManage)
	require.ErrorIs(t, err, ErrForbidden)
}

func TestTenantIsolationAndInheritanceCycleFailClosed(t *testing.T) {
	service, repository := testService(t)
	ctx := context.Background()
	for _, tenant := range []model.Tenant{{ID: "tenant_a", Name: "A", Status: "active"}, {ID: "tenant_b", Name: "B", Status: "active"}} {
		_, err := repository.CreateTenant(ctx, tenant)
		require.NoError(t, err)
	}
	_, err := repository.CreateRole(ctx, model.Role{ID: "a1", TenantID: "tenant_a", Name: "a1", Permissions: []string{PermissionAuditView}})
	require.NoError(t, err)
	_, err = repository.CreateRole(ctx, model.Role{ID: "a2", TenantID: "tenant_a", Name: "a2", ParentIDs: []string{"a1"}})
	require.NoError(t, err)
	require.NoError(t, repository.SetRoleParents(ctx, "tenant_a", "a1", []string{"a2"}))
	hash, err := HashPassword("another strong test password")
	require.NoError(t, err)
	_, err = repository.CreateUser(ctx, model.User{ID: "same", TenantID: "tenant_a", Username: "same", DisplayName: "A", PasswordHash: hash, Status: "active", AuthProvider: "local"})
	require.NoError(t, err)
	require.NoError(t, repository.SetUserRoles(ctx, "tenant_a", "same", []string{"a1"}))
	_, err = service.Principal(ctx, "tenant_a", "same")
	require.ErrorIs(t, err, ErrForbidden)
	_, err = service.Principal(ctx, "tenant_b", "same")
	require.ErrorIs(t, err, ErrForbidden)
}

func testService(t *testing.T) (*Service, *store.RBACRepository) {
	t.Helper()
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	opened, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rbac.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	return NewService(opened.RBAC()), opened.RBAC()
}
