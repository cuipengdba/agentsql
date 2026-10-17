import { CopyOutlined, PlusOutlined, ReloadOutlined, StopOutlined } from "@ant-design/icons";
import { Alert, AutoComplete, Button, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Space, Table, Tag, Typography, message } from "antd";
import type { TableProps } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { listAgents } from "@/api/agents";
import { authConfig, issueMCPDelegationToken, me } from "@/api/auth";
import { listDatasources } from "@/api/datasources";
import {
  approveTaskReadGrantRequest, createTaskReadGrantRequest, getTaskReadGrantRequest, issueTaskReadGrantRequest,
  listTaskReadGrantRequests, listTaskReadGrants, rejectTaskReadGrantRequest, revokeTaskReadGrant,
} from "@/api/taskReadGrants";
import type { AdminView, AgentView, DatasourceView, TaskReadGrantRequestView, TaskReadGrantView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";

import { apiErrorMessage, copyText, formatDateTime } from "./config/utils";

interface RequestFormValues {
  agent_id: string;
  datasource_id: string;
  sql: string;
  expires_at: Dayjs;
  max_uses: number;
}

function requestStatus(item: TaskReadGrantRequestView): { label: string; color: string } {
  if (item.grant_revoked_at) return { label: "已撤销", color: "default" };
  if (item.status === "issued") return { label: "已领取", color: "green" };
  if (item.status === "rejected") return { label: "已拒绝", color: "red" };
  if (Date.parse(item.expires_at) <= Date.now()) return { label: "已过期", color: "default" };
  if (item.status === "approved") return { label: "待领取", color: "blue" };
  return { label: "待审批", color: "orange" };
}

function grantStatus(grant: TaskReadGrantView): { label: string; color: string } {
  if (grant.revoked_at) return { label: "已撤销", color: "default" };
  const expiry = Date.parse(grant.expires_at);
  if (!Number.isFinite(expiry) || expiry <= Date.now()) return { label: "已过期", color: "default" };
  if (grant.max_uses != null && grant.used_count >= grant.max_uses) return { label: "次数已用尽", color: "default" };
  return { label: "有效", color: "green" };
}

export function TaskReadGrants() {
  const [form] = Form.useForm<RequestFormValues>();
  const [requests, setRequests] = useState<TaskReadGrantRequestView[]>([]);
  const [grants, setGrants] = useState<TaskReadGrantView[]>([]);
  const [principal, setPrincipal] = useState<AdminView | null>(null);
  const [agents, setAgents] = useState<AgentView[]>([]);
  const [datasources, setDatasources] = useState<DatasourceView[]>([]);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [busyID, setBusyID] = useState("");
  const [reviewing, setReviewing] = useState<TaskReadGrantRequestView | null>(null);
  const [reviewLoading, setReviewLoading] = useState(false);
  const [oneTimeKey, setOneTimeKey] = useState("");
  const [delegationEnabled, setDelegationEnabled] = useState(false);
  const [delegationAgentID, setDelegationAgentID] = useState("");
  const [delegationToken, setDelegationToken] = useState("");
  const [delegationExpiry, setDelegationExpiry] = useState("");
  const [issuingDelegation, setIssuingDelegation] = useState(false);
  const idempotencyKey = useRef("");
  const mounted = useRef(true);
  const canReview = Boolean(principal?.permissions.includes("strategy.manage"));
  const canAudit = Boolean(principal?.permissions.includes("audit.view"));

  const load = useCallback(async () => {
    setLoading(true);
    setFailed(false);
    try {
      const current = await me();
      const reviewer = current.permissions.includes("strategy.manage");
      const canListResources = current.permissions.includes("datasource.manage");
      const [requestList, agentPage, datasourcePage, grantList] = await Promise.all([
        listTaskReadGrantRequests(),
        canListResources ? listAgents({ page: 1, page_size: 100 }) : Promise.resolve({ list: [] as AgentView[] }),
        canListResources ? listDatasources({ page: 1, page_size: 100 }) : Promise.resolve({ list: [] as DatasourceView[] }),
        reviewer ? listTaskReadGrants() : Promise.resolve([]),
      ]);
      if (!mounted.current) return;
      setPrincipal(current);
      setRequests(requestList || []);
      setAgents(agentPage.list || []);
      setDatasources(datasourcePage.list || []);
      setGrants(grantList || []);
    } catch {
      if (mounted.current) setFailed(true);
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void load();
    void authConfig().then((config) => {
      if (mounted.current) setDelegationEnabled(Boolean(config.human_delegation_enabled));
    }).catch(() => { if (mounted.current) setDelegationEnabled(false); });
    return () => { mounted.current = false; };
  }, [load]);

  const issueDelegation = async () => {
    const agentID = delegationAgentID.trim();
    if (!agentID || issuingDelegation) return;
    setIssuingDelegation(true);
    try {
      const issued = await issueMCPDelegationToken(agentID);
      if (!mounted.current) return;
      setDelegationToken(issued.token);
      setDelegationExpiry(issued.expires_at);
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "签发 MCP 用户委托令牌失败"));
    } finally {
      if (mounted.current) setIssuingDelegation(false);
    }
  };

  const openCreate = () => {
    form.resetFields();
    form.setFieldsValue({ expires_at: dayjs().add(1, "hour"), max_uses: 1 });
    idempotencyKey.current = crypto.randomUUID();
    setCreateOpen(true);
  };

  const submitCreate = async () => {
    let values: RequestFormValues;
    try { values = await form.validateFields(); } catch { return; }
    const now = dayjs();
    if (!values.expires_at.isAfter(now) || values.expires_at.isAfter(now.add(24, "hour"))) {
      void message.error("有效期必须在未来 24 小时内");
      return;
    }
    if (!Number.isInteger(values.max_uses) || values.max_uses < 1 || values.max_uses > 10) {
      void message.error("允许返回结果次数必须为 1–10");
      return;
    }
    setCreating(true);
    try {
      await createTaskReadGrantRequest({
        agent_id: values.agent_id.trim(), datasource_id: values.datasource_id.trim(),
        sql: values.sql, expires_at: values.expires_at.toISOString(),
        idempotency_key: idempotencyKey.current, max_uses: values.max_uses,
      });
      if (!mounted.current) return;
      form.resetFields();
      setCreateOpen(false);
      void message.success("申请已提交，等待另一位管理员审批");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "提交申请失败，可用同一申请重试"));
    } finally {
      if (mounted.current) setCreating(false);
    }
  };

  const review = async (item: TaskReadGrantRequestView, approve: boolean) => {
    setBusyID(item.id);
    try {
      if (approve) await approveTaskReadGrantRequest(item.id);
      else await rejectTaskReadGrantRequest(item.id);
      if (!mounted.current) return;
      setReviewing(null);
      void message.success(approve ? "申请已批准，申请人可以领取密钥" : "申请已拒绝");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "审批失败"));
    } finally {
      if (mounted.current) setBusyID("");
    }
  };

  const openReview = async (item: TaskReadGrantRequestView) => {
    setReviewLoading(true);
    try {
      const detail = await getTaskReadGrantRequest(item.id);
      if (!mounted.current) return;
      if (!detail.sql) throw new Error("审批详情没有可核对的 SQL");
      setReviewing(detail);
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "无法读取申请详情"));
    } finally {
      if (mounted.current) setReviewLoading(false);
    }
  };

  const issue = async (item: TaskReadGrantRequestView) => {
    setBusyID(item.id);
    try {
      const grant = await issueTaskReadGrantRequest(item.id);
      if (!mounted.current) return;
      setOneTimeKey(grant.api_key || "");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "领取失败，请刷新申请状态"));
    } finally {
      if (mounted.current) setBusyID("");
    }
  };

  const revokeByID = async (id: string) => {
    setBusyID(id);
    try {
      await revokeTaskReadGrant(id);
      if (!mounted.current) return;
      void message.success("授权已撤销，后续请求将被拒绝");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "撤销失败，请刷新状态"));
    } finally {
      if (mounted.current) setBusyID("");
    }
  };

  const requestColumns: TableProps<TaskReadGrantRequestView>["columns"] = [
    { title: "申请 ID", dataIndex: "id", width: 250, ellipsis: true, render: (value: string) => <code>{value}</code> },
    { title: "申请人", dataIndex: "requester_user_id", width: 155, ellipsis: true },
    { title: "Agent", dataIndex: "agent_id", width: 155, ellipsis: true },
    { title: "数据源", dataIndex: "datasource_id", width: 155, ellipsis: true },
    { title: "状态", width: 95, render: (_: unknown, item: TaskReadGrantRequestView) => { const state = requestStatus(item); return <Tag color={state.color}>{state.label}</Tag>; } },
    { title: "审批人", dataIndex: "reviewer_user_id", width: 155, ellipsis: true, render: (value?: string) => value || "—" },
    { title: "到期", dataIndex: "expires_at", width: 180, render: (value: string) => formatDateTime(value) },
    { title: "结果预算", width: 105, render: (_: unknown, item: TaskReadGrantRequestView) => item.max_uses == null ? "旧版不限次" : `${item.max_uses} 次` },
    { title: "授权 ID", dataIndex: "grant_id", width: 250, ellipsis: true, render: (value?: string) => value ? (canAudit ? <Link to={`/audit?task_grant_id=${encodeURIComponent(value)}`}>{value}</Link> : <code>{value}</code>) : "—" },
    { title: "操作", fixed: "right", width: 190, render: (_: unknown, item: TaskReadGrantRequestView) => {
      const pending = item.status === "pending" && Date.parse(item.expires_at) > Date.now();
      const canDecide = canReview && item.requester_user_id !== principal?.id && pending;
      const canIssue = item.status === "approved" && item.requester_user_id === principal?.id && Date.parse(item.expires_at) > Date.now();
      const canRevoke = item.status === "issued" && item.requester_user_id === principal?.id && Boolean(item.grant_id) && !item.grant_revoked_at && Date.parse(item.expires_at) > Date.now();
      return <Space size="small">
        {canDecide && <Button type="link" size="small" loading={reviewLoading || busyID === item.id} onClick={() => void openReview(item)}>核对并审批</Button>}
        {canIssue && <Button type="link" size="small" loading={busyID === item.id} onClick={() => void issue(item)}>领取密钥</Button>}
        {canRevoke && <Popconfirm title="立即撤销自己的任务授权？" onConfirm={() => void revokeByID(item.grant_id!)}><Button type="link" danger size="small" loading={busyID === item.grant_id}>撤销</Button></Popconfirm>}
        {!canDecide && !canIssue && !canRevoke && "—"}
      </Space>;
    } },
  ];

  const grantColumns: TableProps<TaskReadGrantView>["columns"] = [
    { title: "授权 ID", dataIndex: "id", width: 250, ellipsis: true, render: (value: string) => canAudit ? <Link to={`/audit?task_grant_id=${encodeURIComponent(value)}`}>{value}</Link> : <code>{value}</code> },
    { title: "Agent", dataIndex: "agent_id", width: 160, ellipsis: true },
    { title: "数据源", dataIndex: "datasource_id", width: 160, ellipsis: true },
    { title: "签发者", dataIndex: "issuer_user_id", width: 160, ellipsis: true },
    { title: "状态", width: 100, render: (_: unknown, grant: TaskReadGrantView) => { const state = grantStatus(grant); return <Tag color={state.color}>{state.label}</Tag>; } },
    { title: "到期", dataIndex: "expires_at", width: 180, render: (value: string) => formatDateTime(value) },
    { title: "已用 / 上限", width: 110, render: (_: unknown, grant: TaskReadGrantView) => grant.max_uses == null ? "旧版不限次" : `${grant.used_count} / ${grant.max_uses}` },
    { title: "操作", fixed: "right", width: 100, render: (_: unknown, grant: TaskReadGrantView) =>
      <Popconfirm title="立即撤销这项临时授权？" description="已发出的密钥会失效。" onConfirm={() => void revokeByID(grant.id)}>
        <Button type="link" danger size="small" icon={<StopOutlined />} disabled={grantStatus(grant).label !== "有效"} loading={busyID === grant.id}>撤销</Button>
      </Popconfirm> },
  ];

  return <PageContainer title="任务授权" subtitle="申请人提交精确只读查询，另一位管理员审批后一次性领取密钥" extra={<Space>
    <Button icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>刷新</Button>
    <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建申请</Button>
  </Space>}>
    {delegationEnabled && <section style={{ marginBottom: 24 }}>
      <Typography.Title level={4}>MCP 用户身份委托</Typography.Title>
      <Alert type="info" showIcon message="Agent 密钥与用户令牌同时校验" description="为指定 Agent 签发 30 分钟内有效的用户令牌。它只用于 MCP 请求，不能登录控制台；退出登录、停用账号或撤销查询权限后立即失效。客户端需分别配置 Authorization: Bearer <Agent Key> 与 X-AgentSQL-User-Authorization: Bearer <用户令牌>。" style={{ marginBottom: 12 }} />
      <Space wrap>
        <AutoComplete style={{ width: 280 }} value={delegationAgentID} onChange={setDelegationAgentID} placeholder="选择或输入 Agent ID" options={agents.map((agent) => ({ value: agent.id, label: `${agent.name} (${agent.id})` }))} filterOption={(input, option) => String(option?.label || "").toLowerCase().includes(input.toLowerCase())} />
        <Button onClick={() => void issueDelegation()} loading={issuingDelegation} disabled={!delegationAgentID.trim()}>签发用户令牌</Button>
      </Space>
    </section>}
    <Alert type="info" showIcon message="授权范围仍受原策略约束" description={<span>一位申请人、另一位审批人；最长 24 小时，只允许一条精确匹配的简单 SELECT。新申请可设 1–10 次结果预算；预算在返回结果前原子扣减，传输失败不退还。原 Agent 的权限、规则、脱敏和审计继续逐次生效。先在 <Link to="/onboarding">接入向导</Link>检查配置。</span>} style={{ marginBottom: 16 }} />
    {failed && <Alert type="error" showIcon message="授权流程加载失败" description="检查账号的查询执行权限及服务状态。" action={<Button size="small" onClick={() => void load()}>重试</Button>} style={{ marginBottom: 16 }} />}
    <Typography.Title level={4}>授权申请</Typography.Title>
    <div className="cfg-table"><Table<TaskReadGrantRequestView> rowKey="id" columns={requestColumns} dataSource={requests} loading={loading} pagination={{ pageSize: 20, showSizeChanger: false }} scroll={{ x: 1_540 }} /></div>
    {canReview && <><Typography.Title level={4} style={{ marginTop: 28 }}>已签发授权</Typography.Title>
      <div className="cfg-table"><Table<TaskReadGrantView> rowKey="id" columns={grantColumns} dataSource={grants} loading={loading} pagination={{ pageSize: 20, showSizeChanger: false }} scroll={{ x: 1_010 }} /></div></>}

    <Modal open={Boolean(reviewing)} title="核对任务授权申请" width={700} destroyOnClose maskClosable={false} onCancel={() => setReviewing(null)} footer={[
      <Button key="close" onClick={() => setReviewing(null)}>取消</Button>,
      <Button key="reject" danger loading={busyID === reviewing?.id} onClick={() => { if (reviewing) void review(reviewing, false); }}>拒绝</Button>,
      <Button key="approve" type="primary" loading={busyID === reviewing?.id} onClick={() => { if (reviewing) void review(reviewing, true); }}>确认 SQL 后批准</Button>,
    ]}>
      {reviewing && <Space direction="vertical" style={{ width: "100%" }}>
        <Typography.Text>申请人：<code>{reviewing.requester_user_id}</code></Typography.Text>
        <Typography.Text>Agent：<code>{reviewing.agent_id}</code>　数据源：<code>{reviewing.datasource_id}</code></Typography.Text>
        <Typography.Text>有效期至：{formatDateTime(reviewing.expires_at)}</Typography.Text>
        <Typography.Text>结果预算：{reviewing.max_uses == null ? "旧版不限次" : `${reviewing.max_uses} 次`}</Typography.Text>
        <Typography.Text strong>申请的精确 SQL</Typography.Text>
        <Input.TextArea value={reviewing.sql} rows={6} readOnly aria-label="申请的精确 SQL" />
        <Typography.Text type="secondary">SQL SHA-256：<code>{reviewing.sql_sha256}</code></Typography.Text>
        <Alert type="warning" showIcon message="只批准已经核对的查询" description="批准后仅申请人可领取一次展示的 Key。请同时核对结果预算；原 Agent 的最小权限、脱敏与审计仍逐次生效。" />
      </Space>}
    </Modal>

    <Modal open={createOpen} title="申请临时只读授权" width={700} destroyOnClose maskClosable={false} onCancel={() => setCreateOpen(false)} footer={[
      <Button key="cancel" onClick={() => setCreateOpen(false)}>取消</Button>,
      <Button key="create" type="primary" loading={creating} onClick={() => void submitCreate()}>提交审批</Button>,
    ]}>
      <Form<RequestFormValues> form={form} layout="vertical">
        <Form.Item name="agent_id" label="Agent ID" rules={[{ required: true, whitespace: true }]}>
          <AutoComplete placeholder="选择或输入 Agent ID" options={agents.map((agent) => ({ value: agent.id, label: `${agent.name} (${agent.id})` }))} filterOption={(input, option) => String(option?.label || "").toLowerCase().includes(input.toLowerCase())} />
        </Form.Item>
        <Form.Item name="datasource_id" label="数据源 ID" rules={[{ required: true, whitespace: true }]}>
          <AutoComplete placeholder="选择或输入数据源 ID" options={datasources.map((source) => ({ value: source.id, label: `${source.name} (${source.id})` }))} filterOption={(input, option) => String(option?.label || "").toLowerCase().includes(input.toLowerCase())} />
        </Form.Item>
        <Form.Item name="sql" label="唯一允许的 SQL" rules={[{ required: true, whitespace: true }]} extra="原始 SQL 文本必须精确匹配；空格、大小写或参数变化都会被拒绝。当前只接受简单 SELECT。">
          <Input.TextArea rows={5} placeholder="SELECT id FROM public.orders WHERE id = 1 LIMIT 1" />
        </Form.Item>
        <Form.Item name="expires_at" label="到期时间" rules={[{ required: true }]}><DatePicker showTime className="cfg-full-width" /></Form.Item>
        <Form.Item name="max_uses" label="最多返回结果次数" rules={[{ required: true, type: "number", min: 1, max: 10 }]} extra="默认 1 次，允许 1–10 次。结果通过审计后、返回给客户端前扣减；网络中断或进程异常可能消耗一次。">
          <InputNumber min={1} max={10} precision={0} className="cfg-full-width" />
        </Form.Item>
      </Form>
    </Modal>

    <Modal open={Boolean(oneTimeKey)} title="保存临时 Key" closable={false} maskClosable={false} footer={<Button type="primary" onClick={() => setOneTimeKey("")}>我已妥善保存</Button>}>
      <Alert type="warning" showIcon message="密钥只显示一次" description="关闭后无法再次查看。请放在目标客户端的安全凭据配置中，到期或撤销后立即失效。" />
      <div className="cfg-credential-value" style={{ marginTop: 16 }}><code>{oneTimeKey}</code><Button icon={<CopyOutlined />} onClick={() => void copyText(oneTimeKey).then(() => { void message.success("临时 Key 已复制"); }).catch(() => { void message.error("复制失败，请手动选择密钥"); })}>复制</Button></div>
      <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>该 Key 只能调用 MCP 的 query 工具，领取只展示一次；调用次数遵守申请时的结果预算。使用前请确认原 Agent 对目标对象已有只读权限。</Typography.Paragraph>
    </Modal>
    <Modal open={Boolean(delegationToken)} title="保存 MCP 用户令牌" closable={false} maskClosable={false} footer={<Button type="primary" onClick={() => { setDelegationToken(""); setDelegationExpiry(""); }}>我已妥善保存</Button>}>
      <Alert type="warning" showIcon message="此令牌仅在本窗口展示" description={`有效期至 ${formatDateTime(delegationExpiry)}。请只放入可信 MCP 客户端的安全凭据配置；退出控制台后会失效。`} />
      <div className="cfg-credential-value" style={{ marginTop: 16 }}><code>{delegationToken}</code><Button icon={<CopyOutlined />} onClick={() => void copyText(delegationToken).then(() => { void message.success("用户令牌已复制"); }).catch(() => { void message.error("复制失败，请手动选择令牌"); })}>复制</Button></div>
      <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>令牌只适用于所选 Agent，不能代替 Agent API Key，且不能访问控制台 API。</Typography.Paragraph>
    </Modal>
  </PageContainer>;
}
