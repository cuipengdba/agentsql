import type { AuditView } from "@/api/types";
import { adaptAssessment, type AssessmentHit } from "@/components/stageflow/adaptAssessment";
import type { FlowDecision, StageFlowData } from "@/components/stageflow/types";

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function parseAuditRuleHits(encoded: string | null | undefined): AssessmentHit[] {
  if (!encoded?.trim()) return [];
  try {
    const parsed: unknown = JSON.parse(encoded);
    if (!Array.isArray(parsed)) return [];
    return parsed.flatMap((item) => {
      if (!isObject(item)) return [];
      if (
        typeof item.RuleID !== "string" ||
        typeof item.Risk !== "number" ||
        !Number.isFinite(item.Risk) ||
        typeof item.Decision !== "string" ||
        typeof item.Message !== "string" ||
        typeof item.Suggestion !== "string"
      ) return [];
      return [{
        RuleID: item.RuleID,
        Risk: item.Risk,
        Decision: item.Decision,
        Message: item.Message,
        Suggestion: item.Suggestion,
      }];
    });
  } catch {
    return [];
  }
}

export function normalizeAuditDecision(value: string | null | undefined): FlowDecision {
  const normalized = (value || "").trim().toLowerCase();
  switch (normalized) {
    case "allow":
    case "warn":
    case "approve":
    case "deny":
    case "error":
      return normalized;
    default:
      return "error";
  }
}

function optionalNonNegative(value: number | null | undefined): number | undefined {
  return value !== null && value !== undefined && Number.isFinite(value) && value >= 0 ? value : undefined;
}

export function auditToFlow(record: AuditView): StageFlowData {
  const decision = normalizeAuditDecision(record.decision);
  const hits = parseAuditRuleHits(record.rule_hits);
  const flow = adaptAssessment({
    Decision: decision,
    Hits: hits,
    EstScanRows: optionalNonNegative(record.est_rows),
  });
  return {
    ...flow,
    totalLatencyMs: optionalNonNegative(record.latency_ms),
    rowsReturned: optionalNonNegative(record.rows_returned),
    errorMsg: record.error_msg?.trim() || flow.errorMsg,
  };
}
