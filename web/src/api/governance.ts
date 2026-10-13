import { request } from "./client";

export interface PrincipalView {
  id: string;
  tenant_id: string;
  username: string;
  roles: string[];
  permissions: string[];
}

export interface TenantView {
  id: string;
  name: string;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface UserView {
  id: string;
  tenant_id: string;
  username: string;
  display_name: string;
  status: string;
  auth_provider: string;
  role_ids: string[];
}

export interface RoleView {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  builtin: boolean;
  parent_role_ids: string[];
  permissions: string[];
}

export interface PermissionView {
  code: string;
  description: string;
}

const tenantParams = (tenantID: string) => ({ tenant_id: tenantID });

export const currentPrincipal = () => request<PrincipalView>({ method: "GET", url: "/auth/me" });
export const listTenants = () => request<TenantView[]>({ method: "GET", url: "/tenants" });
export const createTenant = (name: string) => request<TenantView>({ method: "POST", url: "/tenants", data: { name } });
export const updateTenant = (id: string, data: { name?: string; status?: string }) => request<TenantView>({ method: "PUT", url: `/tenants/${encodeURIComponent(id)}`, data });

export const listUsers = (tenantID: string) => request<UserView[]>({ method: "GET", url: "/users", params: tenantParams(tenantID) });
export const createUser = (data: { tenant_id: string; username: string; display_name: string; password: string; role_ids?: string[] }) =>
  request<UserView>({ method: "POST", url: "/users", data });
export const updateUser = (id: string, data: { tenant_id: string; display_name?: string; status?: string; role_ids?: string[] }) =>
  request<UserView>({ method: "PUT", url: `/users/${encodeURIComponent(id)}`, data });
export const deleteUser = (id: string, tenantID: string) =>
  request<{ deleted: boolean }>({ method: "DELETE", url: `/users/${encodeURIComponent(id)}`, params: tenantParams(tenantID) });

export const listRoles = (tenantID: string) => request<RoleView[]>({ method: "GET", url: "/roles", params: tenantParams(tenantID) });
export const listPermissions = () => request<PermissionView[]>({ method: "GET", url: "/permissions" });
export const createRole = (data: { tenant_id: string; name: string; description: string; permissions: string[]; parent_role_ids: string[] }) =>
  request<RoleView>({ method: "POST", url: "/roles", data });
export const updateRole = (id: string, data: { tenant_id: string; name: string; description: string; permissions: string[]; parent_role_ids: string[] }) =>
  request<RoleView>({ method: "PUT", url: `/roles/${encodeURIComponent(id)}`, data });
export const deleteRole = (id: string, tenantID: string) =>
  request<{ deleted: boolean }>({ method: "DELETE", url: `/roles/${encodeURIComponent(id)}`, params: tenantParams(tenantID) });
