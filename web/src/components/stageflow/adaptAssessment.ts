import { getRuleMeta } from "@/constants/ruleMeta";

import { stageNodes } from "./stageNodes";
import type { FlowDecision, StageFlowData, StageKey, StageStatus, StageVerdictData } from "./types";

type BackendStageKey = "auth" | "load" | "parse" | "guard_static" | "guard_dynamic" | "execute" | "redact" | "audit";

export interface AssessmentHit {
  RuleID: string;
  Risk: number;
  Decision: string;
  Message: string;
  Suggestion: string;
}

export interface AssessmentLike {
  Decision: string;
  Hits?: readonly AssessmentHit[] | null;
  StageLatency?: Partial<Record<BackendStageKey, number | null>> | null;
  EstScanRows?: number | null;
  Reason?: string | null;
  Suggestion?: string | null;
  ErrorMessage?: string | null;
}

const errorStageNodes = {
  parse: "parse",
  explain: "guard",
  metadata: "guard",
  connect: "execute",
  ping: "execute",
  acquire: "execute",
  begin_tx: "execute",
  query: "execute",
  read_rows: "execute",
  execute: "execute",
  commit: "execute",
  rollback: "execute",
} as const satisfies Readonly<Record<string, StageKey>>;

export type DatabaseErrorStage = keyof typeof errorStageNodes;

export function normalizeErrorStage(value: string | null | undefined): DatabaseErrorStage | undefined {
  const normalized = (value || "").trim().toLowerCase();
  return Object.prototype.hasOwnProperty.call(errorStageNodes, normalized)
    ? normalized as DatabaseErrorStage
    : undefined;
}

export function errorStageToStageKey(value: string | null | undefined): StageKey {
  const stage = normalizeErrorStage(value);
  return stage ? errorStageNodes[stage] : "execute";
}

function safeLatency(value: number | null | undefined): number {
  return value !== null && value !== undefined && Number.isFinite(value) && value >= 0 ? value : 0;
}

function normalizeDecision(value: string): FlowDecision {
  switch (value.trim().toLowerCase()) {
    case "allow":
    case "warn":
    case "approve":
    case "deny":
    case "error":
      return value.trim().toLowerCase() as FlowDecision;
    default:
      return "error";
  }
}

function errorStatuses(errorStage: string | null | undefined): Record<StageKey, StageStatus> {
  const failedNode = errorStageToStageKey(errorStage);
  const failedIndex = stageNodes.findIndex((node) => node.key === failedNode);
  return Object.fromEntries(stageNodes.map((node, index) => [
    node.key,
    index < failedIndex ? "pass" : index === failedIndex ? "block" : "skip",
  ])) as Record<StageKey, StageStatus>;
}

function statusesFor(decision: FlowDecision, errorStage?: string | null): Record<StageKey, StageStatus> {
  switch (decision) {
    case "allow":
      return { auth: "pass", parse: "pass", guard: "pass", decide: "pass", execute: "pass", audit: "pass" };
    case "warn":
      return { auth: "pass", parse: "pass", guard: "warn", decide: "warn", execute: "pass", audit: "pass" };
    case "approve":
      return { auth: "pass", parse: "pass", guard: "warn", decide: "warn", execute: "locked", audit: "pass" };
    case "deny":
      return { auth: "pass", parse: "pass", guard: "block", decide: "block", execute: "skip", audit: "pass" };
    case "error":
      return errorStatuses(errorStage);
  }
}

function blockStage(decision: FlowDecision, errorStage?: string | null): StageKey | undefined {
  if (decision === "deny") return "guard";
  if (decision === "approve") return "decide";
  if (decision === "error") return errorStageToStageKey(errorStage);
  return undefined;
}

function verdictFromHits(hits: readonly AssessmentHit[]): StageVerdictData | undefined {
  const hit = hits.find((candidate) => candidate.Decision.trim().toLowerCase() !== "allow");
  if (!hit) return undefined;
  const meta = getRuleMeta(hit.RuleID);
  return {
    ruleId: hit.RuleID || undefined,
    title: meta.title,
    message: hit.Message || "规则命中，当前请求需要进一步处理",
    suggestion: hit.Suggestion || undefined,
    risk: Number.isFinite(hit.Risk) ? hit.Risk : meta.risk,
  };
}

// Example: auth=2 and load=3 become the single auth node latency 5.
// Missing backend keys contribute zero and never produce NaN.
function errorStageNote(errorStage: string | null | undefined): string {
  switch (normalizeErrorStage(errorStage)) {
    case "parse": return "SQL 解析失败";
    case "explain": return "EXPLAIN 失败";
    case "metadata": return "动态元数据读取失败";
    case "connect": return "数据源连接失败";
    case "ping": return "数据源连通性检查失败";
    case "acquire": return "数据库连接获取失败";
    case "begin_tx": return "事务启动失败";
    case "query": return "查询执行失败";
    case "read_rows": return "结果读取失败";
    case "commit": return "事务提交失败";
    case "rollback": return "事务回滚失败";
    case "execute":
    case undefined:
      return "数据库执行失败";
  }
}

export function adaptAssessment(assessment: AssessmentLike, errorStage?: string | null): StageFlowData {
  const decision = normalizeDecision(assessment.Decision || "error");
  const latency = assessment.StageLatency || {};
  const statuses = statusesFor(decision, errorStage);
  const failedNode = decision === "error" ? errorStageToStageKey(errorStage) : undefined;
  const scanRows = safeLatency(assessment.EstScanRows);
  const steps = stageNodes.map((node) => {
    const latencyMs = node.latencyKeys.reduce((sum, key) => sum + safeLatency(latency[key as BackendStageKey]), 0);
    let note = node.subtitle;
    if (node.key === "guard" && scanRows > 0) note = `预估扫描 ${Math.round(scanRows).toLocaleString("zh-CN")} 行`;
    if (node.key === "decide" && decision === "approve") note = "转人工审批";
    if (node.key === "execute" && decision === "deny") note = "用户 SQL 未执行";
    if (node.key === failedNode) note = errorStageNote(errorStage);
    return { key: node.key, label: node.label, status: statuses[node.key], latencyMs, note };
  });
  const totalLatencyMs = Object.values(latency).reduce<number>((sum, value) => sum + safeLatency(value), 0);
  const verdict = verdictFromHits(assessment.Hits || []);
  const errorMessage = assessment.ErrorMessage?.trim() || assessment.Reason?.trim() || errorStageNote(errorStage);
  const errorVerdict = decision === "error" ? {
    title: normalizeErrorStage(errorStage) === "parse" ? "SQL 解析失败" : "数据库执行失败",
    message: errorMessage,
    suggestion: assessment.Suggestion?.trim() || undefined,
  } : undefined;
  return {
    decision,
    steps,
    blockAt: blockStage(decision, errorStage),
    verdict: errorVerdict || verdict,
    totalLatencyMs,
    errorMsg: decision === "error" ? errorMessage : undefined,
  };
}
