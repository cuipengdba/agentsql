import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import type { DiscoveryFinding, DiscoveryResponse } from "@/api/discovery";
import { discoverySensitiveTypes, sensitiveTypeMeta } from "@/constants/sensitiveTypes";

import { DiscoveryResults } from "./DiscoveryResults";

function finding(overrides: Partial<DiscoveryFinding>): DiscoveryFinding {
  return {
    schema: "public",
    table: "orders",
    column: "phone",
    data_type: "text",
    category: "phone",
    signals: [{ name: "column_name_strong_phone", count: 1 }],
    confidence: "medium",
    sampled: false,
    matched_samples: 0,
    eligible_samples: 0,
    recommended_rule: { sensitive_type: "phone", algo: "mask" },
    applicable: true,
    existing_rule: false,
    ...overrides,
  };
}

describe("DiscoveryResults enhanced discovery", () => {
  it("exports all nine scan categories with the non-purple enhanced palette", () => {
    expect(discoverySensitiveTypes).toEqual([
      "phone", "email", "idcard", "bankcard", "ip", "birthdate", "number", "date", "generic",
    ]);
    expect(sensitiveTypeMeta.number.color).toBe("#D48806");
    expect(sensitiveTypeMeta.date.color).toBe("#13A8A8");
    expect(sensitiveTypeMeta.generic.color).toBe("#5B7A9D");
  });

  it("renders suggested algorithms and range summaries in table and cards", () => {
    const findings = [
      finding({ column: "phone" }),
      finding({ column: "email", category: "email", recommended_rule: { sensitive_type: "email", algo: "mask" } }),
      finding({ column: "id_card", category: "idcard", recommended_rule: { sensitive_type: "idcard", algo: "mask" } }),
      finding({ column: "bank_card", category: "bankcard", recommended_rule: { sensitive_type: "bankcard", algo: "mask" } }),
      finding({ column: "client_ip", category: "ip", recommended_rule: { sensitive_type: "ip", algo: "mask" } }),
      finding({ column: "birth_date", category: "birthdate", recommended_rule: { sensitive_type: "birthdate", algo: "mask" } }),
      finding({ column: "price", category: "number", recommended_rule: { sensitive_type: "number", algo: "block" } }),
      finding({
        column: "stock",
        category: "number",
        recommended_rule: { sensitive_type: "number", algo: "range", range: { bucket_width: 50 } },
      }),
      finding({
        column: "created_at",
        category: "date",
        recommended_rule: { sensitive_type: "date", algo: "range", range: { granularity: "month" } },
      }),
      finding({
        column: "full_name",
        category: "generic",
        recommended_rule: { sensitive_type: "generic", algo: "block" },
      }),
      finding({
        column: "name",
        category: "generic",
        confidence: "low",
        recommended_rule: null,
        applicable: false,
        reason: "可能含敏感自由文本，请人工判断后选择 block/hash",
      }),
    ];
    const result: DiscoveryResponse = {
      scope: { datasource_id: "ds-1", tables: [{ schema: "public", table: "orders" }], sampling: true, sample_rows: 3, categories: [...discoverySensitiveTypes] },
      stats: { tables_requested: 1, tables_scanned: 1, columns_seen: findings.length, candidate_columns: findings.length, sampled_columns: 9, sampled_values_count: 27, findings_count: findings.length },
      limits: { max_tables: 20, max_metadata_columns: 500, max_candidate_columns: 50, max_columns_per_sample_query: 10, default_sample_rows: 10, max_sample_rows: 20, default_sample_values: 500, max_sample_values: 1000 },
      findings,
    };
    const html = renderToStaticMarkup(<DiscoveryResults result={result} selectedKeys={[]} statuses={new Map()} onSelectionChange={() => undefined} />);
    expect(html).toContain("建议算法");
    expect(html).toContain("打码（mask）");
    expect(html).toContain("阻断（block）");
    expect(html).toContain("width=50");
    expect(html).toContain("granularity=month");
    expect(html).toContain("仅发现");
    expect(html).toContain("手机号");
    expect(html).toContain("邮箱");
    expect(html).toContain("身份证");
    expect(html).toContain("银行卡");
    expect(html).toContain("IP 地址");
    expect(html).toContain("出生日期");
    expect(html).not.toContain("哈希指纹（hash）");
  });
});
