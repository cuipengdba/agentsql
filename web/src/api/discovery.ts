import type { ApiResponse } from "./types";
import { request } from "./client";
import type { MaskAlgorithm, SensitiveType } from "@/constants/sensitiveTypes";

// Reuse the complete mask-domain unions so an enhanced backend response does
// not become an impossible value to the older discovery UI.
export type DiscoveryCategory = SensitiveType;
export type DiscoveryAlgorithm = MaskAlgorithm;
export type DiscoveryConfidence = "high" | "medium" | "low";
export type DiscoveryApplyStatus = "created" | "existing" | "covered_by_global" | "conflict" | "ambiguous";

export interface DiscoveryTableRef {
  schema: string;
  table: string;
}

export interface DiscoveryRequest {
  tables: DiscoveryTableRef[];
  sampling?: boolean;
  sample_rows?: number;
  categories?: DiscoveryCategory[];
}

export interface DiscoverySignal {
  name: string;
  count: number;
}

export interface DiscoveryRecommendedRule {
  sensitive_type: SensitiveType;
  algo: DiscoveryAlgorithm;
  range?: {
    bucket_width?: number;
    bucket_offset?: number;
    granularity?: string;
  };
}

export type DiscoveryRange = NonNullable<DiscoveryRecommendedRule["range"]>;

export interface DiscoveryFinding {
  schema: string;
  table: string;
  column: string;
  data_type: string;
  category: DiscoveryCategory;
  signals: DiscoverySignal[];
  confidence: DiscoveryConfidence;
  sampled: boolean;
  matched_samples: number;
  eligible_samples: number;
  recommended_rule: DiscoveryRecommendedRule | null;
  applicable: boolean;
  existing_rule: boolean;
  reason?: string;
}

export interface DiscoveryScope {
  datasource_id: string;
  tables: DiscoveryTableRef[];
  sampling: boolean;
  sample_rows: number;
  categories: DiscoveryCategory[];
}

export interface DiscoveryStats {
  tables_requested: number;
  tables_scanned: number;
  columns_seen: number;
  candidate_columns: number;
  sampled_columns: number;
  sampled_values_count: number;
  findings_count: number;
}

export interface DiscoveryLimits {
  max_tables: number;
  max_metadata_columns: number;
  max_candidate_columns: number;
  max_columns_per_sample_query: number;
  default_sample_rows: number;
  max_sample_rows: number;
  default_sample_values: number;
  max_sample_values: number;
}

export interface DiscoveryResponse {
  scope: DiscoveryScope;
  stats: DiscoveryStats;
  limits: DiscoveryLimits;
  findings: DiscoveryFinding[];
}

export interface DiscoveryApplyItem {
  schema: string;
  table: string;
  column: string;
  category: DiscoveryCategory;
  sensitive_type: SensitiveType;
  algo: DiscoveryAlgorithm;
  range?: DiscoveryRange;
}

export interface DiscoveryApplyRequest {
  items: DiscoveryApplyItem[];
}

export interface DiscoveryApplyItemView {
  schema?: string;
  table?: string;
  column: string;
  category?: DiscoveryCategory;
  sensitive_type: SensitiveType;
  algo: DiscoveryAlgorithm;
  range?: DiscoveryRange;
  rule_id?: string;
}

export interface DiscoveryApplyCounts {
  requested: number;
  created: number;
  existing: number;
  covered_by_global: number;
  conflicts: number;
  ambiguous: number;
}

export interface DiscoveryApplyResponse {
  created: DiscoveryApplyItemView[];
  existing: DiscoveryApplyItemView[];
  covered_by_global: DiscoveryApplyItemView[];
  conflicts: DiscoveryApplyItemView[];
  ambiguous: DiscoveryApplyItemView[];
  counts: DiscoveryApplyCounts;
}

export function discoveryApplyItemFromFinding(finding: DiscoveryFinding): DiscoveryApplyItem {
  if (!finding.applicable || !finding.recommended_rule) {
    throw new Error("Discovery finding is not applicable");
  }
  return {
    schema: finding.schema,
    table: finding.table,
    column: finding.column,
    category: finding.category,
    sensitive_type: finding.recommended_rule.sensitive_type,
    algo: finding.recommended_rule.algo,
    ...(finding.recommended_rule.range ? { range: { ...finding.recommended_rule.range } } : {}),
  };
}

export function discoverSensitiveColumns(
  datasourceID: string,
  input: DiscoveryRequest,
  signal?: AbortSignal,
): Promise<DiscoveryResponse> {
  return request<DiscoveryResponse>({
    method: "POST",
    url: `/datasources/${encodeURIComponent(datasourceID)}/discover`,
    data: input,
    signal,
    suppressErrorMessage: true,
  });
}

export function applyDiscoveryDrafts(
  datasourceID: string,
  input: DiscoveryApplyRequest,
  signal?: AbortSignal,
): Promise<DiscoveryApplyResponse> {
  return request<DiscoveryApplyResponse>({
    method: "POST",
    url: `/datasources/${encodeURIComponent(datasourceID)}/discover/apply`,
    data: input,
    signal,
    suppressErrorMessage: true,
  });
}

export function discoveryApplyResponseFromError(error: unknown): DiscoveryApplyResponse | undefined {
  if (typeof error !== "object" || error === null) return undefined;
  const response = (error as { response?: { data?: unknown } }).response;
  if (typeof response?.data !== "object" || response.data === null) return undefined;
  const data = (response.data as Partial<ApiResponse<unknown>>).data;
  if (typeof data !== "object" || data === null || !("counts" in data)) return undefined;
  return data as DiscoveryApplyResponse;
}
