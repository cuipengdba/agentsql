import { describe, expect, it } from "vitest";

import { ruleMeta } from "./ruleMeta";

describe("rule metadata", () => {
  it("classifies EXPLAIN-dependent rules as dynamic", () => {
    expect(ruleMeta.R004.dynamic).toBe(true);
    expect(ruleMeta.R005.dynamic).toBe(true);
  });
});
