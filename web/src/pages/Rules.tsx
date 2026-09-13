import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined, UndoOutlined } from "@ant-design/icons";
import { Alert, Button, Drawer, Form, Input, InputNumber, Popconfirm, Segmented, Select, Space, Switch, Table, Tag, Tooltip, message } from "antd";
import type { TableProps } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { createRule, deleteRule, listRules, updateRule } from "@/api/rules";
import type { RuleInput, RuleView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { configLabel, dbTypeMeta, riskLevelMeta } from "@/constants/labels";
import { ruleMeta, type RuleMeta } from "@/constants/ruleMeta";

import { apiErrorMessage, isCanceled } from "./config/utils";

type RuleFilter = "all" | "generic" | "postgres" | "mysql";

interface RuleRow {
  id: string;
  title: string;
  dbType: "all" | "postgres" | "mysql";
  risk: number;
  patternType: string;
  definition: string;
  enabled: boolean;
  builtin: boolean;
  dynamic: boolean;
  group: string;
  stored?: RuleView;
}

interface CustomRuleValues {
  id: string;
  db_type: "all" | "postgres" | "mysql";
  title: string;
  risk_level: number;
  definition: string;
  enabled: boolean;
}

function builtinRow(meta: RuleMeta, stored?: RuleView): RuleRow {
  return {
    id: meta.id,
    title: meta.title,
    dbType: meta.dbType,
    risk: meta.risk,
    patternType: meta.patternType,
    definition: stored?.definition || "",
    enabled: stored?.enabled ?? true,
    builtin: true,
    dynamic: meta.dynamic,
    group: meta.group,
    stored,
  };
}

function customRow(stored: RuleView): RuleRow {
  return {
    id: stored.id,
    title: stored.title || stored.id,
    dbType: stored.db_type as RuleRow["dbType"],
    risk: stored.risk_level,
    patternType: stored.pattern_type,
    definition: stored.definition,
    enabled: stored.enabled,
    builtin: false,
    dynamic: false,
    group: "自定义规则",
    stored,
  };
}

export function Rules() {
  const [form] = Form.useForm<CustomRuleValues>();
  const [storedRules, setStoredRules] = useState<RuleView[]>([]);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [filter, setFilter] = useState<RuleFilter>("all");
  const [keyword, setKeyword] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<RuleView | null>(null);
  const [saving, setSaving] = useState(false);
  const [savingIDs, setSavingIDs] = useState<Set<string>>(new Set());
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
      const response = await listRules({ page: 1, page_size: 100 });
      if (!mountedRef.current || controller.signal.aborted || sequence !== requestRef.current) return;
      setStoredRules(response.list || []);
    } catch (error: unknown) {
      if (!controller.signal.aborted && !isCanceled(error) && mountedRef.current) setFailed(true);
    } finally {
      if (controllerRef.current === controller) controllerRef.current = null;
      if (mountedRef.current && sequence === requestRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void load();
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      requestRef.current += 1;
    };
  }, [load]);

  const rows = useMemo(() => {
    const overrides = new Map(storedRules.map((stored) => [stored.id, stored]));
    const builtins = Object.values(ruleMeta).map((meta) => builtinRow(meta, overrides.get(meta.id)));
    const custom = storedRules.filter((stored) => !stored.builtin && !ruleMeta[stored.id]).map(customRow);
    const search = keyword.trim().toLowerCase();
    return [...builtins, ...custom].filter((row) => {
      const dialectMatch = filter === "all" || (filter === "generic" ? row.dbType === "all" : row.dbType === filter);
      const searchMatch = !search || row.id.toLowerCase().includes(search) || row.title.toLowerCase().includes(search);
      return dialectMatch && searchMatch;
    });
  }, [filter, keyword, storedRules]);

  const markSaving = (id: string, active: boolean) => {
    setSavingIDs((current) => {
      const next = new Set(current);
      if (active) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const toggleBuiltin = async (row: RuleRow, enabled: boolean) => {
    markSaving(row.id, true);
    try {
      if (row.stored) {
        await updateRule(row.id, { enabled });
      } else {
        await createRule({
          id: row.id,
          db_type: row.dbType,
          title: row.title,
          risk_level: row.risk,
          pattern_type: "ast",
          definition: "",
          enabled,
          builtin: true,
        });
      }
      if (!mountedRef.current) return;
      setStoredRules((current) => {
        const existing = current.find((item) => item.id === row.id);
        if (existing) return current.map((item) => item.id === row.id ? { ...item, enabled } : item);
        return [...current, {
          id: row.id, db_type: row.dbType, title: row.title, risk_level: row.risk,
          pattern_type: "ast", definition: "", enabled, builtin: true,
          created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
        }];
      });
      void message.success(`${row.id} 已${enabled ? "启用" : "禁用"}，下一次真实网关请求生效`);
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "保存规则开关失败"));
    } finally {
      if (mountedRef.current) markSaving(row.id, false);
    }
  };

  const restoreBuiltin = async (row: RuleRow) => {
    if (!row.stored) {
      void message.info("该规则当前已使用内置默认值");
      return;
    }
    await toggleBuiltin(row, true);
  };

  const toggleCustom = async (row: RuleRow, enabled: boolean) => {
    markSaving(row.id, true);
    try {
      await updateRule(row.id, { enabled });
      if (!mountedRef.current) return;
      setStoredRules((current) => current.map((item) => item.id === row.id ? { ...item, enabled } : item));
      void message.success(`${row.id} 已${enabled ? "启用" : "禁用"}`);
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "保存规则开关失败"));
    } finally {
      if (mountedRef.current) markSaving(row.id, false);
    }
  };

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ db_type: "all", risk_level: 3, definition: "", enabled: true });
    setDrawerOpen(true);
  };

  const openEdit = (rule: RuleView) => {
    setEditing(rule);
    form.setFieldsValue({
      id: rule.id,
      db_type: rule.db_type as CustomRuleValues["db_type"],
      title: rule.title,
      risk_level: rule.risk_level,
      definition: rule.definition,
      enabled: rule.enabled,
    });
    setDrawerOpen(true);
  };

  const submitCustom = async () => {
    let values: CustomRuleValues;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    const input: RuleInput = {
      id: values.id,
      db_type: values.db_type,
      title: values.title.trim(),
      risk_level: values.risk_level,
      pattern_type: "ast",
      definition: values.definition,
      enabled: values.enabled,
      builtin: false,
    };
    setSaving(true);
    try {
      if (editing) await updateRule(editing.id, input);
      else await createRule(input);
      if (!mountedRef.current) return;
      setDrawerOpen(false);
      void message.success(editing ? "自定义规则已更新" : "自定义规则已创建");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, editing ? "更新自定义规则失败" : "创建自定义规则失败"));
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  const removeCustom = async (rule: RuleView) => {
    markSaving(rule.id, true);
    try {
      await deleteRule(rule.id);
      if (!mountedRef.current) return;
      setStoredRules((current) => current.filter((item) => item.id !== rule.id));
      void message.success("自定义规则已删除");
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "删除自定义规则失败"));
    } finally {
      if (mountedRef.current) markSaving(rule.id, false);
    }
  };

  const columns: TableProps<RuleRow>["columns"] = [
    { title: "规则 ID", dataIndex: "id", width: 100, render: (value: string) => <code>{value}</code> },
    { title: "中文名", dataIndex: "title", width: 220, ellipsis: true },
    { title: "方言", dataIndex: "dbType", width: 110, render: (value: string) => <Tag color={dbTypeMeta[value as keyof typeof dbTypeMeta]?.color}>{configLabel(dbTypeMeta, value)}</Tag> },
    { title: "分组", dataIndex: "group", width: 150, ellipsis: true },
    { title: "风险", dataIndex: "risk", width: 105, render: (value: number) => <Tag color={riskLevelMeta[value as keyof typeof riskLevelMeta]?.color}>{riskLevelMeta[value as keyof typeof riskLevelMeta]?.label || value}</Tag> },
    { title: "匹配类型", dataIndex: "patternType", width: 100, render: (value: string) => <code>{value || "—"}</code> },
    { title: "类别", dataIndex: "dynamic", width: 170, render: (dynamic: boolean) => dynamic ? <Tooltip title="依赖执行计划、索引或事务运行态"><Tag color="orange">动态 · 依赖运行态</Tag></Tooltip> : <Tag>静态</Tag> },
    { title: "来源", dataIndex: "builtin", width: 90, render: (builtin: boolean) => <Tag color={builtin ? "processing" : "default"}>{builtin ? "内置" : "自定义"}</Tag> },
    { title: "启用", dataIndex: "enabled", width: 90, render: (enabled: boolean, row: RuleRow) => <Switch checked={enabled} loading={savingIDs.has(row.id)} onChange={(checked) => void (row.builtin ? toggleBuiltin(row, checked) : toggleCustom(row, checked))} /> },
    {
      title: "操作", key: "action", fixed: "right", width: 180,
      render: (_: unknown, row: RuleRow) => row.builtin ? <Button type="link" size="small" icon={<UndoOutlined />} loading={savingIDs.has(row.id)} disabled={!row.stored} onClick={() => void restoreBuiltin(row)}>恢复默认</Button> : <Space size={4}><Button type="link" size="small" icon={<EditOutlined />} disabled={savingIDs.has(row.id)} onClick={() => row.stored && openEdit(row.stored)}>编辑</Button><Popconfirm title="确认删除自定义规则？" onConfirm={() => row.stored && void removeCustom(row.stored)}><Button type="link" danger size="small" icon={<DeleteOutlined />} loading={savingIDs.has(row.id)}>删除</Button></Popconfirm></Space>,
    },
  ];

  return (
    <PageContainer title="规则" subtitle="管理全局规则开关与自定义 AST 规则" extra={<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建自定义规则</Button>}>
      <Alert className="cfg-inline-alert" type="info" showIcon message="开关保存后对真实网关的下一次 SQL 请求立即生效、无需重启；拦截演示台（T21）为零连库纯静态演示，不读取此处开关。动态规则需真实连库执行 EXPLAIN 或读取运行态才会触发。" />
      <section className="cfg-rule-filter">
        <Segmented<RuleFilter> value={filter} options={[{ value: "all", label: "全部" }, { value: "generic", label: "通用" }, { value: "postgres", label: "PostgreSQL" }, { value: "mysql", label: "MySQL" }]} onChange={setFilter} />
        <Input.Search allowClear value={keyword} onChange={(event) => setKeyword(event.target.value)} placeholder="搜索规则 ID 或中文名" className="cfg-rule-search" />
        <Button icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>刷新</Button>
      </section>
      {failed ? <Alert className="cfg-inline-alert" type="error" showIcon message="规则覆盖加载失败" action={<Button size="small" onClick={() => void load()}>重试</Button>} /> : null}
      <div className="cfg-table"><Table<RuleRow> rowKey="id" columns={columns} dataSource={rows} loading={loading} pagination={{ pageSize: 20, showSizeChanger: false }} scroll={{ x: 1_410 }} /></div>

      <Drawer open={drawerOpen} title={editing ? "编辑自定义规则" : "新建自定义规则"} width={540} destroyOnClose onClose={() => setDrawerOpen(false)} extra={<Button type="primary" loading={saving} onClick={() => void submitCustom()}>保存</Button>}>
        <Form<CustomRuleValues> form={form} layout="vertical">
          <Form.Item name="id" label="规则 ID" rules={[{ required: true, whitespace: true }]}><Input disabled={Boolean(editing)} placeholder="CUSTOM_001" /></Form.Item>
          <Form.Item name="db_type" label="适用方言" rules={[{ required: true }]}><Select options={Object.entries(dbTypeMeta).map(([value, meta]) => ({ value, label: meta.label }))} /></Form.Item>
          <Form.Item name="title" label="标题" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="risk_level" label="风险等级" rules={[{ required: true }]}><InputNumber min={1} max={5} className="cfg-full-width" /></Form.Item>
          <Form.Item label="匹配类型"><Input value="ast" disabled /></Form.Item>
          <Form.Item name="definition" label="规则定义"><Input.TextArea autoSize={{ minRows: 5, maxRows: 12 }} /></Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked"><Switch /></Form.Item>
        </Form>
      </Drawer>
    </PageContainer>
  );
}
