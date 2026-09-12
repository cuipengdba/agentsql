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

function statusesFor(decision: FlowDecision): Record<StageKey, StageStatus> {
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
      return { auth: "pass", parse: "block", guard: "skip", decide: "skip", execute: "skip", audit: "pass" };
  }
}

function blockStage(decision: FlowDecision): StageKey | undefined {
  if (decision === "deny") return "guard";
  if (decision === "approve") return "decide";
  if (decision === "error") return "parse";
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
export function adaptAssessment(assessment: AssessmentLike): StageFlowData {
  const decision = normalizeDecision(assessment.Decision || "error");
  const latency = assessment.StageLatency || {};
  const statuses = statusesFor(decision);
  const scanRows = safeLatency(assessment.EstScanRows);
  const steps = stageNodes.map((node) => {
    const latencyMs = node.latencyKeys.reduce((sum, key) => sum + safeLatency(latency[key as BackendStageKey]), 0);
    let note = node.subtitle;
    if (node.key === "guard" && scanRows > 0) note = `预估扫描 ${Math.round(scanRows).toLocaleString("zh-CN")} 行`;
    if (node.key === "decide" && decision === "approve") note = "转人工审批";
    if (node.key === "execute" && decision === "deny") note = "用户 SQL 未执行";
    return { key: node.key, label: node.label, status: statuses[node.key], latencyMs, note };
  });
  const totalLatencyMs = Object.values(latency).reduce<number>((sum, value) => sum + safeLatency(value), 0);
  const verdict = verdictFromHits(assessment.Hits || []);
  return {
    decision,
    steps,
    blockAt: blockStage(decision),
    verdict: verdict || (decision === "error" ? { title: "处理失败", message: "请求未能完成安全评估" } : undefined),
    totalLatencyMs,
    errorMsg: decision === "error" ? verdict?.message || "请求未能完成安全评估" : undefined,
  };
}
