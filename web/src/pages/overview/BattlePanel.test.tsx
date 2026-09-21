import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import type { DashboardSummary } from "@/api/types";

import { BattlePanel, buildBattleTabItems } from "./BattlePanel";

const summary: DashboardSummary = {
  kpi: {
    total_requests: 0,
    blocked: 0,
    pending_approvals: 0,
    active_agents: 0,
    datasources_total: 0,
    total_requests_change_pct: null,
    blocked_change_pct: null,
  },
  trend_14d: [],
  decision_distribution: [],
  battle_report: { blocked_count: 1234, est_rows_saved: 25_000 },
  agent_ranking: [{ agent_id: "agent-1", name: "分析助手", blocked_count: 8 }],
  risk_top: [{ rule_id: "R001", count: 6 }],
};

describe("BattlePanel", () => {
  it("renders hero metrics, labels, tabs, and the default Agent ranking", () => {
    const html = renderToStaticMarkup(<BattlePanel summary={summary} />);
    expect(html).toContain("1,234");
    expect(html).toContain("2.5 万");
    expect(html).toContain("已拦截次数");
    expect(html).toContain("避免风险扫描（行）");
    expect(html).toContain("Agent 排行");
    expect(html).toContain("高危规则 Top5");
    expect(html).toContain("分析助手");
  });

  it("renders the empty Agent state when summary is absent", () => {
    const html = renderToStaticMarkup(<BattlePanel summary={null} />);
    expect(html).toContain("暂无被拦截 Agent");
    expect(html).toContain("已拦截次数");
  });

  it("builds rule content and its empty state independently of tab interaction", () => {
    const filledItems = buildBattleTabItems([], summary.risk_top) || [];
    const emptyItems = buildBattleTabItems([], []) || [];
    expect(renderToStaticMarkup(<>{filledItems[1]?.children}</>)).toContain("R001");
    expect(renderToStaticMarkup(<>{emptyItems[1]?.children}</>)).toContain("暂无高危规则命中");
  });
});
