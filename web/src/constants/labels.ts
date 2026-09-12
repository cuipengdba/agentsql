import { palette } from "@/theme/tokens";

export type Decision = "allow" | "deny" | "warn" | "approve";

export interface DecisionMeta {
  label: string;
  tokenName: "allow" | "deny" | "warn" | "approve";
  color: string;
  tagColor: "success" | "error" | "warning" | "orange";
}

export const decisionMeta: Record<Decision, DecisionMeta> = {
  allow: { label: "放行", tokenName: "allow", color: palette.semantic.allow, tagColor: "success" },
  deny: { label: "拦截", tokenName: "deny", color: palette.semantic.deny, tagColor: "error" },
  warn: { label: "告警", tokenName: "warn", color: palette.semantic.warn, tagColor: "warning" },
  approve: { label: "待审批", tokenName: "approve", color: palette.semantic.approve, tagColor: "orange" },
};

export const stmtTypeLabels: Record<string, string> = {
  SELECT: "查询",
  INSERT: "插入",
  UPDATE: "更新",
  DELETE: "删除",
  MERGE: "合并",
  DDL: "结构变更",
  ADMIN: "管理操作",
  TRANSACTION: "事务控制",
  UNKNOWN: "未知语句",
};

export function statementLabel(stmtType: string | null | undefined): string {
  const normalized = (stmtType || "").trim().toUpperCase();
  return stmtTypeLabels[normalized] || (normalized || "未知语句");
}

export function getDecisionMeta(decision: string | null | undefined): DecisionMeta {
  const normalized = (decision || "").trim().toLowerCase();
  return decisionMeta[normalized as Decision] || {
    label: normalized || "未知",
    tokenName: "deny",
    color: palette.semantic.deny,
    tagColor: "error",
  };
}
