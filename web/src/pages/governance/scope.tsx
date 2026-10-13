import { Alert, Select, Spin } from "antd";
import { useEffect, useState } from "react";

import { currentPrincipal, listTenants, type PrincipalView, type TenantView } from "@/api/governance";

export function useTenantScope() {
  const [principal, setPrincipal] = useState<PrincipalView | null>(null);
  const [tenants, setTenants] = useState<TenantView[]>([]);
  const [tenantID, setTenantID] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  useEffect(() => {
    let active = true;
    void currentPrincipal().then(async (identity) => {
      const bootstrap = identity.id === "user_bootstrap_admin" && identity.tenant_id === "tenant_default" && identity.permissions.includes("tenant.manage");
      const visible = bootstrap ? await listTenants() : [{ id: identity.tenant_id, name: identity.tenant_id, status: "active", created_at: "", updated_at: "" }];
      if (!active) return;
      setPrincipal(identity);
      setTenants(visible);
      setTenantID(identity.tenant_id);
    }).catch(() => { if (active) setError(true); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, []);

  return { principal, tenants, tenantID, setTenantID, loading, error };
}

export function ScopeStatus({ loading, error, allowed }: { loading: boolean; error: boolean; allowed: boolean }) {
  if (loading) return <Spin />;
  if (error) return <Alert type="error" showIcon message="无法读取当前账号或租户，请刷新后重试" />;
  if (!allowed) return <Alert type="warning" showIcon message="当前账号没有管理此页面的权限" />;
  return null;
}

export function TenantPicker({ tenants, tenantID, onChange }: { tenants: TenantView[]; tenantID: string; onChange: (id: string) => void }) {
  if (tenants.length <= 1) return null;
  return <Select aria-label="选择租户" value={tenantID} onChange={onChange} style={{ minWidth: 220 }} options={tenants.map((item) => ({ value: item.id, label: item.name }))} />;
}
