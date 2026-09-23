import { describe, expect, it } from "vitest";

import { discoveryApplyItemFromFinding, type DiscoveryFinding } from "./discovery";

function finding(overrides: Partial<DiscoveryFinding>): DiscoveryFinding {
  return {
    schema: "public",
    table: "orders",
    column: "amount",
    data_type: "numeric",
    category: "number",
    signals: [],
    confidence: "medium",
    sampled: true,
    matched_samples: 3,
    eligible_samples: 3,
    recommended_rule: { sensitive_type: "number", algo: "block" },
    applicable: true,
    existing_rule: false,
    ...overrides,
  };
}

describe("discoveryApplyItemFromFinding", () => {
  it("keeps number block recommendations parameter-free", () => {
    expect(discoveryApplyItemFromFinding(finding({}))).toEqual({
      schema: "public",
      table: "orders",
      column: "amount",
      category: "number",
      sensitive_type: "number",
      algo: "block",
    });
  });

  it("passes explicit number and date range parameters through", () => {
    expect(discoveryApplyItemFromFinding(finding({
      recommended_rule: {
        sensitive_type: "number",
        algo: "range",
        range: { bucket_width: 50, bucket_offset: -5 },
      },
    }))).toMatchObject({ range: { bucket_width: 50, bucket_offset: -5 } });

    expect(discoveryApplyItemFromFinding(finding({
      column: "created_at",
      category: "date",
      recommended_rule: {
        sensitive_type: "date",
        algo: "range",
        range: { granularity: "month" },
      },
    }))).toMatchObject({
      category: "date",
      algo: "range",
      range: { granularity: "month" },
    });
  });

  it("keeps high-precision generic as block", () => {
    expect(discoveryApplyItemFromFinding(finding({
      column: "full_name",
      category: "generic",
      recommended_rule: { sensitive_type: "generic", algo: "block" },
    }))).toMatchObject({ category: "generic", sensitive_type: "generic", algo: "block" });
  });

  it("refuses discovery-only generic findings", () => {
    expect(() => discoveryApplyItemFromFinding(finding({
      column: "name",
      category: "generic",
      recommended_rule: null,
      applicable: false,
    }))).toThrow("Discovery finding is not applicable");
  });
});
