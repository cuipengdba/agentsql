import * as echarts from "echarts";
import type { EChartsOption } from "echarts";
import { useEffect, useRef } from "react";

import type { DecisionCount } from "@/api/types";
import { decisionMeta, type Decision } from "@/constants/labels";
import { palette, type ThemeMode } from "@/theme/tokens";

interface DecisionDonutProps {
  data: DecisionCount[];
  mode: ThemeMode;
}

const decisionOrder: Decision[] = ["allow", "warn", "approve", "deny"];

function safeCount(value: number | undefined): number {
  return Number.isFinite(value) && value !== undefined ? Math.max(0, value) : 0;
}

export function DecisionDonut({ data, mode }: DecisionDonutProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);
  const colors = palette[mode];

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    const chart = echarts.init(container);
    chartRef.current = chart;
    const resizeObserver = new ResizeObserver(() => chart.resize());
    resizeObserver.observe(container);
    const handleResize = () => chart.resize();
    window.addEventListener("resize", handleResize);
    return () => {
      resizeObserver.disconnect();
      window.removeEventListener("resize", handleResize);
      chart.dispose();
      chartRef.current = null;
    };
  }, []);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) {
      return;
    }
    const counts = new Map(data.map((item) => [item.decision.toLowerCase(), safeCount(item.count)]));
    const total = decisionOrder.reduce((sum, decision) => sum + (counts.get(decision) || 0), 0);
    chart.setOption(
      {
        animationDuration: 450,
        title: {
          text: total.toLocaleString("zh-CN"),
          subtext: "总次数",
          left: "center",
          top: "40%",
          textStyle: { color: colors.text, fontSize: 24, fontWeight: 650 },
          subtextStyle: { color: colors.textSecondary, fontSize: 12 },
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
          orient: "vertical",
          right: 0,
          top: "middle",
          textStyle: { color: colors.textSecondary },
          formatter: (name: string) => {
            const decision = decisionOrder.find((item) => decisionMeta[item].label === name);
            if (!decision) {
              return name;
            }
            const count = counts.get(decision) || 0;
            const percent = total > 0 ? ((count / total) * 100).toFixed(1) : "0.0";
            return `${name}  ${count.toLocaleString("zh-CN")}  ${percent}%`;
          },
        },
        series: [
          {
            name: "决策分布",
            type: "pie",
            radius: ["56%", "76%"],
            center: ["35%", "50%"],
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
      } satisfies EChartsOption,
      true,
    );
  }, [colors.border, colors.elevated, colors.surface, colors.text, colors.textSecondary, data, mode]);

  return <div ref={containerRef} className="chart-canvas decision-chart" aria-label="决策分布" />;
}
