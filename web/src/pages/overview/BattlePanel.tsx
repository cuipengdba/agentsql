import { SafetyCertificateFilled } from "@ant-design/icons";
import { Card, Empty, Tabs } from "antd";
import type { TabsProps } from "antd";

import type { AgentRankingItem, DashboardSummary, RiskTopEntry } from "@/api/types";
import { getRuleMeta } from "@/constants/ruleMeta";

function finiteNumber(value: number | null | undefined): number {
  return value !== null && value !== undefined && Number.isFinite(value) ? value : 0;
}

export function formatRows(value: number | null | undefined): string {
  const rows = finiteNumber(value);
  return rows >= 10_000 ? `${(rows / 10_000).toFixed(1)} 万` : rows.toLocaleString("zh-CN");
}

function AgentRanking({ ranking }: { ranking: AgentRankingItem[] }) {
  const maxBlocked = Math.max(1, ...ranking.map((item) => finiteNumber(item.blocked_count)));
  return (
    <div className="battle-tab-panel">
      {ranking.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无被拦截 Agent" /> : (
        <div className="ranking-list">
          {ranking.slice(0, 5).map((item) => {
            const name = item.name?.trim() || item.agent_id?.slice(0, 12) || "—";
            const blocked = finiteNumber(item.blocked_count);
            return (
              <div className="ranking-item" key={item.agent_id}>
                <div className="ranking-line"><span title={item.agent_id}>{name}</span><span className="mono-text">{blocked.toLocaleString("zh-CN")}</span></div>
                <div className="ranking-track"><span style={{ width: `${Math.min(100, (blocked / maxBlocked) * 100)}%` }} /></div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function RiskRanking({ risks }: { risks: RiskTopEntry[] }) {
  return (
    <div className="battle-tab-panel">
      {risks.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无高危规则命中" /> : (
        <div className="risk-list">
          {risks.slice(0, 5).map((item) => {
            const meta = getRuleMeta(item.rule_id);
            return <div className="risk-item" key={item.rule_id}><span className="risk-id mono-text">{meta.id}</span><span className="risk-title" title={meta.title}>{meta.title}</span><span className="risk-count mono-text">{finiteNumber(item.count).toLocaleString("zh-CN")}</span></div>;
          })}
        </div>
      )}
    </div>
  );
}

export function buildBattleTabItems(ranking: AgentRankingItem[], risks: RiskTopEntry[]): TabsProps["items"] {
  return [
    { key: "agents", label: "Agent 排行", children: <AgentRanking ranking={ranking} /> },
    { key: "rules", label: "高危规则 Top5", children: <RiskRanking risks={risks} /> },
  ];
}

export function BattlePanel({ summary }: { summary: DashboardSummary | null }) {
  const report = summary?.battle_report;
  const ranking = summary?.agent_ranking || [];
  const risks = summary?.risk_top || [];

  return (
    <Card
      className="overview-panel battle-panel"
      bordered
      title={<span className="battle-title"><SafetyCertificateFilled aria-hidden="true" /><span>拦截战报</span></span>}
    >
      <div className="battle-hero">
        <div className="battle-metric battle-metric-primary">
          <div className="battle-number">{finiteNumber(report?.blocked_count).toLocaleString("zh-CN")}</div>
          <div className="battle-label">已拦截次数</div>
        </div>
        <div className="battle-metric battle-metric-secondary">
          <div className="battle-number">{formatRows(report?.est_rows_saved)}</div>
          <div className="battle-label">避免风险扫描（行）</div>
        </div>
      </div>
      <Tabs className="battle-tabs" size="small" items={buildBattleTabItems(ranking, risks)} />
    </Card>
  );
}
