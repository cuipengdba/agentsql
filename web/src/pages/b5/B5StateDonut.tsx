import * as echarts from "echarts";
import type { EChartsOption } from "echarts";
import { useEffect, useMemo, useRef } from "react";

import { palette, type ThemeMode } from "@/theme/tokens";

export interface B5StateDatum { state: string; count: number }

const stateColors = ["#06b6d4", "#14b8a6", "#22c55e", "#f59e0b", "#f97316", "#ef4444", "#64748b"];

export function buildB5StateOption(data: B5StateDatum[], mode: ThemeMode): EChartsOption {
  const colors = palette[mode];
  const safe = data.map((item) => ({ name: item.state, value: Number.isFinite(item.count) ? Math.max(0, item.count) : 0 }));
  const total = safe.reduce((sum, item) => sum + item.value, 0);
  return {
    animationDuration: 400,
    title: { text: total.toLocaleString("zh-CN"), subtext: "总数", left: "center", top: "37%", textAlign: "center", textStyle: { color: colors.text, fontSize: 23 }, subtextStyle: { color: colors.textSecondary } },
    tooltip: { trigger: "item", backgroundColor: colors.elevated, borderColor: colors.border, textStyle: { color: colors.text } },
    legend: { type: "scroll", bottom: 0, left: "center", textStyle: { color: colors.textSecondary }, pageTextStyle: { color: colors.textSecondary } },
    series: [{ type: "pie", radius: ["52%", "72%"], center: ["50%", "43%"], label: { show: false }, itemStyle: { borderColor: colors.surface, borderWidth: 2 }, data: safe.map((item, index) => ({ ...item, itemStyle: { color: stateColors[index % stateColors.length] } })) }],
  };
}

export function B5StateDonut({ data, mode, label }: { data: B5StateDatum[]; mode: ThemeMode; label: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const total = useMemo(() => data.reduce((sum, item) => sum + Math.max(0, item.count || 0), 0), [data]);
  useEffect(() => {
    if (!ref.current) return;
    chart.current = echarts.init(ref.current);
    const resize = () => chart.current?.resize();
    const observer = new ResizeObserver(resize);
    observer.observe(ref.current);
    window.addEventListener("resize", resize);
    return () => { observer.disconnect(); window.removeEventListener("resize", resize); chart.current?.dispose(); };
  }, []);
  useEffect(() => { chart.current?.setOption(buildB5StateOption(data, mode), true); }, [data, mode]);
  return <div ref={ref} className="b5-state-chart" role="img" aria-label={`${label}，共 ${total} 条`} />;
}
