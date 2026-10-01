package adminapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
)

type userView struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenant_id"`
	Username     string   `json:"username"`
	DisplayName  string   `json:"display_name"`
	Status       string   `json:"status"`
	AuthProvider string   `json:"auth_provider"`
	RoleIDs      []string `json:"role_ids"`
}

type userInput struct {
	TenantID    string    `json:"tenant_id,omitempty"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Password    *string   `json:"password,omitempty"`
	Status      string    `json:"status,omitempty"`
	RoleIDs     *[]string `json:"role_ids,omitempty"`
}

type roleInput struct {
	TenantID    string   `json:"tenant_id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	ParentIDs   []string `json:"parent_role_ids,omitempty"`
}

func (handler *Handler) permissionsList(writer http.ResponseWriter, request *http.Request) {
	items, err := handler.deps.Runtime.Store.RBAC().ListPermissions(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, items)
}

func (handler *Handler) usersList(writer http.ResponseWriter, request *http.Request) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	tenantID, ok := handler.targetTenant(writer, principal, request.URL.Query().Get("tenant_id"))
	if !ok {
		return
	}
	items, err := handler.deps.Runtime.Store.RBAC().ListUsers(request.Context(), tenantID)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	views := make([]userView, 0, len(items))
	for _, item := range items {
		view, viewErr := handler.userView(request, item)
		if viewErr != nil {
			handler.internal(writer, viewErr)
			return
		}
		views = append(views, view)
	}
	handler.ok(writer, views)
}

func (handler *Handler) usersCreate(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	var input userInput
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID, ok := handler.targetTenant(writer, principal, input.TenantID)
	if !ok {
		return
	}
	if input.Password == nil || utf8.RuneCountInString(*input.Password) < 12 || strings.TrimSpace(input.Username) == "" {
		handler.fail(writer, http.StatusUnprocessableEntity, "username and a password of at least 12 characters are required")
		return
	}
	hash, err := rbac.HashPassword(*input.Password)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid password")
		return
	}
	status := input.Status
	if status == "" {
		status = "active"
	}
	created, err := handler.deps.Runtime.Store.RBAC().CreateUser(request.Context(), model.User{
		ID: rbac.NewID("user"), TenantID: tenantID, Username: input.Username, DisplayName: input.DisplayName,
		PasswordHash: hash, Status: status, AuthProvider: "local",
	})
	if err != nil {
		handler.fail(writer, http.StatusConflict, "user already exists or is invalid")
		return
	}
	if input.RoleIDs != nil {
		if err := handler.deps.Runtime.Store.RBAC().SetUserRoles(request.Context(), tenantID, created.ID, *input.RoleIDs); err != nil {
			_ = handler.deps.Runtime.Store.RBAC().DeleteUser(request.Context(), tenantID, created.ID)
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid role assignment")
			return
		}
	}
	view, err := handler.userView(request, created)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, view)
}

func (handler *Handler) usersGet(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	tenantID, ok := handler.targetTenant(writer, principal, request.URL.Query().Get("tenant_id"))
	if !ok {
		return
	}
	item, err := handler.deps.Runtime.Store.RBAC().GetUser(request.Context(), tenantID, request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	view, err := handler.userView(request, item)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, view)
}

func (handler *Handler) usersUpdate(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	var input userInput
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID, ok := handler.targetTenant(writer, principal, input.TenantID)
	if !ok {
		return
	}
	repository := handler.deps.Runtime.Store.RBAC()
	item, err := repository.GetUser(request.Context(), tenantID, request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	if input.Username != "" {
		item.Username = input.Username
	}
	if input.DisplayName != "" {
		item.DisplayName = input.DisplayName
	}
	if input.Status != "" {
		item.Status = input.Status
	}
	if input.Password != nil {
		if utf8.RuneCountInString(*input.Password) < 12 {
			handler.fail(writer, http.StatusUnprocessableEntity, "password must be at least 12 characters")
			return
		}
		item.PasswordHash, err = rbac.HashPassword(*input.Password)
		if err != nil {
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid password")
			return
		}
	}
	item, err = repository.UpdateUser(request.Context(), item)
	if err != nil {
		handler.fail(writer, http.StatusConflict, "user update failed")
		return
	}
	if input.RoleIDs != nil {
		if err := repository.SetUserRoles(request.Context(), tenantID, item.ID, *input.RoleIDs); err != nil {
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid role assignment")
			return
		}
	}
	view, err := handler.userView(request, item)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, view)
}

func (handler *Handler) usersSetRoles(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	var input struct {
		TenantID string   `json:"tenant_id,omitempty"`
		RoleIDs  []string `json:"role_ids"`
	}
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID, ok := handler.targetTenant(writer, principal, input.TenantID)
	if !ok {
		return
	}
	if err := handler.deps.Runtime.Store.RBAC().SetUserRoles(request.Context(), tenantID, request.PathValue("id"), input.RoleIDs); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid role assignment")
		return
	}
	handler.ok(writer, map[string]bool{"updated": true})
}

func (handler *Handler) usersDelete(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	tenantID, ok := handler.targetTenant(writer, principal, request.URL.Query().Get("tenant_id"))
	if !ok {
		return
	}
	if request.PathValue("id") == principal.UserID {
		handler.fail(writer, http.StatusConflict, "cannot delete the current user")
		return
	}
	if err := handler.deps.Runtime.Store.RBAC().DeleteUser(request.Context(), tenantID, request.PathValue("id")); err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func (handler *Handler) userView(request *http.Request, item model.User) (userView, error) {
	roles, err := handler.deps.Runtime.Store.RBAC().UserRoleIDs(request.Context(), item.TenantID, item.ID)
	if err != nil {
		return userView{}, err
	}
	if roles == nil {
		roles = make([]string, 0)
	}
	return userView{ID: item.ID, TenantID: item.TenantID, Username: item.Username,
		DisplayName: item.DisplayName, Status: item.Status, AuthProvider: item.AuthProvider, RoleIDs: roles}, nil
}

func (handler *Handler) rolesList(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	tenantID, ok := handler.targetTenant(writer, principal, request.URL.Query().Get("tenant_id"))
	if !ok {
		return
	}
	items, err := handler.deps.Runtime.Store.RBAC().ListRoles(request.Context(), tenantID)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, items)
}

func (handler *Handler) rolesCreate(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	var input roleInput
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID, ok := handler.targetTenant(writer, principal, input.TenantID)
	if !ok {
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		handler.fail(writer, http.StatusUnprocessableEntity, "role name is required")
		return
	}
	item, err := handler.deps.Runtime.Store.RBAC().CreateRole(request.Context(), model.Role{
		ID: rbac.NewID("role"), TenantID: tenantID, Name: input.Name, Description: input.Description,
		Permissions: input.Permissions, ParentIDs: input.ParentIDs,
	})
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid role, permission, or parent role")
		return
	}
	handler.ok(writer, item)
}

func (handler *Handler) rolesGet(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	tenantID, ok := handler.targetTenant(writer, principal, request.URL.Query().Get("tenant_id"))
	if !ok {
		return
	}
	item, err := handler.deps.Runtime.Store.RBAC().GetRole(request.Context(), tenantID, request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, item)
}

func (handler *Handler) rolesUpdate(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	var input roleInput
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID, ok := handler.targetTenant(writer, principal, input.TenantID)
	if !ok {
		return
	}
	repository := handler.deps.Runtime.Store.RBAC()
	item, err := repository.GetRole(request.Context(), tenantID, request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	if input.Name != "" {
		item.Name = input.Name
	}
	item.Description, item.Permissions, item.ParentIDs = input.Description, input.Permissions, input.ParentIDs
	item, err = repository.UpdateRole(request.Context(), item)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid role, permission, or parent role")
		return
	}
	handler.ok(writer, item)
}

func (handler *Handler) rolesSetPermissions(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	var input struct {
		TenantID    string   `json:"tenant_id,omitempty"`
		Permissions []string `json:"permissions"`
	}
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID, ok := handler.targetTenant(writer, principal, input.TenantID)
	if !ok {
		return
	}
	if err := handler.deps.Runtime.Store.RBAC().SetRolePermissions(request.Context(), tenantID, request.PathValue("id"), input.Permissions); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid permission assignment")
		return
	}
	handler.ok(writer, map[string]bool{"updated": true})
}

func (handler *Handler) rolesDelete(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	tenantID, ok := handler.targetTenant(writer, principal, request.URL.Query().Get("tenant_id"))
	if !ok {
		return
	}
	if err := handler.deps.Runtime.Store.RBAC().DeleteRole(request.Context(), tenantID, request.PathValue("id")); err != nil {
		handler.fail(writer, http.StatusConflict, "role cannot be deleted")
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func (handler *Handler) tenantsList(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	if principal.IsBootstrapAdmin() {
		items, err := handler.deps.Runtime.Store.RBAC().ListTenants(request.Context())
		if err != nil {
			handler.internal(writer, err)
			return
		}
		handler.ok(writer, items)
		return
	}
	item, err := handler.deps.Runtime.Store.RBAC().GetTenant(request.Context(), principal.TenantID)
	if err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, []model.Tenant{item})
}

func (handler *Handler) tenantsCreate(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	if !principal.IsBootstrapAdmin() {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	var input struct {
		ID   string `json:"id,omitempty"`
		Name string `json:"name"`
	}
	if decodeJSON(writer, request, &input) != nil || strings.TrimSpace(input.Name) == "" {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.ID == "" {
		input.ID = rbac.NewID("tenant")
	}
	item, err := handler.deps.Runtime.Store.RBAC().CreateTenant(request.Context(), model.Tenant{ID: input.ID, Name: input.Name, Status: "active"})
	if err != nil {
		handler.fail(writer, http.StatusConflict, "tenant already exists or is invalid")
		return
	}
	if _, err := handler.rbac.ProvisionTenant(request.Context(), item.ID); err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, item)
}

func (handler *Handler) tenantsGet(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	id := request.PathValue("id")
	if id != principal.TenantID && !principal.IsBootstrapAdmin() {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	item, err := handler.deps.Runtime.Store.RBAC().GetTenant(request.Context(), id)
	if err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, item)
}

func (handler *Handler) tenantsUpdate(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	id := request.PathValue("id")
	if id != principal.TenantID && !principal.IsBootstrapAdmin() {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	var input struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	repository := handler.deps.Runtime.Store.RBAC()
	item, err := repository.GetTenant(request.Context(), id)
	if err != nil {
		handler.notFound(writer)
		return
	}
	if input.Name != "" {
		item.Name = input.Name
	}
	if input.Status != "" {
		item.Status = input.Status
	}
	item, err = repository.UpdateTenant(request.Context(), item)
	if err != nil {
		handler.fail(writer, http.StatusConflict, "tenant update failed")
		return
	}
	handler.ok(writer, item)
}

func (handler *Handler) tenantsDelete(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request)
	id := request.PathValue("id")
	if !principal.IsBootstrapAdmin() || id == rbac.DefaultTenantID {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	if err := handler.deps.Runtime.Store.RBAC().DeleteTenant(request.Context(), id); err != nil {
		handler.fail(writer, http.StatusConflict, "tenant is not empty or does not exist")
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func (handler *Handler) targetTenant(writer http.ResponseWriter, principal rbac.Principal, requested string) (string, bool) {
	if principal.TenantID == "" {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	if requested == "" {
		return principal.TenantID, true
	}
	if requested != principal.TenantID && !principal.IsBootstrapAdmin() {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return "", false
	}
	return requested, true
}
