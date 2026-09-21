import type { EChartsOption } from "echarts";
import { describe, expect, it } from "vitest";

import { buildDecisionOption } from "./DecisionDonut";

const data = [
  { decision: "allow", count: 10 },
  { decision: "deny", count: 2 },
];

function firstSeries(option: EChartsOption) {
  return Array.isArray(option.series) ? option.series[0] : option.series;
}

describe("buildDecisionOption", () => {
  it("anchors the title and pie to the same wide-layout center", () => {
    const option = buildDecisionOption(data, "light", false);
    expect(option.title).toMatchObject({ left: "32%", top: "50%", itemGap: 4 });
    expect(firstSeries(option)).toMatchObject({ center: ["32%", "50%"] });
  });

  it("moves the pie and scrolling legend for narrow layouts", () => {
    const option = buildDecisionOption(data, "dark", true);
    expect(option.title).toMatchObject({ left: "50%", top: "42%" });
    expect(firstSeries(option)).toMatchObject({ center: ["50%", "42%"] });
    expect(option.legend).toMatchObject({ type: "scroll", orient: "horizontal", bottom: 0 });
  });

  it("keeps five decisions and rich count/percentage formatting", () => {
    const option = buildDecisionOption(data, "light", false);
    const legend = option.legend as { formatter: (name: string) => string; textStyle: { rich: object } };
    const series = firstSeries(option) as { data: unknown[] };
    expect(series.data).toHaveLength(5);
    expect(legend.formatter("放行")).toBe("{name|放行}  {count|10}  {pct|83.3%}");
    expect(legend.textStyle.rich).toMatchObject({ name: expect.any(Object), count: expect.any(Object), pct: expect.any(Object) });
  });
});
