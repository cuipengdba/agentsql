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
  expectedDecision: "allow" | "warn" | "deny";
}

const demoRunScenarios: readonly DemoRunScenario[] = [
  {
    key: "normal-allow",
    title: "正常放行",
    sql: "SELECT id, full_name, region FROM customers ORDER BY id LIMIT 5",
    datasourceID: "ds-demo-pg",
    agentProfile: "ro",
    expected: "allow，真实返回 ≤5 行，有 audit_id。",
    expectedDecision: "allow",
  },
  {
    key: "write-blocked",
    title: "无 WHERE 写拦截",
    sql: "UPDATE orders SET status = 'cancelled'",
    datasourceID: "ds-demo-mysql",
    agentProfile: "dml",
    expected: "deny，hits 含 R002 与 DEMO_NON_SELECT，结果为空、不显示数据库报错。",
    expectedDecision: "deny",
  },
  {
    key: "masked-columns",
    title: "脱敏",
    sql: "SELECT id, full_name, phone, email FROM customers ORDER BY id LIMIT 5",
    datasourceID: "ds-demo-mysql",
    agentProfile: "ro",
    expected: "allow，phone/email 列全部掩码（如 138****8000），表格与原始 JSON 中都看不到完整手机号/邮箱。",
    expectedDecision: "allow",
  },
  {
    key: "large-result-warning",
    title: "大结果告警",
    sql: "SELECT id, status, amount FROM orders WHERE status = 'paid'",
    datasourceID: "ds-demo-pg",
    agentProfile: "ro",
    expected: "warn，hits 含 R005，est_scan_rows>20，返回 ≤20 行且 truncated=true。",
    expectedDecision: "warn",
  },
  {
    key: "unauthorized-deny",
    title: "越权拒绝",
    sql: "SELECT id, note FROM internal_notes LIMIT 5",
    datasourceID: "ds-demo-pg",
    agentProfile: "ro",
    expected: "deny，hits 含 R010，不返回 internal_notes 内容。",
    expectedDecision: "deny",
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
              <Tag color={scenario.expectedDecision === "allow" ? "success" : scenario.expectedDecision === "warn" ? "warning" : "error"}>
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
