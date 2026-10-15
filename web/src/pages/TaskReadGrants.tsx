import { CopyOutlined, PlusOutlined, ReloadOutlined, StopOutlined } from "@ant-design/icons";
import { Alert, AutoComplete, Button, DatePicker, Form, Input, Modal, Popconfirm, Space, Table, Tag, Typography, message } from "antd";
import type { TableProps } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { listAgents } from "@/api/agents";
import { listDatasources } from "@/api/datasources";
import { createTaskReadGrant, listTaskReadGrants, revokeTaskReadGrant } from "@/api/taskReadGrants";
import type { AgentView, DatasourceView, TaskReadGrantView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";

import { apiErrorMessage, copyText, formatDateTime } from "./config/utils";

interface GrantFormValues {
  agent_id: string;
  datasource_id: string;
  sql: string;
  expires_at: Dayjs;
}

function grantStatus(grant: TaskReadGrantView): { label: string; color: string } {
  if (grant.revoked_at) return { label: "已撤销", color: "default" };
  const expiry = Date.parse(grant.expires_at);
  if (!Number.isFinite(expiry) || expiry <= Date.now()) return { label: "已过期", color: "default" };
  return { label: "有效", color: "green" };
}

export function TaskReadGrants() {
  const [form] = Form.useForm<GrantFormValues>();
  const [grants, setGrants] = useState<TaskReadGrantView[]>([]);
  const [agents, setAgents] = useState<AgentView[]>([]);
  const [datasources, setDatasources] = useState<DatasourceView[]>([]);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [revokingID, setRevokingID] = useState("");
  const [oneTimeKey, setOneTimeKey] = useState("");
  const mounted = useRef(true);

  const load = useCallback(async () => {
    setLoading(true);
    setFailed(false);
    try {
      const [grantList, agentPage, datasourcePage] = await Promise.all([
        listTaskReadGrants(), listAgents({ page: 1, page_size: 100 }), listDatasources({ page: 1, page_size: 100 }),
      ]);
      if (!mounted.current) return;
      setGrants(grantList || []);
      setAgents(agentPage.list || []);
      setDatasources(datasourcePage.list || []);
    } catch {
      if (mounted.current) setFailed(true);
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void load();
    return () => { mounted.current = false; };
  }, [load]);

  const openCreate = () => {
    form.resetFields();
    form.setFieldsValue({ expires_at: dayjs().add(1, "hour") });
    setCreateOpen(true);
  };

  const submitCreate = async () => {
    let values: GrantFormValues;
    try { values = await form.validateFields(); } catch { return; }
    const now = dayjs();
    if (!values.expires_at.isAfter(now) || values.expires_at.isAfter(now.add(24, "hour"))) {
      void message.error("有效期必须在未来 24 小时内");
      return;
    }
    setCreating(true);
    try {
      const created = await createTaskReadGrant({
        agent_id: values.agent_id.trim(), datasource_id: values.datasource_id.trim(),
        sql: values.sql, expires_at: values.expires_at.toISOString(),
      });
      if (!mounted.current) return;
      form.resetFields();
      setCreateOpen(false);
      setOneTimeKey(created.api_key || "");
      void message.success("临时授权已创建");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "创建临时授权失败"));
    } finally {
      if (mounted.current) setCreating(false);
    }
  };

  const revoke = async (grant: TaskReadGrantView) => {
    setRevokingID(grant.id);
    try {
      await revokeTaskReadGrant(grant.id);
      if (!mounted.current) return;
      void message.success("授权已撤销，后续请求将被拒绝");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "撤销失败"));
    } finally {
      if (mounted.current) setRevokingID("");
    }
  };

  const copyKey = async () => {
    try { await copyText(oneTimeKey); void message.success("临时 Key 已复制"); }
    catch { void message.error("复制失败，请手动选择密钥"); }
  };

  const columns: TableProps<TaskReadGrantView>["columns"] = [
    { title: "授权 ID", dataIndex: "id", width: 250, ellipsis: true, render: (value: string) => <code>{value}</code> },
    { title: "Agent", dataIndex: "agent_id", width: 160, ellipsis: true },
    { title: "数据源", dataIndex: "datasource_id", width: 160, ellipsis: true },
    { title: "签发者", dataIndex: "issuer_user_id", width: 160, ellipsis: true },
    { title: "状态", width: 100, render: (_: unknown, grant: TaskReadGrantView) => { const status = grantStatus(grant); return <Tag color={status.color}>{status.label}</Tag>; } },
    { title: "到期", dataIndex: "expires_at", width: 180, render: (value: string) => formatDateTime(value) },
    { title: "创建", dataIndex: "created_at", width: 180, render: (value: string) => formatDateTime(value) },
    { title: "操作", fixed: "right", width: 100, render: (_: unknown, grant: TaskReadGrantView) =>
      <Popconfirm title="立即撤销这项临时授权？" description="已发出的密钥会失效。" onConfirm={() => void revoke(grant)}>
        <Button type="link" danger size="small" icon={<StopOutlined />} disabled={grantStatus(grant).label !== "有效"} loading={revokingID === grant.id}>撤销</Button>
      </Popconfirm> },
  ];

  return <PageContainer title="临时只读授权" subtitle="为单次任务签发限时、限 Agent、限数据源、限精确 SQL 的密钥" extra={<Space>
    <Button icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>刷新</Button>
    <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建授权</Button>
  </Space>}>
    <Alert type="info" showIcon message="临时授权只会收窄现有权限" description={<span>最长有效 24 小时，只允许一条精确匹配的 SELECT。原 Agent 的对象权限、规则、脱敏和审计仍逐次生效；可先在 <Link to="/onboarding">接入向导</Link>检查 Agent 与数据源配置。</span>} style={{ marginBottom: 16 }} />
    {failed && <Alert type="error" showIcon message="授权列表加载失败" description="检查当前账号是否同时具有策略管理和查询执行权限。" action={<Button size="small" onClick={() => void load()}>重试</Button>} style={{ marginBottom: 16 }} />}
    <div className="cfg-table"><Table<TaskReadGrantView> rowKey="id" columns={columns} dataSource={grants} loading={loading} pagination={{ pageSize: 20, showSizeChanger: false }} scroll={{ x: 1_260 }} /></div>

    <Modal open={createOpen} title="新建临时只读授权" width={700} destroyOnClose maskClosable={false} onCancel={() => setCreateOpen(false)} footer={[
      <Button key="cancel" onClick={() => setCreateOpen(false)}>取消</Button>,
      <Button key="create" type="primary" loading={creating} onClick={() => void submitCreate()}>创建并显示密钥</Button>,
    ]}>
      <Form<GrantFormValues> form={form} layout="vertical">
        <Form.Item name="agent_id" label="Agent ID" rules={[{ required: true, whitespace: true }]}>
          <AutoComplete placeholder="选择或输入 Agent ID" options={agents.map((agent) => ({ value: agent.id, label: `${agent.name} (${agent.id})` }))} filterOption={(input, option) => String(option?.label || "").toLowerCase().includes(input.toLowerCase())} />
        </Form.Item>
        <Form.Item name="datasource_id" label="数据源 ID" rules={[{ required: true, whitespace: true }]}>
          <AutoComplete placeholder="选择或输入数据源 ID" options={datasources.map((source) => ({ value: source.id, label: `${source.name} (${source.id})` }))} filterOption={(input, option) => String(option?.label || "").toLowerCase().includes(input.toLowerCase())} />
        </Form.Item>
        <Form.Item name="sql" label="唯一允许的 SQL" rules={[{ required: true, whitespace: true }]} extra="原始 SQL 文本必须精确匹配；空格、大小写或参数变化都会被拒绝。当前只接受简单 SELECT，不接受 CTE、锁行或写入。">
          <Input.TextArea rows={5} placeholder="SELECT id FROM public.orders WHERE id = 1 LIMIT 1" />
        </Form.Item>
        <Form.Item name="expires_at" label="到期时间" rules={[{ required: true }]}><DatePicker showTime className="cfg-full-width" /></Form.Item>
      </Form>
    </Modal>

    <Modal open={Boolean(oneTimeKey)} title="保存临时 Key" closable={false} maskClosable={false} footer={<Button type="primary" onClick={() => setOneTimeKey("")}>我已妥善保存</Button>}>
      <Alert type="warning" showIcon message="密钥只显示一次" description="关闭后无法再次查看。请放在目标客户端的安全凭据配置中，到期或撤销后立即失效。" />
      <div className="cfg-credential-value" style={{ marginTop: 16 }}><code>{oneTimeKey}</code><Button icon={<CopyOutlined />} onClick={() => void copyKey()}>复制</Button></div>
      <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>该 Key 只能调用 MCP 的 query 工具；使用前请确认原 Agent 对目标对象已有只读权限。</Typography.Paragraph>
    </Modal>
  </PageContainer>;
}
