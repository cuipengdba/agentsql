import { DeleteOutlined, EditOutlined, PlusOutlined, SaveOutlined } from "@ant-design/icons";
import { Alert, Button, Descriptions, Drawer, Empty, Form, Input, Modal, Popconfirm, Radio, Select, Space, Table, Tag, message } from "antd";
import type { TableProps } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";

import { listAgents } from "@/api/agents";
import { listDatasources } from "@/api/datasources";
import { createPolicy, deletePolicy, listPolicies, updatePolicy } from "@/api/policies";
import type { AgentView, DatasourceView, PolicyInput, PolicyView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { configLabel, objectTypeMeta, policyActionMeta } from "@/constants/labels";

import { apiErrorMessage, isCanceled } from "./config/utils";

type PolicyChangeKind = "create" | "update" | "delete";

interface PolicyChange {
  kind: PolicyChangeKind;
  id: string;
  before?: PolicyView;
  after?: PolicyInput;
}

interface PolicyFormValues {
  object_type: "database" | "schema" | "table" | "column";
  object_name: string;
  action: "allow" | "deny";
  columns?: string[];
  row_filter?: string;
}

function generatePolicyID(): string {
  return `pol_${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
}

function validateObjectPattern(value: string, objectType: PolicyFormValues["object_type"]): string | null {
  if (!value) return "对象名不能为空";
  if (value.trim() !== value) return "对象名首尾不能包含空格";
  const parts = value.split(".");
  if (parts.length > 2 || parts.some((part) => !part)) return "对象名只能是裸名称、schema.table 或 schema.*";
  if (parts.length === 2 && parts[0] === "*") return "不允许 *.x 形式的通配符";
  if (objectType === "column" && (value === "*" || value.endsWith(".*"))) return "列级策略必须精确指定一张表";
  return null;
}

function cleanColumns(values: string[] | undefined): string[] {
  return Array.from(new Set((values || []).map((value) => value.trim()).filter(Boolean)));
}

function policySummary(value: PolicyView | PolicyInput | undefined): string {
  if (!value) return "—";
  const columns = value.columns ? `，列=${value.columns}` : "";
  const rowFilter = value.row_filter ? `，行过滤=${value.row_filter}` : "";
  return `${value.object_type} ${value.object_name}，${value.action}${columns}${rowFilter}`;
}

export function Policies() {
  const [form] = Form.useForm<PolicyFormValues>();
  const objectType = Form.useWatch("object_type", form) as PolicyFormValues["object_type"] | undefined;
  const [agents, setAgents] = useState<AgentView[]>([]);
  const [datasources, setDatasources] = useState<DatasourceView[]>([]);
  const [agentID, setAgentID] = useState("");
  const [datasourceID, setDatasourceID] = useState("");
  const [list, setList] = useState<PolicyView[]>([]);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<PolicyView | null>(null);
  const [changes, setChanges] = useState<PolicyChange[]>([]);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const mountedRef = useRef(true);
  const listControllerRef = useRef<AbortController | null>(null);
  const listSequenceRef = useRef(0);

  useEffect(() => {
    let active = true;
    void Promise.all([listAgents({ page: 1, page_size: 100 }), listDatasources({ page: 1, page_size: 100 })])
      .then(([agentPage, datasourcePage]) => {
        if (!active) return;
        setAgents(agentPage.list || []);
        setDatasources(datasourcePage.list || []);
      })
      .catch((error: unknown) => {
        if (active && !isCanceled(error)) void message.error(apiErrorMessage(error, "加载 Agent 与数据源失败"));
      });
    return () => { active = false; };
  }, []);

  const load = useCallback(async () => {
    if (!agentID || !datasourceID) {
      setList([]);
      setFailed(false);
      return;
    }
    listControllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = ++listSequenceRef.current;
    listControllerRef.current = controller;
    setLoading(true);
    setFailed(false);
    try {
      const response = await listPolicies({ agent_id: agentID, datasource_id: datasourceID, page: 1, page_size: 100 });
      if (!mountedRef.current || controller.signal.aborted || sequence !== listSequenceRef.current) return;
      setList(response.list || []);
    } catch (error: unknown) {
      if (!controller.signal.aborted && !isCanceled(error) && mountedRef.current) setFailed(true);
    } finally {
      if (listControllerRef.current === controller) listControllerRef.current = null;
      if (mountedRef.current && sequence === listSequenceRef.current) setLoading(false);
    }
  }, [agentID, datasourceID]);

  useEffect(() => {
    mountedRef.current = true;
    void load();
    return () => {
      mountedRef.current = false;
      listControllerRef.current?.abort();
      listSequenceRef.current += 1;
    };
  }, [load]);

  const changeScope = (nextAgent: string, nextDatasource: string) => {
    setAgentID(nextAgent);
    setDatasourceID(nextDatasource);
    setChanges([]);
    setList([]);
  };

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ object_type: "table", action: "allow", columns: [] });
    setDrawerOpen(true);
  };

  const openEdit = (policy: PolicyView) => {
    setEditing(policy);
    form.setFieldsValue({
      object_type: policy.object_type as PolicyFormValues["object_type"],
      object_name: policy.object_name,
      action: policy.action as PolicyFormValues["action"],
      columns: policy.columns?.split(",").map((value) => value.trim()).filter(Boolean) || [],
      row_filter: policy.row_filter || undefined,
    });
    setDrawerOpen(true);
  };

  const queueChange = (change: PolicyChange) => {
    setChanges((current) => {
      const withoutSame = current.filter((item) => item.id !== change.id);
      return [...withoutSame, change];
    });
  };

  const queueForm = async () => {
    let values: PolicyFormValues;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    const objectName = values.object_name;
    const patternError = validateObjectPattern(objectName, values.object_type);
    if (patternError) {
      form.setFields([{ name: "object_name", errors: [patternError] }]);
      return;
    }
    const columns = cleanColumns(values.columns);
    if (values.object_type === "column" && columns.length === 0) {
      form.setFields([{ name: "columns", errors: ["列级策略至少需要一个授权列"] }]);
      return;
    }
    const input: PolicyInput = {
      id: editing?.id || generatePolicyID(),
      agent_id: agentID,
      datasource_id: datasourceID,
      object_type: values.object_type,
      object_name: objectName,
      action: values.object_type === "column" ? "allow" : values.action,
    };
    if (values.object_type === "column") input.columns = columns.join(",");
    if (values.row_filter?.trim()) input.row_filter = values.row_filter.trim();
    queueChange({ kind: editing ? "update" : "create", id: input.id, before: editing || undefined, after: input });
    setDrawerOpen(false);
    void message.success("变更已加入待提交清单");
  };

  const queueDelete = (policy: PolicyView) => {
    const pendingCreate = changes.find((change) => change.id === policy.id && change.kind === "create");
    if (pendingCreate) {
      setChanges((current) => current.filter((change) => change.id !== policy.id));
      return;
    }
    queueChange({ kind: "delete", id: policy.id, before: policy });
  };

  const commit = async () => {
    if (changes.length === 0) return;
    setSaving(true);
    let succeeded = 0;
    try {
      for (const change of changes) {
        if (!mountedRef.current) return;
        if (change.kind === "create" && change.after) await createPolicy(change.after);
        else if (change.kind === "update" && change.after) await updatePolicy(change.id, change.after);
        else if (change.kind === "delete") await deletePolicy(change.id);
        succeeded += 1;
      }
      if (!mountedRef.current) return;
      setChanges([]);
      setPreviewOpen(false);
      void message.success(`已成功提交 ${succeeded} 项策略变更`);
    } catch (error: unknown) {
      if (mountedRef.current) {
        setChanges((current) => current.slice(succeeded));
        setPreviewOpen(false);
        void message.error(`提交在第 ${succeeded + 1} 项停止，已成功 ${succeeded} 项：${apiErrorMessage(error, "策略保存失败")}`);
      }
    } finally {
      if (mountedRef.current) {
        setSaving(false);
        void load();
      }
    }
  };

  const columns: TableProps<PolicyView>["columns"] = [
    { title: "对象类型", dataIndex: "object_type", width: 150, render: (value: string) => <Tag color={objectTypeMeta[value as keyof typeof objectTypeMeta]?.color}>{configLabel(objectTypeMeta, value)}</Tag> },
    { title: "对象名", dataIndex: "object_name", width: 220, ellipsis: true, render: (value: string) => <code>{value}</code> },
    { title: "动作", dataIndex: "action", width: 90, render: (value: string) => <Tag color={policyActionMeta[value as keyof typeof policyActionMeta]?.color}>{configLabel(policyActionMeta, value)}</Tag> },
    { title: "授权列", dataIndex: "columns", width: 220, ellipsis: true, render: (value: string | null) => value || "—" },
    { title: "行过滤", dataIndex: "row_filter", ellipsis: true, render: (value: string | null) => value || "—" },
    { title: "操作", key: "action", width: 150, render: (_: unknown, row: PolicyView) => <Space size={4}><Button type="link" size="small" icon={<EditOutlined />} onClick={() => openEdit(row)}>编辑</Button><Popconfirm title="加入待删除清单？" onConfirm={() => queueDelete(row)}><Button type="link" danger size="small" icon={<DeleteOutlined />}>删除</Button></Popconfirm></Space> },
  ];

  return (
    <PageContainer title="权限" subtitle="配置 Agent 可访问的数据库对象与列级白名单" extra={<Space><Button icon={<SaveOutlined />} disabled={changes.length === 0} onClick={() => setPreviewOpen(true)}>提交变更 ({changes.length})</Button><Button type="primary" icon={<PlusOutlined />} disabled={!agentID || !datasourceID} onClick={openCreate}>新增策略</Button></Space>}>
      <section className="cfg-scope-filter">
        <Select className="cfg-scope-select" allowClear showSearch optionFilterProp="label" value={agentID || undefined} placeholder="先选择 Agent" options={agents.map((agent) => ({ value: agent.id, label: agent.name || agent.id }))} onChange={(value) => changeScope(value || "", datasourceID)} />
        <Select className="cfg-scope-select" allowClear showSearch optionFilterProp="label" value={datasourceID || undefined} placeholder="再选择数据源" options={datasources.map((source) => ({ value: source.id, label: `${source.name || source.id} · ${source.db_type}` }))} onChange={(value) => changeScope(agentID, value || "")} />
      </section>
      {!agentID || !datasourceID ? <div className="cfg-empty"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="先选择 Agent 与数据源" /></div> : <>
        {failed ? <Alert className="cfg-inline-alert" type="error" showIcon message="策略加载失败" action={<Button size="small" onClick={() => void load()}>重试</Button>} /> : null}
        <div className="cfg-table"><Table<PolicyView> rowKey="id" columns={columns} dataSource={list} loading={loading} pagination={false} scroll={{ x: 1_050 }} /></div>
      </>}

      <Drawer open={drawerOpen} title={editing ? "编辑策略" : "新增策略"} width={540} destroyOnClose onClose={() => setDrawerOpen(false)} extra={<Button type="primary" onClick={() => void queueForm()}>加入待提交</Button>}>
        <Descriptions className="cfg-scope-summary" size="small" column={1} bordered><Descriptions.Item label="Agent">{agentID}</Descriptions.Item><Descriptions.Item label="数据源">{datasourceID}</Descriptions.Item></Descriptions>
        <Form<PolicyFormValues> className="cfg-form" form={form} layout="vertical" onValuesChange={(changed: Partial<PolicyFormValues>) => { if (changed.object_type === "column") form.setFieldValue("action", "allow"); }}>
          <Form.Item name="object_type" label="对象类型" rules={[{ required: true }]}><Select options={Object.entries(objectTypeMeta).map(([value, meta]) => ({ value, label: meta.label }))} /></Form.Item>
          <Form.Item name="object_name" label="对象名" rules={[{ required: true, message: "请输入对象名" }]}><Input placeholder="* / schema.* / schema.table / table" /></Form.Item>
          <Form.Item name="action" label="动作" rules={[{ required: true }]}><Radio.Group disabled={objectType === "column"} options={Object.entries(policyActionMeta).map(([value, meta]) => ({ value, label: meta.label }))} /></Form.Item>
          {objectType === "column" ? <Alert className="cfg-form-note" type="info" showIcon message="列级策略只支持 allow，拒绝列请从白名单中移除" /> : null}
          {objectType === "column" ? <Form.Item name="columns" label="授权列" rules={[{ required: true, type: "array", min: 1 }]}><Select mode="tags" tokenSeparators={[","]} placeholder="输入列名后回车" /></Form.Item> : null}
          <Form.Item name="row_filter" label="行过滤"><Input.TextArea autoSize={{ minRows: 2, maxRows: 5 }} /></Form.Item>
        </Form>
      </Drawer>

      <Modal open={previewOpen} title="策略变更预览" width={720} okText="确认并串行提交" confirmLoading={saving} onOk={() => void commit()} onCancel={() => setPreviewOpen(false)}>
        <div className="cfg-diff-list">{changes.map((change, index) => <div className="cfg-diff" key={`${change.id}-${index}`}><Tag color={change.kind === "delete" ? "error" : change.kind === "create" ? "success" : "processing"}>{change.kind === "create" ? "新增" : change.kind === "update" ? "修改" : "删除"}</Tag><code>{change.id}</code><div><span>变更前</span><p>{policySummary(change.before)}</p></div><div><span>变更后</span><p>{policySummary(change.after)}</p></div></div>)}</div>
      </Modal>
    </PageContainer>
  );
}
