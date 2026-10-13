import { DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import { Alert, Button, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, message } from "antd";
import { useCallback, useEffect, useState } from "react";

import { createRole, deleteRole, listPermissions, listRoles, updateRole, type PermissionView, type RoleView } from "@/api/governance";
import { PageContainer } from "@/components/PageContainer";

import { ScopeStatus, TenantPicker, useTenantScope } from "./governance/scope";

interface RoleForm {
  name: string;
  description: string;
  permissions: string[];
  parent_role_ids: string[];
}

export function Roles() {
  const scope = useTenantScope();
  const allowed = Boolean(scope.principal?.permissions.includes("role.manage"));
  const [form] = Form.useForm<RoleForm>();
  const [roles, setRoles] = useState<RoleView[]>([]);
  const [permissions, setPermissions] = useState<PermissionView[]>([]);
  const [editing, setEditing] = useState<RoleView | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);

  const reload = useCallback(async () => {
    if (!scope.tenantID || !allowed) return;
    const [nextRoles, nextPermissions] = await Promise.all([listRoles(scope.tenantID), listPermissions()]);
    setRoles(nextRoles);
    setPermissions(nextPermissions);
    setError(false);
  }, [scope.tenantID, allowed]);

  useEffect(() => {
    let active = true;
    if (!scope.tenantID || !allowed) return;
    void Promise.all([listRoles(scope.tenantID), listPermissions()])
      .then(([nextRoles, nextPermissions]) => { if (active) { setRoles(nextRoles); setPermissions(nextPermissions); setError(false); } })
      .catch(() => { if (active) setError(true); });
    return () => { active = false; };
  }, [scope.tenantID, allowed]);

  const beginCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ permissions: [], parent_role_ids: [], description: "" });
    setOpen(true);
  };
  const beginEdit = (role: RoleView) => {
    setEditing(role);
    form.setFieldsValue({ name: role.name, description: role.description, permissions: role.permissions, parent_role_ids: role.parent_role_ids });
    setOpen(true);
  };
  const save = async () => {
    let values: RoleForm;
    try { values = await form.validateFields(); } catch { return; }
    setBusy(true);
    const data = { tenant_id: scope.tenantID, name: values.name.trim(), description: values.description?.trim() || "", permissions: values.permissions || [], parent_role_ids: values.parent_role_ids || [] };
    try {
      if (editing) await updateRole(editing.id, data);
      else await createRole(data);
      setOpen(false);
      void message.success(editing ? "角色已更新" : "角色已创建");
      await reload();
    } catch { /* API client shows the server error. */ } finally { setBusy(false); }
  };
  const remove = async (role: RoleView) => {
    setBusy(true);
    try { await deleteRole(role.id, scope.tenantID); void message.success("角色已删除"); await reload(); }
    catch { /* API client shows the server error. */ } finally { setBusy(false); }
  };

  return (
    <PageContainer title="角色管理" subtitle="角色控制人类管理员可操作的控制台 API；Agent 查询权限仍由数据源策略决定。" extra={<Space><TenantPicker tenants={scope.tenants} tenantID={scope.tenantID} onChange={scope.setTenantID} />{allowed && <Button type="primary" icon={<PlusOutlined />} onClick={beginCreate}>新建角色</Button>}</Space>}>
      <ScopeStatus loading={scope.loading} error={scope.error} allowed={allowed} />
      {allowed && !scope.loading && <>
        {error && <Alert type="error" showIcon message="角色或权限列表读取失败" action={<Button size="small" onClick={() => void reload()}>重试</Button>} />}
        <Table rowKey="id" dataSource={roles} pagination={{ pageSize: 20 }} columns={[
          { title: "角色", dataIndex: "name", render: (value: string, role: RoleView) => <Space>{value}{role.builtin && <Tag>内置</Tag>}</Space> },
          { title: "说明", dataIndex: "description" },
          { title: "权限", dataIndex: "permissions", render: (items: string[]) => items.map((item) => <Tag key={item}>{item}</Tag>) },
          { title: "继承", dataIndex: "parent_role_ids", render: (ids: string[]) => ids.map((id) => <Tag key={id}>{roles.find((role) => role.id === id)?.name || id}</Tag>) },
          { title: "操作", render: (_: unknown, role: RoleView) => <Space><Button size="small" icon={<EditOutlined />} disabled={role.builtin} onClick={() => beginEdit(role)}>编辑</Button><Popconfirm title="删除这个角色？" onConfirm={() => void remove(role)} disabled={role.builtin}><Button size="small" danger icon={<DeleteOutlined />} disabled={role.builtin || busy}>删除</Button></Popconfirm></Space> },
        ]} />
      </>}
      <Modal title={editing ? "编辑角色" : "新建角色"} open={open} onCancel={() => setOpen(false)} onOk={() => void save()} confirmLoading={busy} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: "请输入角色名称" }]}><Input /></Form.Item>
          <Form.Item name="description" label="说明"><Input /></Form.Item>
          <Form.Item name="permissions" label="直接权限"><Select mode="multiple" options={permissions.map((item) => ({ value: item.code, label: `${item.code} · ${item.description}` }))} /></Form.Item>
          <Form.Item name="parent_role_ids" label="继承角色" extra="继承角色的权限也会生效。"><Select mode="multiple" options={roles.filter((role) => role.id !== editing?.id).map((role) => ({ value: role.id, label: role.name }))} /></Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  );
}
