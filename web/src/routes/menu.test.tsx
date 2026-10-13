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

describe("governance navigation", () => {
  it("shows only the governance pages granted by the current principal", () => {
    const routes = menuRoutesForFeatures(false, ["user.manage", "role.manage"]);
    expect(routes.map((route) => route.key)).toContain("users");
    expect(routes.map((route) => route.key)).toContain("roles");
    expect(routes.map((route) => route.key)).not.toContain("tenants");
    expect(menuRoutesForFeatures(false).map((route) => route.key)).not.toContain("users");
  });
});
