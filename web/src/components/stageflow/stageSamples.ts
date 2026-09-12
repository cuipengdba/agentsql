import { stageNodes } from "./stageNodes";
import type { FlowDecision, StageFlowData, StageKey, StageStatus } from "./types";

export type StageSampleKey = "allow" | "deny" | "approve" | "warn" | "error";

export interface StageSample {
  key: StageSampleKey;
  label: string;
  sql: string;
  data: StageFlowData;
}

function steps(
  statuses: Record<StageKey, StageStatus>,
  latencies: Partial<Record<StageKey, number>>,
  notes: Partial<Record<StageKey, string>> = {},
) {
  return stageNodes.map((node) => ({
    key: node.key,
    label: node.label,
    status: statuses[node.key],
    latencyMs: latencies[node.key] || 0,
    note: notes[node.key] || node.subtitle,
  }));
}

function statusMap(values: readonly StageStatus[]): Record<StageKey, StageStatus> {
  return stageNodes.reduce<Record<StageKey, StageStatus>>((result, node, index) => {
    result[node.key] = values[index] || "pending";
    return result;
  }, { auth: "pending", parse: "pending", guard: "pending", decide: "pending", execute: "pending", audit: "pending" });
}

function sampleData(
  decision: FlowDecision,
  statuses: readonly StageStatus[],
  latencies: Partial<Record<StageKey, number>>,
  rest: Omit<StageFlowData, "decision" | "steps"> = {},
  notes: Partial<Record<StageKey, string>> = {},
): StageFlowData {
  return { decision, steps: steps(statusMap(statuses), latencies, notes), ...rest };
}

export const stageSamples: Record<StageSampleKey, StageSample> = {
  allow: {
    key: "allow",
    label: "放行",
    sql: "SELECT id, name FROM public.customers WHERE active = TRUE LIMIT 128",
    data: sampleData(
      "allow",
      ["pass", "pass", "pass", "pass", "pass", "pass"],
      { auth: 3.2, parse: 1.4, guard: 8.6, execute: 16.8, audit: 2.1 },
      { rowsReturned: 128, totalLatencyMs: 32.1 },
      { guard: "权限与规则检查通过", decide: "允许受控执行", execute: "返回 128 行", audit: "结果脱敏并已留痕" },
    ),
  },
  deny: {
    key: "deny",
    label: "拦截",
    sql: "UPDATE public.orders SET status = 'closed'",
    data: sampleData(
      "deny",
      ["pass", "pass", "block", "block", "skip", "pass"],
      { auth: 2.8, parse: 1.7, guard: 4.3, audit: 1.9 },
      {
        blockAt: "guard",
        totalLatencyMs: 10.7,
        verdict: {
          ruleId: "R002",
          title: "无条件批量写防护",
          message: "检测到 UPDATE 未提供 WHERE 条件，可能修改整张订单表",
          suggestion: "请补充可验证的 WHERE 条件，并优先按主键分批更新",
          risk: 5,
        },
      },
      { guard: "R002 命中并拦截", decide: "拦截", execute: "零触库，用户 SQL 未执行", audit: "拦截事件已留痕" },
    ),
  },
  approve: {
    key: "approve",
    label: "审批",
    sql: "SELECT * FROM public.audit_logs",
    data: sampleData(
      "approve",
      ["pass", "pass", "warn", "warn", "locked", "pass"],
      { auth: 3.5, parse: 1.2, guard: 42.6, audit: 2.4 },
      {
        blockAt: "decide",
        totalLatencyMs: 49.7,
        verdict: {
          ruleId: "R004",
          title: "大范围扫描审批",
          message: "查询计划预计扫描 860,000 行，超过人工审批阈值",
          suggestion: "请增加高选择性过滤条件或使用索引字段缩小扫描范围",
          risk: 4,
        },
      },
      { guard: "预计扫描 860,000 行", decide: "转人工审批", execute: "待审批解锁", audit: "审批待办已同步留痕" },
    ),
  },
  warn: {
    key: "warn",
    label: "告警",
    sql: "SELECT id, created_at FROM public.orders WHERE created_at >= CURRENT_DATE",
    data: sampleData(
      "warn",
      ["pass", "pass", "warn", "warn", "pass", "pass"],
      { auth: 2.9, parse: 1.1, guard: 9.8, execute: 24.5, audit: 2.2 },
      {
        rowsReturned: 1_000,
        totalLatencyMs: 40.5,
        verdict: {
          ruleId: "R005",
          title: "大结果集限制提醒",
          message: "查询未设置 LIMIT，结果集规模可能超过默认返回上限",
          suggestion: "请增加 LIMIT 或缩小时间范围，避免返回不必要的数据",
          risk: 3,
        },
      },
      { guard: "结果集规模告警", decide: "带告警放行", execute: "已按上限截断", audit: "告警与结果已留痕" },
    ),
  },
  error: {
    key: "error",
    label: "错误",
    sql: "SELEC * FROM public.orders WHERE",
    data: sampleData(
      "error",
      ["pass", "block", "skip", "skip", "skip", "pass"],
      { auth: 3.1, parse: 0.8, audit: 1.6 },
      {
        blockAt: "parse",
        totalLatencyMs: 5.5,
        errorMsg: "SQL 语法不完整，解析器无法生成可信 AST",
        verdict: {
          title: "SQL 解析失败",
          message: "SQL 语法不完整，解析器无法生成可信 AST",
          suggestion: "请检查 SELECT 关键字、字段和 WHERE 条件后重新提交",
          risk: 5,
        },
      },
      { parse: "解析失败", guard: "未进入规则安检", decide: "未生成决策", execute: "用户 SQL 未执行", audit: "错误事件已留痕" },
    ),
  },
};

export const stageSampleList: readonly StageSample[] = [
  stageSamples.allow,
  stageSamples.deny,
  stageSamples.approve,
  stageSamples.warn,
  stageSamples.error,
];
