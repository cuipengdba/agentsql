import { describe, expect, it } from "vitest";

import { menuRoutesForFeatures } from "./menu";

describe("B5 navigation gate", () => {
  it("hides the sessions/transactions entry while b5_sessions is off", () => {
    expect(menuRoutesForFeatures(false).some((route) => route.key === "b5-operations")).toBe(false);
  });

  it("shows the entry only after the backend reports the feature enabled", () => {
    expect(menuRoutesForFeatures(true).find((route) => route.key === "b5-operations")?.path).toBe("/b5-operations");
  });
});
