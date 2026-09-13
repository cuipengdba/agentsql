import type { PlaygroundAssessRequest } from "@/api/types";
import type { FlowDecision } from "@/components/stageflow/types";

export interface PlaygroundScenario {
  key: string;
  label: string;
  dbType: PlaygroundAssessRequest["db_type"];
  agentLevel: NonNullable<PlaygroundAssessRequest["agent_level"]>;
  sql: string;
  expect: FlowDecision;
}

export const playgroundScenarios: readonly PlaygroundScenario[] = [
  // 预期 allow，无规则命中。
  {
    key: "point-query",
    label: "正常点查",
    dbType: "postgres",
    agentLevel: "readonly",
    sql: "SELECT id, name FROM public.customers WHERE id = 42 LIMIT 10",
    expect: "allow",
  },
  // 预期 deny，命中 R002；MySQL R202 可能同时给出 approve 判词，但 deny 优先。
  {
    key: "update-without-where",
    label: "无 WHERE 全表更新",
    dbType: "mysql",
    agentLevel: "dml",
    sql: "UPDATE orders SET status = 'closed'",
    expect: "deny",
  },
  // 预期 deny，至少命中 R001、R006。
  {
    key: "stacked-injection",
    label: "堆叠注入夹带删除",
    dbType: "mysql",
    agentLevel: "dml",
    sql: "SELECT * FROM users WHERE id = 1; DELETE FROM users",
    expect: "deny",
  },
  // 预期 deny，命中 R007。
  {
    key: "dangerous-sleep",
    label: "危险函数慢查询",
    dbType: "postgres",
    agentLevel: "readonly",
    sql: "SELECT * FROM public.orders WHERE id = 1 OR pg_sleep(10) IS NULL",
    expect: "deny",
  },
  // 预期 approve，命中 R202 且不命中 R002。
  {
    key: "unlimited-update",
    label: "无 LIMIT 批量写转人工",
    dbType: "mysql",
    agentLevel: "dml",
    sql: "UPDATE accounts SET balance = balance + 1 WHERE level = 'vip'",
    expect: "approve",
  },
  // 预期 error，由 PARSE 判词 fail-closed。
  {
    key: "parse-error",
    label: "残缺 SQL 解析失败",
    dbType: "postgres",
    agentLevel: "readonly",
    sql: "SELECT FROM WHERE (((",
    expect: "error",
  },
];
