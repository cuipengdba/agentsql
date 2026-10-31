import { Alert, Button, Card, Descriptions, Form, Input, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography, message } from "antd";
import { useCallback, useEffect, useState } from "react";

import { listAgents, rotateAgentKey } from "@/api/agents";
import { listDatasources } from "@/api/datasources";
import { currentPrincipal, listUsers, type UserView } from "@/api/governance";
import { createSQLBotBinding, deleteSQLBotBinding, diagnoseSQLBotBinding, exportSQLBotConfig, listSQLBotBindings, rotateSQLBotConnector, updateSQLBotBinding, type SQLBotBinding, type SQLBotBindingInput, type SQLBotDiagnostics } from "@/api/sqlbot";
import type { AgentView, DatasourceView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { localeText, useLocaleStore } from "@/i18n/locale";
import { selectIsDemo, useDemoStore } from "@/store/demoStore";

type BindingForm = SQLBotBindingInput;
const { Text, Paragraph } = Typography;

export function SQLBotDiagnosticsView({ data }: { data: SQLBotDiagnostics }) {
  const locale = useLocaleStore((state) => state.locale);
  const tr = (zh: string, en: string) => localeText(zh, en, locale);
  const labels: Record<string, string> = {
    binding_enabled: tr("映射已启用", "Mapping enabled"),
    agent_readonly_active: tr("只读 Agent 有效", "Readonly Agent active"),
    supported_datasource: tr("PostgreSQL/MySQL 数据源与模式匹配", "PostgreSQL/MySQL datasource and schema match"),
    user_query_permission: tr("映射人员具有查询权限", "Mapped user has query permission"),
  };
  return <Space direction="vertical" style={{ width: "100%" }}>
    <Alert type={data.configuration_ready ? "info" : "warning"} showIcon message={data.configuration_ready ? tr("配置检查通过", "Configuration checks passed") : tr("配置需要处理", "Configuration needs attention")} description={tr("此检查未执行业务查询。网络、SQLBot 版本、模型和最终查询仍需在 SQLBot 中验证。", "No business query was executed. Verify network, SQLBot version, model and final queries in SQLBot.")} />
    <Descriptions column={1} size="small"><Descriptions.Item label={tr("适配版本", "Adapter target")}>SQLBot {data.supported_sqlbot_version}</Descriptions.Item><Descriptions.Item label={tr("映射修订号", "Mapping revision")}>{data.revision}</Descriptions.Item></Descriptions>
    {data.checks.map((check) => <Space key={check.code}><Tag color={check.ok ? "green" : "red"}>{check.ok ? tr("通过", "Passed") : tr("未通过", "Failed")}</Tag><Text>{labels[check.code] || check.code}</Text></Space>)}
  </Space>;
}

export function SQLBot() {
  const locale = useLocaleStore((state) => state.locale);
  const tr = (zh: string, en: string) => localeText(zh, en, locale);
  const demoEnabled = useDemoStore(selectIsDemo);
  const [bindings, setBindings] = useState<SQLBotBinding[]>([]);
  const [agents, setAgents] = useState<AgentView[]>([]);
  const [sources, setSources] = useState<DatasourceView[]>([]);
  const [users, setUsers] = useState<UserView[]>([]);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<SQLBotBinding | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [credential, setCredential] = useState<{ name: string; value: string } | null>(null);
  const [diagnostics, setDiagnostics] = useState<SQLBotDiagnostics | null>(null);
  const [endpoint, setEndpoint] = useState(() => typeof window === "undefined" ? "" : `${window.location.origin}/mcp`);
  const [exportText, setExportText] = useState("");
  const [form] = Form.useForm<BindingForm>();
  const selectedDatasourceID = Form.useWatch("datasource_id", form);
  const selectedDatasource = sources.find((source) => source.id === selectedDatasourceID);
  const refresh = useCallback(async () => setBindings(await listSQLBotBindings()), []);
  useEffect(() => {
    let active = true;
    void Promise.all([listSQLBotBindings(), listAgents({ page: 1, page_size: 100 }), listDatasources({ page: 1, page_size: 100 }), currentPrincipal().then((principal) => listUsers(principal.tenant_id))]).then(([b, a, d, u]) => {
      if (active) { setBindings(b); setAgents(a.list); setSources(d.list); setUsers(u); }
    }).catch(() => {});
    return () => { active = false; };
  }, []);
  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    try { await action(); } catch { /* API client reports the failure. */ } finally { setBusy(false); }
  };
  const openForm = (binding?: SQLBotBinding) => {
    setEditing(binding || null);
    form.resetFields();
    const suffix = crypto.randomUUID().slice(0, 8).toUpperCase();
    form.setFieldsValue(binding || { workspace_id: "1", schema_name: "public", numeric_columns: [], agent_key_env: `AGENTSQL_SQLBOT_AGENT_${suffix}`, connector_token_env: `AGENTSQL_SQLBOT_CONNECTOR_${suffix}`, enabled: false, revision: 0 });
    setFormOpen(true);
  };
  const save = async (values: BindingForm) => run(async () => {
    const input = { ...values, numeric_columns: values.numeric_columns || [], revision: editing?.revision || 0 };
    if (editing) await updateSQLBotBinding(editing.id, input);
    else {
      const created = await createSQLBotBinding(input);
      setCredential({ name: created.binding.connector_token_env, value: created.connector_token });
    }
    setFormOpen(false); form.resetFields(); setExportText(""); await refresh();
    void message.success(tr("映射已保存", "Mapping saved"));
  });
  const download = () => {
    const url = URL.createObjectURL(new Blob([exportText + "\n"], { type: "application/json" }));
    const link = document.createElement("a"); link.href = url; link.download = "agentsql-sqlbot-config.json"; link.click(); URL.revokeObjectURL(url);
  };
  return <PageContainer title={tr("AgentSQL 问数与安全网关", "AgentSQL Q&A and Security Gateway")} subtitle={tr("AgentSQL 自有问数可独立使用；已有 SQLBot 可选接入数据库安全网关。", "AgentSQL Q&A works independently; an existing SQLBot deployment can optionally use its database security gateway.")} extra={<Space><Button loading={busy} onClick={() => void run(refresh)}>{tr("刷新", "Refresh")}</Button><Button type="primary" onClick={() => openForm()}>{tr("新增映射", "Add mapping")}</Button></Space>}>
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      {demoEnabled ? <Alert showIcon type="info" message={tr("线上仅展示模拟页面与合成数据，不接入或调用真实模型。", "The public demo shows simulated pages and synthetic data only; it does not connect to or call a real model.")} /> : null}
      <Card title={tr("两种使用方式", "Two ways to use AgentSQL")} size="small">
        <Space direction="vertical" size={8}>
          <Text><Text strong>{tr("独立问数：", "Standalone Q&A: ")}</Text>{tr("AgentSQL 自己提供 Go 问数服务、Web 页面与兼容 API，查询仍通过受控网关。当前能力以已发布版本说明为准。", "AgentSQL provides its own Go Q&A service, Web page, and compatible API. Queries still pass through the controlled gateway. Refer to the released version for the currently available scope.")}</Text>
          <Text><Text strong>{tr("SQLBot 安全网关：", "SQLBot security gateway: ")}</Text>{tr("保留现有 SQLBot 界面，通过下方映射让数据库访问由 AgentSQL 授权、受控执行并审计。", "Keep the existing SQLBot interface and use the mappings below so AgentSQL authorizes, controls, and audits database access.")}</Text>
          <Text type="secondary">{tr("完整页面功能和现有业务零改动替换按 v4.5.0 验收矩阵逐项验证，通过前不作完成声明。", "Complete page functionality and zero-change replacement of existing applications are verified item by item against the v4.5.0 acceptance matrix before being claimed.")}</Text>
        </Space>
      </Card>
      <Alert showIcon type="warning" message={tr("仅信任受控的 SQLBot 服务器", "Trust only a controlled SQLBot server")} description={tr("连接凭据允许 SQLBot 服务器代表映射人员查询。请核对工作空间和人员 ID，使用专用只读 Agent；Agent Key 与连接凭据分别保存到服务器环境。禁用、修改或轮换映射后，旧人员令牌立即失效。", "The connector credential allows the SQLBot server to query for the mapped user. Verify workspace and user IDs, use a dedicated readonly Agent, and store the Agent Key and connector credential separately in the server environment. Disabling, editing or rotating a mapping invalidates its old user tokens.")} />
      <Card title={tr("工作空间与人员映射", "Workspace and user mappings")}>
        <Table<SQLBotBinding> rowKey="id" dataSource={bindings} loading={busy} scroll={{ x: 1000 }} pagination={{ pageSize: 10 }} columns={[
          { title: tr("SQLBot 来源", "SQLBot source"), key: "source", render: (_, b) => <Space direction="vertical" size={0}><Text>{b.datasource_name}</Text><Text type="secondary">{tr("工作空间", "Workspace")} {b.workspace_id} · {tr("用户", "User")} {b.sqlbot_user_id}</Text></Space> },
          { title: tr("网关身份", "Gateway identity"), key: "identity", render: (_, b) => <Space direction="vertical" size={0}><Text>{agents.find((a) => a.id === b.agent_id)?.name || b.agent_id}</Text><Text type="secondary">{users.find((u) => u.id === b.agentsql_user_id)?.username || b.agentsql_user_id}</Text></Space> },
          { title: tr("数据源与模式", "Datasource and schema"), key: "target", render: (_, b) => <Space direction="vertical" size={0}><Text>{sources.find((s) => s.id === b.datasource_id)?.name || b.datasource_id}</Text><Text code>{b.schema_name}</Text></Space> },
          { title: tr("状态", "Status"), key: "state", render: (_, b) => <Space direction="vertical" size={0}><Tag color={b.enabled ? "green" : "default"}>{b.enabled ? tr("已启用", "Enabled") : tr("已禁用", "Disabled")}</Tag><Text type="secondary">r{b.revision}</Text></Space> },
          { title: tr("操作", "Actions"), key: "actions", width: 340, render: (_, b) => <Space wrap size={4}>
            <Button size="small" onClick={() => openForm(b)}>{tr("编辑", "Edit")}</Button>
            <Button size="small" onClick={() => void run(async () => setDiagnostics(await diagnoseSQLBotBinding(b.id)))}>{tr("诊断", "Diagnose")}</Button>
            <Popconfirm title={tr("切换此映射状态？", "Change this mapping's status?")} description={tr("已有人员令牌将失效，正在执行的请求可能失败。", "Existing user tokens will be invalidated; in-flight requests may fail.")} onConfirm={() => run(async () => { await updateSQLBotBinding(b.id, { ...b, enabled: !b.enabled }); setExportText(""); await refresh(); })}><Button size="small">{b.enabled ? tr("禁用", "Disable") : tr("启用", "Enable")}</Button></Popconfirm>
            <Popconfirm title={tr("轮换连接凭据？", "Rotate connector credential?")} description={tr("旧连接凭据立即失效，请更新 SQLBot 服务器配置。", "The old connector credential stops working immediately. Update the SQLBot server configuration.")} onConfirm={() => run(async () => { const result = await rotateSQLBotConnector(b.id, b.revision); setCredential({ name: result.binding.connector_token_env, value: result.connector_token }); await refresh(); })}><Button size="small">{tr("轮换连接凭据", "Rotate connector")}</Button></Popconfirm>
            <Popconfirm title={tr("轮换此 Agent 的 Key？", "Rotate this Agent's Key?")} description={tr("所有使用此 Agent 的客户端都需要更新 Key。", "Every client using this Agent must update its Key.")} onConfirm={() => run(async () => { const result = await rotateAgentKey(b.agent_id); if (result.api_key) setCredential({ name: b.agent_key_env, value: result.api_key }); })}><Button size="small">{tr("轮换 Agent Key", "Rotate Agent Key")}</Button></Popconfirm>
            <Popconfirm title={tr("删除此映射？", "Delete this mapping?")} onConfirm={() => run(async () => { await deleteSQLBotBinding(b.id, b.revision); setExportText(""); await refresh(); })}><Button size="small" danger>{tr("删除", "Delete")}</Button></Popconfirm>
          </Space> },
        ]} />
      </Card>
      <Card title={tr("导出 SQLBot 配置", "Export SQLBot configuration")}>
        <Paragraph>{tr("配置仅包含已启用映射和环境变量名称，不包含连接凭据或 Agent Key。每个映射请使用独立的连接凭据环境变量。", "The configuration contains enabled mappings and environment variable names only. It includes no connector credentials or Agent Keys. Use a separate connector environment variable for each mapping.")}</Paragraph>
        <Space.Compact style={{ width: "100%" }}><Input aria-label={tr("AgentSQL MCP 地址", "AgentSQL MCP endpoint")} value={endpoint} onChange={(event) => { setEndpoint(event.target.value); setExportText(""); }} placeholder="https://agentsql.example/mcp" /><Button loading={busy} onClick={() => void run(async () => setExportText(JSON.stringify(await exportSQLBotConfig(endpoint), null, 2)))}>{tr("生成配置", "Generate configuration")}</Button></Space.Compact>
        {exportText ? <><Input.TextArea readOnly rows={12} value={exportText} aria-label={tr("SQLBot 配置 JSON", "SQLBot configuration JSON")} style={{ marginTop: 12, fontFamily: "monospace" }} /><Space style={{ marginTop: 8 }}><Button onClick={download}>{tr("下载配置", "Download configuration")}</Button><Text copyable={{ text: exportText }}>{tr("复制配置", "Copy configuration")}</Text></Space></> : null}
      </Card>
    </Space>
    <Modal title={editing ? tr("编辑 SQLBot 映射", "Edit SQLBot mapping") : tr("新增 SQLBot 映射", "Add SQLBot mapping")} open={formOpen} onCancel={() => { setFormOpen(false); form.resetFields(); }} onOk={() => form.submit()} confirmLoading={busy} width={660} destroyOnClose>
      <Form form={form} layout="vertical" onFinish={(values) => void save(values)}>
        <Form.Item name="workspace_id" label={tr("SQLBot 工作空间 ID", "SQLBot workspace ID")} rules={[{ required: true, pattern: /^[1-9][0-9]{0,18}$/ }]}><Input /></Form.Item>
        <Form.Item name="datasource_name" label={tr("SQLBot 数据源名称（精确匹配）", "SQLBot datasource name (exact match)")} rules={[{ required: true, max: 200 }]}><Input /></Form.Item>
        <Form.Item name="sqlbot_user_id" label={tr("SQLBot 用户 ID", "SQLBot user ID")} rules={[{ required: true, pattern: /^[1-9][0-9]{0,18}$/ }]}><Input /></Form.Item>
        <Form.Item name="agentsql_user_id" label={tr("AgentSQL 人员", "AgentSQL user")} rules={[{ required: true }]}><Select showSearch optionFilterProp="label" options={users.filter((u) => u.status === "active").map((u) => ({ value: u.id, label: `${u.username} (${u.id})` }))} /></Form.Item>
        <Form.Item name="agent_id" label={tr("专用只读 Agent", "Dedicated readonly Agent")} rules={[{ required: true }]}><Select showSearch optionFilterProp="label" options={agents.filter((a) => a.status === "active" && a.level === "readonly").map((a) => ({ value: a.id, label: `${a.name} (${a.id})` }))} /></Form.Item>
        <Form.Item name="datasource_id" label={tr("PostgreSQL/MySQL 数据源", "PostgreSQL/MySQL datasource")} rules={[{ required: true }]}><Select showSearch optionFilterProp="label" onChange={(id: string) => { const source = sources.find((item) => item.id === id); if (source?.db_type === "mysql") form.setFieldValue("schema_name", source.database); }} options={sources.filter((s) => s.db_type === "postgres" || s.db_type === "mysql").map((s) => ({ value: s.id, label: `${s.name} (${s.db_type}, ${s.id})` }))} /></Form.Item>
        <Form.Item name="schema_name" label={tr("数据库模式", "Database schema")} extra={selectedDatasource?.db_type === "mysql" ? tr("MySQL 模式固定为当前数据源的数据库名称。", "For MySQL, the schema equals this datasource's database name.") : undefined} rules={[{ required: true, pattern: /^[A-Za-z_][A-Za-z0-9_]{0,62}$/ }]}><Input readOnly={selectedDatasource?.db_type === "mysql"} /></Form.Item>
        <Form.Item name="numeric_columns" label={tr("图表数值列别名", "Numeric chart aliases")} extra={tr("仅填写需要按数值展示的输出列别名；脱敏值和无法精确转换的值仍按文本返回。", "Only list output aliases that should be numeric. Masked values and values that cannot be converted exactly remain text.")}><Select mode="tags" tokenSeparators={[","]} /></Form.Item>
        <Form.Item name="agent_key_env" label={tr("Agent Key 环境变量名称", "Agent Key environment variable")} rules={[{ required: true, pattern: /^[A-Z_][A-Z0-9_]{0,95}$/ }]}><Input /></Form.Item>
        <Form.Item name="connector_token_env" label={tr("连接凭据环境变量名称", "Connector credential environment variable")} rules={[{ required: true, pattern: /^[A-Z_][A-Z0-9_]{0,95}$/ }]}><Input /></Form.Item>
        <Form.Item name="enabled" label={tr("启用映射", "Enable mapping")} valuePropName="checked"><Switch /></Form.Item>
      </Form>
    </Modal>
    <Modal title={tr("保存本次凭据", "Save this credential")} open={credential !== null} footer={<Button onClick={() => setCredential(null)}>{tr("已保存，关闭", "Saved, close")}</Button>} onCancel={() => setCredential(null)} destroyOnClose>
      <Alert showIcon type="warning" message={tr("凭据仅显示一次，请安全保存；不要放入配置 JSON、聊天或日志。", "This credential is shown once. Store it securely; do not put it in configuration JSON, chat or logs.")} />
      <Paragraph style={{ marginTop: 16 }}>{credential?.name}</Paragraph><Text code copyable={{ text: credential?.value || "" }} style={{ overflowWrap: "anywhere" }}>{credential?.value}</Text>
    </Modal>
    <Modal title={tr("SQLBot 接入诊断", "SQLBot integration diagnostics")} open={diagnostics !== null} footer={null} onCancel={() => setDiagnostics(null)}>{diagnostics ? <SQLBotDiagnosticsView data={diagnostics} /> : null}</Modal>
  </PageContainer>;
}
