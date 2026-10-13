import { EditOutlined, PlusOutlined } from "@ant-design/icons";
import { Alert, Button, Form, Input, Modal, Select, Space, Table, Tag, message } from "antd";
import { useCallback, useEffect, useState } from "react";

import { createTenant, currentPrincipal, listTenants, updateTenant, type TenantView } from "@/api/governance";
import { PageContainer } from "@/components/PageContainer";

interface TenantForm { name: string; status: string }

export function Tenants() {
  const [form] = Form.useForm<TenantForm>();
  const [allowed, setAllowed] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [tenants, setTenants] = useState<TenantView[]>([]);
  const [editing, setEditing] = useState<TenantView | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  const reload = useCallback(async () => { setTenants(await listTenants()); setError(false); }, []);
  useEffect(() => {
    let active = true;
    void currentPrincipal().then(async (principal) => {
      const canManage = principal.id === "user_bootstrap_admin" && principal.tenant_id === "tenant_default" && principal.permissions.includes("tenant.manage");
      if (!active) return;
      setAllowed(canManage);
      if (canManage) {
        const items = await listTenants();
        if (active) setTenants(items);
      }
    }).catch(() => { if (active) setError(true); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, []);

  const beginCreate = () => { setEditing(null); form.resetFields(); setOpen(true); };
  const beginEdit = (tenant: TenantView) => { setEditing(tenant); form.setFieldsValue({ name: tenant.name, status: tenant.status }); setOpen(true); };
  const save = async () => {
    let values: TenantForm;
    try { values = await form.validateFields(); } catch { return; }
    setBusy(true);
    try {
      if (editing) await updateTenant(editing.id, { name: values.name.trim(), status: editing.id === "tenant_default" ? "active" : values.status });
      else await createTenant(values.name.trim());
      setOpen(false);
      void message.success(editing ? "租户已更新" : "租户已创建");
      await reload();
    } catch { /* API client shows the server error. */ } finally { setBusy(false); }
  };

  return (
    <PageContainer title="租户管理" subtitle="租户隔离人类账号、Agent、数据源、策略和审批。仅初始管理员可跨租户管理。" extra={allowed && <Button type="primary" icon={<PlusOutlined />} onClick={beginCreate}>新建租户</Button>}>
      {loading && <span>加载中…</span>}
      {error && <Alert type="error" showIcon message="无法读取租户" action={<Button size="small" onClick={() => void reload()}>重试</Button>} />}
      {!loading && !error && !allowed && <Alert type="warning" showIcon message="只有初始管理员可以管理租户" />}
      {allowed && !loading && <Table rowKey="id" dataSource={tenants} pagination={{ pageSize: 20 }} columns={[
        { title: "租户 ID", dataIndex: "id", render: (value: string) => <code>{value}</code> },
        { title: "名称", dataIndex: "name" },
        { title: "状态", dataIndex: "status", render: (value: string) => <Tag color={value === "active" ? "green" : "default"}>{value === "active" ? "启用" : "停用"}</Tag> },
        { title: "操作", render: (_: unknown, tenant: TenantView) => <Space><Button size="small" icon={<EditOutlined />} onClick={() => beginEdit(tenant)}>编辑</Button></Space> },
      ]} />}
      <Modal title={editing ? "编辑租户" : "新建租户"} open={open} onCancel={() => setOpen(false)} onOk={() => void save()} confirmLoading={busy} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="租户名称" rules={[{ required: true, message: "请输入名称" }]}><Input /></Form.Item>
          {editing && <Form.Item name="status" label="状态"><Select disabled={editing.id === "tenant_default"} options={[{ value: "active", label: "启用" }, { value: "disabled", label: "停用" }]} /></Form.Item>}
        </Form>
      </Modal>
    </PageContainer>
  );
}
