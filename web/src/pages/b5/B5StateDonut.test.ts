import { describe, expect, it } from "vitest";

import { buildB5StateOption } from "./B5StateDonut";

describe("B5StateDonut", () => {
  it("normalizes invalid counts and keeps the cyan/teal operations palette", () => {
    const option = buildB5StateOption([{ state: "ACTIVE", count: 3 }, { state: "UNKNOWN", count: -1 }], "dark");
    const series = (option.series as unknown as Array<{ data: Array<{ name: string; value: number; itemStyle?: { color: string } }> }>)[0];
    expect(series.data).toEqual(expect.arrayContaining([{ name: "ACTIVE", value: 3, itemStyle: { color: "#06b6d4" } }]));
    expect(series.data.find((item) => item.name === "UNKNOWN")?.value).toBe(0);
  });
});
