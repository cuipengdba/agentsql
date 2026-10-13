import { DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import { Alert, Button, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, message } from "antd";
import { useCallback, useEffect, useState } from "react";

import { createUser, deleteUser, listRoles, listUsers, updateUser, type RoleView, type UserView } from "@/api/governance";
import { PageContainer } from "@/components/PageContainer";

import { ScopeStatus, TenantPicker, useTenantScope } from "./governance/scope";

interface UserForm {
  username: string;
  display_name: string;
  password?: string;
  status: string;
  role_ids: string[];
}

export function Users() {
  const scope = useTenantScope();
  const allowed = Boolean(scope.principal?.permissions.includes("user.manage"));
  const canAssignRoles = Boolean(scope.principal?.permissions.includes("role.manage"));
  const [form] = Form.useForm<UserForm>();
  const [users, setUsers] = useState<UserView[]>([]);
  const [roles, setRoles] = useState<RoleView[]>([]);
  const [editing, setEditing] = useState<UserView | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);

  const reload = useCallback(async () => {
    if (!scope.tenantID || !allowed) return;
    const [nextUsers, nextRoles] = await Promise.all([listUsers(scope.tenantID), canAssignRoles ? listRoles(scope.tenantID) : Promise.resolve([])]);
    setUsers(nextUsers);
    setRoles(nextRoles);
    setError(false);
  }, [scope.tenantID, allowed, canAssignRoles]);

  useEffect(() => {
    let active = true;
    if (!scope.tenantID || !allowed) return;
    void Promise.all([listUsers(scope.tenantID), canAssignRoles ? listRoles(scope.tenantID) : Promise.resolve([])])
      .then(([nextUsers, nextRoles]) => { if (active) { setUsers(nextUsers); setRoles(nextRoles); setError(false); } })
      .catch(() => { if (active) setError(true); });
    return () => { active = false; };
  }, [scope.tenantID, allowed, canAssignRoles]);

  const beginCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ status: "active", role_ids: [] });
    setOpen(true);
  };
  const beginEdit = (user: UserView) => {
    setEditing(user);
    form.setFieldsValue({ username: user.username, display_name: user.display_name, status: user.status, role_ids: user.role_ids });
    setOpen(true);
  };
  const save = async () => {
    let values: UserForm;
    try { values = await form.validateFields(); } catch { return; }
    setBusy(true);
    try {
      if (editing) {
        await updateUser(editing.id, { tenant_id: scope.tenantID, display_name: values.display_name.trim(), status: values.status, ...(canAssignRoles ? { role_ids: values.role_ids } : {}) });
      } else {
        await createUser({ tenant_id: scope.tenantID, username: values.username.trim(), display_name: values.display_name.trim(), password: values.password || "", ...(canAssignRoles ? { role_ids: values.role_ids } : {}) });
      }
      setOpen(false);
      void message.success(editing ? "用户已更新" : "用户已创建");
      await reload();
    } catch { /* API client shows the server error. */ } finally { setBusy(false); }
  };
  const remove = async (user: UserView) => {
    setBusy(true);
    try { await deleteUser(user.id, scope.tenantID); void message.success("用户已删除"); await reload(); }
    catch { /* API client shows the server error. */ } finally { setBusy(false); }
  };

  return (
    <PageContainer title="用户管理" subtitle="为控制台成员分配本租户角色。Agent 的数据库权限仍在权限页单独配置。" extra={<Space><TenantPicker tenants={scope.tenants} tenantID={scope.tenantID} onChange={scope.setTenantID} />{allowed && <Button type="primary" icon={<PlusOutlined />} onClick={beginCreate}>新建用户</Button>}</Space>}>
      <ScopeStatus loading={scope.loading} error={scope.error} allowed={allowed} />
      {allowed && !scope.loading && <>
        {error && <Alert type="error" showIcon message="用户或角色列表读取失败" action={<Button size="small" onClick={() => void reload()}>重试</Button>} />}
        <Table rowKey="id" dataSource={users} pagination={{ pageSize: 20 }} columns={[
          { title: "用户名", dataIndex: "username" },
          { title: "显示名", dataIndex: "display_name" },
          { title: "认证方式", dataIndex: "auth_provider", render: (value: string) => <Tag>{value}</Tag> },
          { title: "状态", dataIndex: "status", render: (value: string) => <Tag color={value === "active" ? "green" : "default"}>{value === "active" ? "启用" : "停用"}</Tag> },
          { title: "角色", dataIndex: "role_ids", render: (ids: string[]) => ids.map((id) => <Tag key={id}>{roles.find((role) => role.id === id)?.name || id}</Tag>) },
          { title: "操作", render: (_: unknown, user: UserView) => <Space><Button size="small" icon={<EditOutlined />} onClick={() => beginEdit(user)}>编辑</Button><Popconfirm title="删除这个用户？" onConfirm={() => void remove(user)} disabled={user.id === scope.principal?.id}><Button size="small" danger icon={<DeleteOutlined />} disabled={user.id === scope.principal?.id || busy}>删除</Button></Popconfirm></Space> },
        ]} />
      </>}
      <Modal title={editing ? "编辑用户" : "新建本地用户"} open={open} onCancel={() => setOpen(false)} onOk={() => void save()} confirmLoading={busy} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: "请输入用户名" }]}><Input disabled={Boolean(editing)} autoComplete="off" /></Form.Item>
          <Form.Item name="display_name" label="显示名" rules={[{ required: true, message: "请输入显示名" }]}><Input /></Form.Item>
          {!editing && <Form.Item name="password" label="初始密码" rules={[{ required: true, min: 12, message: "至少 12 个字符" }]}><Input.Password autoComplete="new-password" /></Form.Item>}
          {editing && <Form.Item name="status" label="状态"><Select options={[{ value: "active", label: "启用" }, { value: "disabled", label: "停用" }]} /></Form.Item>}
          {canAssignRoles && <Form.Item name="role_ids" label="角色" extra="可选多个角色；不选择时无法访问受保护的管理 API。"><Select mode="multiple" options={roles.map((role) => ({ value: role.id, label: role.name }))} /></Form.Item>}
        </Form>
      </Modal>
    </PageContainer>
  );
}
