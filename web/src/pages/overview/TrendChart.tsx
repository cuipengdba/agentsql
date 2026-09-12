import * as echarts from "echarts";
import type { EChartsOption } from "echarts";
import { useEffect, useRef } from "react";

import type { TrendDay } from "@/api/types";
import { palette, type ThemeMode } from "@/theme/tokens";

interface TrendChartProps {
  data: TrendDay[];
  mode: ThemeMode;
}

const seriesMeta = [
  { key: "allow", name: "放行", colorKey: "allow" },
  { key: "warn", name: "告警", colorKey: "warn" },
  { key: "approve", name: "待审批", colorKey: "approve" },
  { key: "deny", name: "拦截", colorKey: "deny" },
] as const;

function finiteCount(value: number | undefined): number {
  return Number.isFinite(value) && value !== undefined ? Math.max(0, value) : 0;
}

export function TrendChart({ data, mode }: TrendChartProps) {
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
    const categories = data.map((item) => {
      const date = item.date || "";
      return date.length >= 10 ? date.slice(5, 10) : date;
    });
    const series = [
      ...seriesMeta.map((meta) => ({
        name: meta.name,
        type: "bar" as const,
        stack: "decisions",
        barMaxWidth: 22,
        itemStyle: { color: palette.semantic[meta.colorKey] },
        emphasis: { focus: "series" as const },
        data: data.map((item) => finiteCount(item[meta.key])),
      })),
      {
        name: "拦截趋势",
        type: "line" as const,
        yAxisIndex: 1,
        smooth: true,
        symbol: "circle",
        symbolSize: 6,
        lineStyle: { width: 2, color: palette.semantic.deny },
        itemStyle: { color: palette.semantic.deny },
        data: data.map((item) => finiteCount(item.deny)),
      },
    ];
    chart.setOption(
      {
        animationDuration: 500,
        color: [
          palette.semantic.allow,
          palette.semantic.warn,
          palette.semantic.approve,
          palette.semantic.deny,
        ],
        grid: { left: 42, right: 42, top: 48, bottom: 30, containLabel: true },
        legend: {
          top: 6,
          left: 0,
          textStyle: { color: colors.textSecondary },
          data: ["放行", "告警", "待审批", "拦截", "拦截趋势"],
        },
        tooltip: {
          trigger: "axis",
          axisPointer: { type: "cross" },
          backgroundColor: colors.elevated,
          borderColor: colors.border,
          textStyle: { color: colors.text },
          formatter: (params: unknown) => {
            if (!Array.isArray(params)) {
              return "";
            }
            const first = params[0] as { axisValue?: string } | undefined;
            const dataIndex = Number((first as { dataIndex?: number } | undefined)?.dataIndex);
            const category = first?.axisValue || "";
            const row = Number.isInteger(dataIndex) && dataIndex >= 0
              ? data[dataIndex]
              : data.find((item) => item.date.slice(5, 10) === category);
            if (!row) {
              return category;
            }
            return [
              category,
              `总计：${finiteCount(row.total).toLocaleString("zh-CN")}`,
              `放行：${finiteCount(row.allow).toLocaleString("zh-CN")}`,
              `告警：${finiteCount(row.warn).toLocaleString("zh-CN")}`,
              `待审批：${finiteCount(row.approve).toLocaleString("zh-CN")}`,
              `拦截：${finiteCount(row.deny).toLocaleString("zh-CN")}`,
            ].join("<br/>");
          },
        },
        xAxis: {
          type: "category",
          data: categories,
          axisLabel: { color: colors.textSecondary },
          axisLine: { lineStyle: { color: colors.border } },
          axisTick: { lineStyle: { color: colors.border } },
        },
        yAxis: [
          {
            type: "value",
            min: 0,
            axisLabel: { color: colors.textSecondary },
            splitLine: { lineStyle: { color: colors.border, opacity: mode === "dark" ? 0.5 : 0.8 } },
          },
          {
            type: "value",
            min: 0,
            axisLabel: { color: colors.textSecondary },
            splitLine: { show: false },
          },
        ],
        series,
      } satisfies EChartsOption,
      true,
    );
  }, [colors.border, colors.elevated, colors.text, colors.textSecondary, data, mode]);

  return <div ref={containerRef} className="chart-canvas" aria-label="近阶段请求趋势" />;
}
