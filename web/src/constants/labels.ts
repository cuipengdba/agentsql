import { palette } from "@/theme/tokens";
import { maskAlgorithmMeta, sensitiveTypeMeta } from "@/constants/sensitiveTypes";

export { sensitiveTypeMeta } from "@/constants/sensitiveTypes";

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

export const agentLevelMeta = {
  readonly: { label: "只读", color: "blue" },
  dml: { label: "读写(DML)", color: "cyan" },
  ddl: { label: "结构(DDL)", color: "orange" },
} as const;

export const agentStatusMeta = {
  active: { label: "启用", color: "success" },
  disabled: { label: "禁用", color: "default" },
} as const;

export const dbTypeMeta = {
  postgres: { label: "PostgreSQL", color: "blue" },
  mysql: { label: "MySQL", color: "gold" },
  all: { label: "通用", color: "default" },
} as const;

export const policyActionMeta = {
  allow: { label: "允许", color: "success" },
  deny: { label: "拒绝", color: "error" },
} as const;

export const objectTypeMeta = {
  database: { label: "库", color: "purple" },
  schema: { label: "模式", color: "geekblue" },
  table: { label: "表", color: "cyan" },
  column: { label: "列(列级白名单)", color: "orange" },
} as const;

export const riskLevelMeta = {
  1: { label: "1 · 低", color: palette.semantic.allow },
  2: { label: "2 · 关注", color: palette.light.brand },
  3: { label: "3 · 告警", color: palette.semantic.warn },
  4: { label: "4 · 高", color: palette.semantic.approve },
  5: { label: "5 · 严重", color: palette.semantic.deny },
} as const;

export function configLabel(meta: Readonly<Record<string, Readonly<{ label: string }>>>, value: string): string {
  return meta[value]?.label || value || "—";
}

export const approvalStatusMeta = {
  pending: { label: "待审批", color: "orange" },
  approved: { label: "已通过", color: "success" },
  rejected: { label: "已拒绝", color: "error" },
  expired: { label: "已过期", color: "default" },
} as const;

export const maskAlgoMeta = maskAlgorithmMeta;

export const discoveryCategoryMeta = sensitiveTypeMeta;

export const discoveryConfidenceMeta = {
  high: { label: "高", color: "success" },
  medium: { label: "中", color: "warning" },
  low: { label: "低", color: "default" },
} as const;

export const discoveryApplyStatusMeta = {
  created: { label: "已生成草稿", color: "success" },
  existing: { label: "已有草稿", color: "blue" },
  covered_by_global: { label: "已被全局规则覆盖", color: "cyan" },
  conflict: { label: "同名列草稿冲突", color: "orange" },
  ambiguous: { label: "多表同名列需确认", color: "gold" },
} as const;
