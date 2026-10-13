import { CheckCircleOutlined, ReloadOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Col, Row, Space, Steps, Typography } from "antd";
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { listAgents } from "@/api/agents";
import { listDatasources } from "@/api/datasources";
import { listPolicies } from "@/api/policies";
import { PageContainer } from "@/components/PageContainer";

interface Counts { datasources: number; agents: number; policies: number }

const actions = [
  { path: "/datasources", label: "配置数据源", detail: "使用最小权限的数据库账号，保存后先测试连接。" },
  { path: "/agents", label: "创建 Agent", detail: "先选只读级别；API Key 只显示一次，请安全保存。" },
  { path: "/policies", label: "授予最小权限", detail: "只允许目标 Agent 读取需要的库、表和列，敏感列按策略脱敏。" },
  { path: "/audit", label: "核对审计", detail: "从真实 MCP 客户端发起允许与拒绝请求，然后核对 Agent、数据源及决策记录。" },
];

export function Onboarding() {
  const [counts, setCounts] = useState<Counts | null>(null);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(false);
  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const [datasources, agents, policies] = await Promise.all([
        listDatasources({ page: 1, page_size: 1 }),
        listAgents({ page: 1, page_size: 1 }),
        listPolicies({ page: 1, page_size: 1 }),
      ]);
      setCounts({ datasources: datasources.total, agents: agents.total, policies: policies.total });
      setError(false);
    } catch { setError(true); } finally { setLoading(false); }
  }, []);
  useEffect(() => { void reload(); }, [reload]);

  const current = !counts?.datasources ? 0 : !counts.agents ? 1 : !counts.policies ? 2 : 3;
  return (
    <PageContainer title="接入向导" subtitle="从数据源到真实 MCP 请求，逐步完成最小权限接入。" extra={<Button icon={<ReloadOutlined />} loading={loading} onClick={() => void reload()}>刷新状态</Button>}>
      {error && <Alert type="error" showIcon message="配置状态读取失败" description="确认当前账号具有数据源和策略查看权限后重试。" style={{ marginBottom: 16 }} />}
      <Card style={{ marginBottom: 20 }}>
        <Steps current={current} responsive items={[{ title: "数据源" }, { title: "Agent" }, { title: "权限" }, { title: "客户端与审计" }]} />
        <Typography.Paragraph type="secondary" style={{ marginTop: 18, marginBottom: 0 }}>前三项根据当前租户中是否存在配置显示进度。它们不代表连接、授权或安全验收已经通过；最后一步需在真实客户端验证。</Typography.Paragraph>
      </Card>
      <Row gutter={[16, 16]}>
        {actions.map((action, index) => <Col xs={24} md={12} key={action.path}>
          <Card title={<Space>{index < current && <CheckCircleOutlined style={{ color: "#3f9d6e" }} />}{index + 1}. {action.label}</Space>} extra={<Link to={action.path}>打开</Link>} style={{ height: "100%" }}>
            <Typography.Paragraph style={{ marginBottom: 0 }}>{action.detail}</Typography.Paragraph>
          </Card>
        </Col>)}
      </Row>
      <Card title="连接 MCP 客户端" style={{ marginTop: 20 }}>
        <Typography.Paragraph>Claude Desktop 使用本机 stdio 或 HTTP 桥接；Dify 自托管使用 Streamable HTTP <code>/mcp</code>。两者都使用 Agent API Key，不能使用管理员登录 token。</Typography.Paragraph>
        <Alert type="info" showIcon message="客户端可达地址因部署拓扑而异" description="Dify 容器里的 127.0.0.1 指向 Dify 自身；仅为 AgentSQL 地址配置必要的内网访问。Dify 1.17.1 还需设置 MCP-Protocol-Version: 2025-06-18 请求头。" />
        <Typography.Paragraph style={{ marginTop: 14, marginBottom: 0 }}><a href="https://github.com/cuipengdba/agentsql/blob/main/docs/INTEGRATIONS.md" target="_blank" rel="noreferrer">查看 Claude Desktop 与 Dify 接入步骤</a></Typography.Paragraph>
      </Card>
    </PageContainer>
  );
}
