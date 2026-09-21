import * as echarts from "echarts";
import type { EChartsOption } from "echarts";
import { useEffect, useMemo, useRef, useState } from "react";

import type { DecisionCount } from "@/api/types";
import { decisionMeta, type Decision } from "@/constants/labels";
import { palette, type ThemeMode } from "@/theme/tokens";

interface DecisionDonutProps {
  data: DecisionCount[];
  mode: ThemeMode;
}

export const decisionOrder: Decision[] = ["allow", "warn", "approve", "deny", "error"];

function safeCount(value: number | undefined): number {
  return Number.isFinite(value) && value !== undefined ? Math.max(0, value) : 0;
}

export function buildDecisionOption(data: DecisionCount[], mode: ThemeMode, narrow: boolean): EChartsOption {
  const colors = palette[mode];
  const counts = new Map(data.map((item) => [item.decision.toLowerCase(), safeCount(item.count)]));
  const total = decisionOrder.reduce((sum, decision) => sum + (counts.get(decision) || 0), 0);
  const centerX = narrow ? "50%" : "32%";
  const centerY = narrow ? "42%" : "50%";

  return {
    animationDuration: 450,
    title: {
      text: total.toLocaleString("zh-CN"),
      subtext: "总次数",
      left: centerX,
      top: centerY,
      textAlign: "center",
      textVerticalAlign: "middle",
      padding: 0,
      itemGap: 4,
      textStyle: { color: colors.text, fontSize: 30, fontWeight: 700 },
      subtextStyle: { color: colors.textSecondary, fontSize: 13 },
    },
    tooltip: {
      trigger: "item",
      renderMode: "richText",
      backgroundColor: colors.elevated,
      borderColor: colors.border,
      textStyle: { color: colors.text },
      formatter: (item: unknown) => {
        const candidate = item as { name?: string; value?: number; percent?: number };
        return `${candidate.name || "未知"}\n${safeCount(candidate.value).toLocaleString("zh-CN")}（${Number.isFinite(candidate.percent) ? candidate.percent : 0}%）`;
      },
    },
    legend: {
      type: narrow ? "scroll" : "plain",
      orient: narrow ? "horizontal" : "vertical",
      ...(narrow ? { left: "center", bottom: 0 } : { right: 0, top: "middle" }),
      itemGap: 10,
      textStyle: {
        color: colors.text,
        fontSize: 13,
        rich: {
          name: { color: colors.textSecondary, fontSize: 13 },
          count: { color: colors.text, fontSize: 13, fontWeight: 700 },
          pct: { color: colors.textSecondary, fontSize: 12 },
        },
      },
      formatter: (name: string) => {
        const decision = decisionOrder.find((item) => decisionMeta[item].label === name);
        if (!decision) return name;
        const count = counts.get(decision) || 0;
        const countText = count.toLocaleString("zh-CN");
        if (narrow) return `{name|${name}}  {count|${countText}}`;
        const percent = total > 0 ? ((count / total) * 100).toFixed(1) : "0.0";
        return `{name|${name}}  {count|${countText}}  {pct|${percent}%}`;
      },
    },
    series: [
      {
        name: "决策分布",
        type: "pie",
        radius: narrow ? ["50%", "68%"] : ["56%", "76%"],
        center: [centerX, centerY],
        avoidLabelOverlap: true,
        label: { show: false },
        labelLine: { show: false },
        itemStyle: { borderColor: colors.surface, borderWidth: 2 },
        data: decisionOrder.map((decision) => ({
          name: decisionMeta[decision].label,
          value: counts.get(decision) || 0,
          itemStyle: { color: decisionMeta[decision].color },
        })),
      },
    ],
  } satisfies EChartsOption;
}

export function DecisionDonut({ data, mode }: DecisionDonutProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);
  const [narrow, setNarrow] = useState(false);

  const counts = useMemo(
    () => new Map(data.map((item) => [item.decision.toLowerCase(), safeCount(item.count)])),
    [data],
  );
  const total = decisionOrder.reduce((sum, decision) => sum + (counts.get(decision) || 0), 0);
  const ariaLabel = `决策分布，共 ${total.toLocaleString("zh-CN")} 次：${decisionOrder
    .map((decision) => `${decisionMeta[decision].label} ${(counts.get(decision) || 0).toLocaleString("zh-CN")}`)
    .join("、")}`;

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const chart = echarts.init(container);
    chartRef.current = chart;
    const updateSize = () => {
      setNarrow(container.clientWidth < 480);
      chart.resize();
    };
    const resizeObserver = new ResizeObserver(updateSize);
    resizeObserver.observe(container);
    updateSize();
    window.addEventListener("resize", updateSize);
    return () => {
      resizeObserver.disconnect();
      window.removeEventListener("resize", updateSize);
      chart.dispose();
      chartRef.current = null;
    };
  }, []);

  useEffect(() => {
    chartRef.current?.setOption(buildDecisionOption(data, mode, narrow), true);
  }, [data, mode, narrow]);

  return <div ref={containerRef} className="chart-canvas decision-chart" role="img" aria-label={ariaLabel} />;
}
