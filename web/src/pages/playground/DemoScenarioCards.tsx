import {
  ArrowRightOutlined,
  AuditOutlined,
  DashboardOutlined,
  PlayCircleOutlined,
} from "@ant-design/icons";
import { Button, Card, Tag, Tooltip } from "antd";
import type { KeyboardEvent } from "react";
import { useNavigate } from "react-router-dom";

import type {
  PlaygroundDemoAgentProfile,
  PlaygroundDemoDatasourceID,
} from "@/api/types";

export interface DemoRunScenario {
  key: string;
  title: string;
  sql: string;
  datasourceID: PlaygroundDemoDatasourceID;
  agentProfile: PlaygroundDemoAgentProfile;
  expected: string;
  expectedDecision: "allow" | "warn" | "deny" | "approve";
}

const demoRunScenarios: readonly DemoRunScenario[] = [
  {
    key: "masked-join-allow",
    title: "列授权 + 脱敏",
    sql: "SELECT c.id, c.full_name, c.phone, c.email, c.region, o.status FROM public.demo_b2_customers c JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1",
    datasourceID: "ds-demo-pg",
    agentProfile: "ro",
    expected: "allow，返回 1 行；phone/email 共 2 个单元格被脱敏（如 138****0001、u***@example.test），并返回 audit_id。",
    expectedDecision: "allow",
  },
  {
    key: "column-grant-missing",
    title: "列授权缺失",
    sql: "SELECT id, name FROM public.products WHERE id=1",
    datasourceID: "ds-demo-pg",
    agentProfile: "ro",
    expected: "deny，原因为 AUTH_COLUMN_GRANT_MISSING；表级允许不会绕过列级绑定。",
    expectedDecision: "deny",
  },
  {
    key: "agent-denied",
    title: "对象越权",
    sql: "SELECT id, note FROM public.internal_notes LIMIT 5",
    datasourceID: "ds-demo-pg",
    agentProfile: "ro",
    expected: "deny，命中 R010 / AUTH_AGENT_DENIED，不返回 internal_notes 内容。",
    expectedDecision: "deny",
  },
  {
    key: "ddl-denied",
    title: "DDL 越权",
    sql: "DROP TABLE public.customers",
    datasourceID: "ds-demo-pg",
    agentProfile: "ro",
    expected: "deny，当前 Agent 没有 DDL 权限；语句不会触达数据库。",
    expectedDecision: "deny",
  },
  {
    key: "write-approval",
    title: "写操作转人工",
    sql: "UPDATE public.demo_tx_accounts SET status='x' WHERE id=1",
    datasourceID: "ds-demo-pg",
    agentProfile: "dml",
    expected: "approve，Live Demo 中的 UPDATE/DELETE 默认进入 DBA 人工审批，审批前不执行。",
    expectedDecision: "approve",
  },
];

interface DemoScenarioCardsProps {
  activeKey: string | null;
  loading: boolean;
  latestAuditID: number | null;
  onRun: (scenario: DemoRunScenario) => void;
}

function profileLabel(profile: PlaygroundDemoAgentProfile): string {
  return profile === "ro" ? "只读演示（ro）" : "DML 拦截演示（dml）";
}

function datasourceLabel(datasourceID: PlaygroundDemoDatasourceID): string {
  return datasourceID === "ds-demo-pg" ? "PostgreSQL · ds-demo-pg" : "MySQL · ds-demo-mysql";
}

export function DemoScenarioCards({ activeKey, loading, latestAuditID, onRun }: DemoScenarioCardsProps) {
  const navigate = useNavigate();

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>, scenario: DemoRunScenario) => {
    if (!loading && (event.key === "Enter" || event.key === " ")) {
      event.preventDefault();
      onRun(scenario);
    }
  };

  return (
    <section className="demo-scenarios" aria-label="Live Demo 六个演示剧本">
      <div className="playground-section-heading">
        <strong>六个真实剧本</strong>
        <span>点击前五张卡片会填入参数并立即运行</span>
      </div>
      <div className="demo-scenario-grid">
        {demoRunScenarios.map((scenario, index) => (
          <Card
            key={scenario.key}
            className={`demo-scenario-card demo-scenario-${scenario.expectedDecision}${activeKey === scenario.key ? " is-active" : ""}${loading ? " is-loading" : ""}`}
            hoverable={!loading}
            role="button"
            tabIndex={loading ? -1 : 0}
            aria-disabled={loading}
            onClick={() => {
              if (!loading) onRun(scenario);
            }}
            onKeyDown={(event) => handleKeyDown(event, scenario)}
          >
            <div className="demo-scenario-title">
              <span className="demo-scenario-number">{index + 1}</span>
              <strong>{scenario.title}</strong>
              <Tag color={scenario.expectedDecision === "allow" ? "success" : scenario.expectedDecision === "warn" || scenario.expectedDecision === "approve" ? "warning" : "error"}>
                {scenario.expectedDecision}
              </Tag>
            </div>
            <code className="demo-scenario-sql">{scenario.sql}</code>
            <div className="demo-scenario-meta">
              <span>{datasourceLabel(scenario.datasourceID)}</span>
              <span>{profileLabel(scenario.agentProfile)}</span>
            </div>
            <p><b>预期现象：</b>{scenario.expected}</p>
            <span className="demo-scenario-run"><PlayCircleOutlined /> 点击立即运行</span>
          </Card>
        ))}

        <Card className="demo-scenario-card demo-scenario-allow demo-scenario-review">
          <div className="demo-scenario-title">
            <span className="demo-scenario-number">6</span>
            <strong>审计/大屏回看</strong>
            <Tag color="blue">回看</Tag>
          </div>
          <p><b>预期现象：</b>使用最近一次运行返回的 audit_id 打开审计详情，或去总览大屏查看实时流。</p>
          <div className="demo-review-actions">
            <Tooltip title={latestAuditID === null ? "请先运行一个剧本" : `查看审计 #${latestAuditID}`}>
              <span>
                <Button
                  icon={<AuditOutlined />}
                  disabled={latestAuditID === null}
                  onClick={() => {
                    if (latestAuditID !== null) navigate(`/audit?focus=${latestAuditID}`);
                  }}
                >
                  在审计页查看
                </Button>
              </span>
            </Tooltip>
            <Button icon={<DashboardOutlined />} onClick={() => navigate("/")}>去总览大屏看实时流</Button>
          </div>
          {latestAuditID === null ? (
            <span className="demo-review-hint">请先运行一个剧本</span>
          ) : (
            <span className="demo-review-hint mono-text">最近 audit_id：{latestAuditID} <ArrowRightOutlined /></span>
          )}
        </Card>
      </div>
    </section>
  );
}
