import { CopyOutlined, DeleteOutlined, EditOutlined, KeyOutlined, PlusOutlined } from "@ant-design/icons";
import { Alert, Button, DatePicker, Drawer, Form, Input, Modal, Pagination, Popconfirm, Radio, Space, Steps, Table, Tag, message } from "antd";
import type { TableProps } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { useCallback, useEffect, useRef, useState } from "react";

import { createAgent, deleteAgent, listAgents, rotateAgentKey, updateAgent } from "@/api/agents";
import type { AgentCreateInput, AgentUpdateInput, AgentView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { agentLevelMeta, agentStatusMeta, configLabel } from "@/constants/labels";

import { apiErrorMessage, copyText, formatDateTime, httpStatus, isCanceled } from "./config/utils";

interface AgentFormValues {
  id: string;
  name: string;
  owner?: string;
  level: "readonly" | "dml" | "ddl";
  status: "active" | "disabled";
  expires_at?: Dayjs | null;
}

function CredentialCard({ apiKey }: { apiKey: string }) {
  const copy = async () => {
    try {
      await copyText(apiKey);
      void message.success("API Key 已复制");
    } catch {
      void message.error("复制失败，请手动选择密钥");
    }
  };
  return (
    <div className="cfg-credential">
      <Alert type="warning" showIcon message="密钥仅展示这一次，关闭后无法再查看，请立即妥善保存" />
      <div className="cfg-credential-value"><code>{apiKey || "密钥未返回"}</code><Button icon={<CopyOutlined />} disabled={!apiKey} onClick={() => void copy()}>复制</Button></div>
    </div>
  );
}

export function Agents() {
  const [createForm] = Form.useForm<AgentFormValues>();
  const [editForm] = Form.useForm<AgentFormValues>();
  const [list, setList] = useState<AgentView[]>([]);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [createStep, setCreateStep] = useState(0);
  const [submitting, setSubmitting] = useState(false);
  const [credential, setCredential] = useState("");
  const [editing, setEditing] = useState<AgentView | null>(null);
  const [rotatingID, setRotatingID] = useState("");
  const [deletingID, setDeletingID] = useState("");
  const mountedRef = useRef(true);
  const requestRef = useRef(0);
  const controllerRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = ++requestRef.current;
    controllerRef.current = controller;
    setLoading(true);
    setFailed(false);
    try {
      const response = await listAgents({ page, page_size: pageSize });
      if (!mountedRef.current || controller.signal.aborted || sequence !== requestRef.current) return;
      setList(response.list || []);
      setTotal(Number.isFinite(response.total) ? Math.max(0, response.total) : 0);
    } catch (error: unknown) {
      if (!controller.signal.aborted && !isCanceled(error) && mountedRef.current) setFailed(true);
    } finally {
      if (controllerRef.current === controller) controllerRef.current = null;
      if (mountedRef.current && sequence === requestRef.current) setLoading(false);
    }
  }, [page, pageSize]);

  useEffect(() => {
    mountedRef.current = true;
    void load();
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      requestRef.current += 1;
    };
  }, [load]);

  const openCreate = () => {
    createForm.resetFields();
    createForm.setFieldsValue({ level: "readonly", status: "active" });
    setCredential("");
    setCreateStep(0);
    setCreateOpen(true);
  };

  const submitCreate = async () => {
    let values: AgentFormValues;
    try {
      values = await createForm.validateFields();
    } catch {
      return;
    }
    const input: AgentCreateInput = {
      id: values.id,
      name: values.name.trim(),
      level: values.level,
      status: "active",
    };
    if (values.owner?.trim()) input.owner = values.owner.trim();
    if (values.expires_at) input.expires_at = values.expires_at.toISOString();
    setSubmitting(true);
    try {
      const created = await createAgent(input);
      if (!mountedRef.current) return;
      setCredential(created.api_key || "");
      setCreateStep(1);
      void message.success("Agent 已创建");
      void load();
    } catch (error: unknown) {
      if (httpStatus(error) === 409) void message.error("Agent 已存在");
      else void message.error(apiErrorMessage(error, "创建 Agent 失败"));
    } finally {
      if (mountedRef.current) setSubmitting(false);
    }
  };

  const openEdit = (agent: AgentView) => {
    setEditing(agent);
    editForm.setFieldsValue({
      id: agent.id,
      name: agent.name,
      owner: agent.owner || undefined,
      level: agent.level as AgentFormValues["level"],
      status: agent.status as AgentFormValues["status"],
      expires_at: agent.expires_at ? dayjs(agent.expires_at) : null,
    });
  };

  const submitEdit = async () => {
    if (!editing) return;
    let values: AgentFormValues;
    try {
      values = await editForm.validateFields();
    } catch {
      return;
    }
    const input: AgentUpdateInput = {};
    if (values.name.trim() !== editing.name) input.name = values.name.trim();
    if ((values.owner || "").trim() !== (editing.owner || "")) input.owner = (values.owner || "").trim();
    if (values.level !== editing.level) input.level = values.level;
    if (values.status !== editing.status) input.status = values.status;
    const expires = values.expires_at?.toISOString() || null;
    if (expires !== (editing.expires_at || null)) input.expires_at = expires;
    if (Object.keys(input).length === 0) {
      setEditing(null);
      return;
    }
    setSubmitting(true);
    try {
      await updateAgent(editing.id, input);
      if (!mountedRef.current) return;
      setEditing(null);
      void message.success("Agent 已更新");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "更新 Agent 失败"));
    } finally {
      if (mountedRef.current) setSubmitting(false);
    }
  };

  const rotate = async (agent: AgentView) => {
    setRotatingID(agent.id);
    try {
      const updated = await rotateAgentKey(agent.id);
      if (!mountedRef.current) return;
      setCredential(updated.api_key || "");
      void message.success("API Key 已轮换，旧密钥已失效");
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "轮换密钥失败"));
    } finally {
      if (mountedRef.current) setRotatingID("");
    }
  };

  const remove = async (agent: AgentView) => {
    setDeletingID(agent.id);
    try {
      await deleteAgent(agent.id);
      if (!mountedRef.current) return;
      void message.success("Agent 已删除");
      if (list.length === 1 && page > 1) setPage((value) => value - 1);
      else void load();
    } catch (error: unknown) {
      if (httpStatus(error) === 409) void message.error("该 Agent 仍配置了权限策略，请先在权限页移除后再删除");
      else void message.error(apiErrorMessage(error, "删除 Agent 失败"));
    } finally {
      if (mountedRef.current) setDeletingID("");
    }
  };

  const columns: TableProps<AgentView>["columns"] = [
    { title: "ID", dataIndex: "id", width: 150, ellipsis: true, render: (value: string) => <code>{value}</code> },
    { title: "名称", dataIndex: "name", width: 150, ellipsis: true },
    { title: "负责人", dataIndex: "owner", width: 120, render: (value: string | null) => value || "—" },
    { title: "级别", dataIndex: "level", width: 120, render: (value: string) => <Tag color={agentLevelMeta[value as keyof typeof agentLevelMeta]?.color}>{configLabel(agentLevelMeta, value)}</Tag> },
    { title: "状态", dataIndex: "status", width: 90, render: (value: string) => <Tag color={agentStatusMeta[value as keyof typeof agentStatusMeta]?.color}>{configLabel(agentStatusMeta, value)}</Tag> },
    { title: "过期时间", dataIndex: "expires_at", width: 180, render: (value: string | null) => formatDateTime(value, "永不过期") },
    { title: "创建时间", dataIndex: "created_at", width: 180, render: (value: string) => formatDateTime(value) },
    {
      title: "操作", key: "action", fixed: "right", width: 230,
      render: (_: unknown, agent: AgentView) => <Space size={4}>
        <Button type="link" size="small" icon={<EditOutlined />} onClick={() => openEdit(agent)}>编辑</Button>
        <Popconfirm title="轮换后旧密钥立即失效，确认？" onConfirm={() => void rotate(agent)}>
          <Button type="link" size="small" icon={<KeyOutlined />} loading={rotatingID === agent.id}>轮换</Button>
        </Popconfirm>
        <Popconfirm title="确认删除该 Agent？" onConfirm={() => void remove(agent)}>
          <Button type="link" danger size="small" icon={<DeleteOutlined />} loading={deletingID === agent.id}>删除</Button>
        </Popconfirm>
      </Space>,
    },
  ];

  return (
    <PageContainer title="Agent" subtitle="管理 AI 身份、能力档位与访问凭证" extra={<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建 Agent</Button>}>
      {failed ? <Alert className="cfg-inline-alert" type="error" showIcon message="Agent 列表加载失败" action={<Button size="small" onClick={() => void load()}>重试</Button>} /> : null}
      <div className="cfg-table"><Table<AgentView> rowKey="id" columns={columns} dataSource={list} loading={loading} pagination={false} scroll={{ x: 1_260 }} /></div>
      <div className="cfg-pagination"><Pagination current={page} pageSize={pageSize} total={total} pageSizeOptions={[20, 50, 100]} showSizeChanger onChange={(next, size) => { setPageSize(size); setPage(size !== pageSize ? 1 : next); }} /></div>

      <Modal open={createOpen} title="新建 Agent" width={620} destroyOnClose maskClosable={false} onCancel={() => { setCreateOpen(false); setCredential(""); }} footer={createStep === 0 ? [<Button key="cancel" onClick={() => setCreateOpen(false)}>取消</Button>, <Button key="submit" type="primary" loading={submitting} onClick={() => void submitCreate()}>创建并生成密钥</Button>] : [<Button key="done" type="primary" onClick={() => { setCreateOpen(false); setCredential(""); }}>我已妥善保存</Button>]}>
        <Steps current={createStep} items={[{ title: "基本信息" }, { title: "保存凭证" }]} />
        {createStep === 0 ? <Form<AgentFormValues> className="cfg-form" form={createForm} layout="vertical">
          <Form.Item name="id" label="Agent ID" rules={[{ required: true }, { pattern: /^[a-z0-9_-]+$/, message: "仅允许小写字母、数字、下划线和连字符" }]}><Input placeholder="reporting-agent" /></Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="owner" label="负责人"><Input /></Form.Item>
          <Form.Item name="level" label="能力档位" rules={[{ required: true }]}><Radio.Group options={Object.entries(agentLevelMeta).map(([value, meta]) => ({ value, label: meta.label }))} /></Form.Item>
          <p className="cfg-form-help">只读仅允许查询；读写可执行 DML；结构档位可提交 DDL，所有请求仍须通过规则与对象权限。</p>
          <Form.Item name="expires_at" label="过期时间"><DatePicker showTime allowClear className="cfg-full-width" /></Form.Item>
        </Form> : <CredentialCard apiKey={credential} />}
      </Modal>

      <Drawer open={editing !== null} title="编辑 Agent" width={520} destroyOnClose onClose={() => setEditing(null)} extra={<Button type="primary" loading={submitting} onClick={() => void submitEdit()}>保存</Button>}>
        <Form<AgentFormValues> form={editForm} layout="vertical">
          <Form.Item name="id" label="Agent ID"><Input disabled /></Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="owner" label="负责人"><Input /></Form.Item>
          <Form.Item name="level" label="能力档位" rules={[{ required: true }]}><Radio.Group options={Object.entries(agentLevelMeta).map(([value, meta]) => ({ value, label: meta.label }))} /></Form.Item>
          <p className="cfg-form-help">能力档位决定可用语句范围，对象级授权请在权限页配置。</p>
          <Form.Item name="status" label="状态" rules={[{ required: true }]}><Radio.Group options={Object.entries(agentStatusMeta).map(([value, meta]) => ({ value, label: meta.label }))} /></Form.Item>
          <Form.Item name="expires_at" label="过期时间"><DatePicker showTime allowClear className="cfg-full-width" /></Form.Item>
        </Form>
      </Drawer>

      <Modal open={Boolean(credential) && !createOpen} title="新 API Key" footer={<Button type="primary" onClick={() => setCredential("")}>我已妥善保存</Button>} closable={false} maskClosable={false}><CredentialCard apiKey={credential} /></Modal>
    </PageContainer>
  );
}
