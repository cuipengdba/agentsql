import { DownOutlined, SearchOutlined, UndoOutlined, UpOutlined } from "@ant-design/icons";
import { Button, Col, DatePicker, Form, Input, Row, Select, Space, message } from "antd";
import type { Dayjs } from "dayjs";
import { useEffect, useState } from "react";

import { listAgents } from "@/api/agents";
import { listDatasources } from "@/api/datasources";
import type { AuditQuery } from "@/api/types";
import { decisionMeta, stmtTypeLabels } from "@/constants/labels";

const { RangePicker } = DatePicker;

interface FilterFormValues {
  timeRange?: [Dayjs | null, Dayjs | null] | null;
  agent_id?: string;
  datasource_id?: string;
  session_id?: string;
  mcp_tool?: string;
  decisions?: string[];
  stmt_types?: string[];
  risk_min?: number;
  risk_max?: number;
  object?: string;
  keyword?: string;
}

export type AppliedAuditQuery = Omit<AuditQuery, "page" | "page_size">;

interface AuditFiltersProps {
  onApply: (query: AppliedAuditQuery) => void;
}

interface SelectOption {
  label: string;
  value: string;
}

function compactText(value: string | undefined): string | undefined {
  const compact = value?.trim();
  return compact || undefined;
}

function toQuery(values: FilterFormValues): AppliedAuditQuery {
  const [start, end] = values.timeRange || [];
  const query: AppliedAuditQuery = {};
  if (start) query.time_start = start.toISOString();
  if (end) query.time_end = end.toISOString();
  if (values.agent_id) query.agent_id = values.agent_id;
  if (values.datasource_id) query.datasource_id = values.datasource_id;
  const sessionID = compactText(values.session_id);
  const mcpTool = compactText(values.mcp_tool);
  if (sessionID) query.session_id = sessionID;
  if (mcpTool) query.mcp_tool = mcpTool;
  if (values.decisions?.length) query.decisions = values.decisions.join(",");
  if (values.stmt_types?.length) query.stmt_types = values.stmt_types.join(",");
  if (values.risk_min !== undefined) query.risk_min = values.risk_min;
  if (values.risk_max !== undefined) query.risk_max = values.risk_max;
  const object = compactText(values.object);
  const keyword = compactText(values.keyword);
  if (object) query.object = object;
  if (keyword) query.keyword = keyword;
  return query;
}

export function AuditFilters({ onApply }: AuditFiltersProps) {
  const [form] = Form.useForm<FilterFormValues>();
  const [expanded, setExpanded] = useState(false);
  const [optionsLoading, setOptionsLoading] = useState(true);
  const [agentOptions, setAgentOptions] = useState<SelectOption[]>([]);
  const [datasourceOptions, setDatasourceOptions] = useState<SelectOption[]>([]);

  useEffect(() => {
    let active = true;
    void Promise.all([listAgents({ page: 1, page_size: 100 }), listDatasources({ page: 1, page_size: 100 })])
      .then(([agents, datasources]) => {
        if (!active) return;
        setAgentOptions(agents.list.map((agent) => ({ label: agent.name || agent.id, value: agent.id })));
        setDatasourceOptions(datasources.list.map((source) => ({ label: `${source.name || source.id} · ${source.db_type}`, value: source.id })));
      })
      .catch(() => {
        if (active) void message.warning("筛选项加载失败，可继续使用其它条件查询");
      })
      .finally(() => {
        if (active) setOptionsLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);

  const submit = (values: FilterFormValues) => {
    if (values.risk_min !== undefined && values.risk_max !== undefined && values.risk_min > values.risk_max) {
      void message.warning("风险等级下限不能大于上限");
      return;
    }
    onApply(toQuery(values));
  };

  const reset = () => {
    form.resetFields();
    onApply({});
  };

  const decisionOptions = ["allow", "deny", "approve", "warn", "error"].map((value) => ({
    value,
    label: value === "error" ? "错误" : decisionMeta[value as keyof typeof decisionMeta].label,
  }));
  const statementOptions = Object.entries(stmtTypeLabels).map(([value, label]) => ({ value, label }));
  const riskOptions = [1, 2, 3, 4, 5].map((value) => ({ value, label: `等级 ${value}` }));

  return (
    <section className="audit-filters">
      <Form<FilterFormValues> form={form} layout="vertical" onFinish={submit}>
        <Row gutter={[12, 0]}>
          <Col xs={24} md={12} xl={8}>
            <Form.Item name="timeRange" label="时间范围">
              <RangePicker showTime allowClear className="audit-filter-control" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={6} xl={4}>
            <Form.Item name="agent_id" label="Agent">
              <Select allowClear showSearch optionFilterProp="label" loading={optionsLoading} options={agentOptions} placeholder="全部 Agent" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} md={6} xl={4}>
            <Form.Item name="datasource_id" label="数据源">
              <Select allowClear showSearch optionFilterProp="label" loading={optionsLoading} options={datasourceOptions} placeholder="全部数据源" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} xl={4}>
            <Form.Item name="decisions" label="决策">
              <Select mode="multiple" allowClear options={decisionOptions} maxTagCount="responsive" placeholder="全部决策" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12} xl={4}>
            <Form.Item name="stmt_types" label="语句类型">
              <Select mode="multiple" allowClear options={statementOptions} maxTagCount="responsive" placeholder="全部类型" />
            </Form.Item>
          </Col>
        </Row>
        {expanded ? (
          <Row gutter={[12, 0]}>
            <Col xs={24} sm={12} xl={4}>
              <Form.Item name="session_id" label="会话 ID">
                <Input allowClear placeholder="session_id" />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} xl={4}>
              <Form.Item name="mcp_tool" label="MCP 工具">
                <Input allowClear placeholder="query / execute_write" />
              </Form.Item>
            </Col>
            <Col xs={12} sm={6} xl={3}>
              <Form.Item name="risk_min" label="最低风险">
                <Select allowClear options={riskOptions} placeholder="1" />
              </Form.Item>
            </Col>
            <Col xs={12} sm={6} xl={3}>
              <Form.Item name="risk_max" label="最高风险">
                <Select allowClear options={riskOptions} placeholder="5" />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12} xl={5}>
              <Form.Item name="object" label="数据库对象">
                <Input allowClear placeholder="schema.table" />
              </Form.Item>
            </Col>
            <Col xs={24} xl={5}>
              <Form.Item name="keyword" label="SQL 关键词">
                <Input allowClear placeholder="搜索 SQL 文本" />
              </Form.Item>
            </Col>
          </Row>
        ) : null}
        <div className="audit-filter-actions">
          <Button type="text" icon={expanded ? <UpOutlined /> : <DownOutlined />} onClick={() => setExpanded((value) => !value)}>
            {expanded ? "收起条件" : "更多条件"}
          </Button>
          <Space>
            <Button icon={<UndoOutlined />} onClick={reset}>重置</Button>
            <Button type="primary" htmlType="submit" icon={<SearchOutlined />}>查询</Button>
          </Space>
        </div>
      </Form>
    </section>
  );
}
