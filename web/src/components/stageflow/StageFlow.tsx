import {
  CheckCircleFilled,
  CloseCircleFilled,
  ExclamationCircleFilled,
  LoadingOutlined,
  LockFilled,
  StopOutlined,
} from "@ant-design/icons";
import { motion, useReducedMotion } from "framer-motion";
import type { CSSProperties, ReactNode } from "react";
import { useEffect, useMemo, useRef, useState } from "react";

import { decisionMeta } from "@/constants/labels";
import { palette } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";

import { StageVerdict } from "./StageVerdict";
import { stageNodes } from "./stageNodes";
import type { StageFlowData, StageFlowProps, StageKey, StageStatus, StageStep } from "./types";

type Orientation = "horizontal" | "vertical";

interface StageStyle extends CSSProperties {
  "--stage-color": string;
  "--stage-surface": string;
  "--stage-border": string;
}

function safeMilliseconds(value: number | undefined): number {
  return value !== undefined && Number.isFinite(value) && value >= 0 ? value : 0;
}

function playbackDuration(step: StageStep): number {
  const latency = safeMilliseconds(step.latencyMs);
  return latency > 0 ? Math.min(900, Math.max(300, Math.round(latency))) : 360;
}

function formatLatency(step: StageStep): string {
  if (step.key === "decide") return "—";
  const latency = safeMilliseconds(step.latencyMs);
  return latency > 0 ? `${latency.toFixed(latency < 10 ? 1 : 0)}ms` : "—";
}

function statusLabel(status: StageStatus): string {
  switch (status) {
    case "pending": return "等待";
    case "active": return "处理中";
    case "pass": return "通过";
    case "warn": return "告警";
    case "block": return "拦截";
    case "skip": return "未执行";
    case "locked": return "待审批";
  }
}

function nodeStatusLabel(status: StageStatus, decision: StageFlowData["decision"], key: StageKey): string {
  if (decision === "approve" && key === "decide" && status === "warn") return "转人工";
  if (decision === "error" && status === "block") return "失败";
  return statusLabel(status);
}

function statusIcon(status: StageStatus, fallback: ReactNode): ReactNode {
  switch (status) {
    case "active": return <LoadingOutlined spin />;
    case "pass": return <CheckCircleFilled />;
    case "warn": return <ExclamationCircleFilled />;
    case "block": return <CloseCircleFilled />;
    case "skip": return <StopOutlined />;
    case "locked": return <LockFilled />;
    case "pending": return fallback;
  }
}

function statusColor(status: StageStatus, mode: "dark" | "light", decision?: StageFlowData["decision"]): string {
  switch (status) {
    case "active": return palette[mode].brand;
    case "pass": return decisionMeta.allow.color;
    case "warn": return decisionMeta.warn.color;
    case "block": return decision === "error" ? decisionMeta.error.color : decisionMeta.deny.color;
    case "locked": return decisionMeta.approve.color;
    case "skip":
    case "pending":
      return palette[mode].textSecondary;
  }
}

function normalizeSteps(data: StageFlowData): StageStep[] {
  return stageNodes.map((node) => {
    const supplied = data.steps.find((step) => step.key === node.key);
    return {
      key: node.key,
      label: node.label,
      status: supplied?.status || "pending",
      latencyMs: safeMilliseconds(supplied?.latencyMs),
      note: supplied?.note?.trim() || node.subtitle,
    };
  });
}

function finalSummary(data: StageFlowData): string {
  const rows = Math.max(0, Number.isFinite(data.rowsReturned) ? data.rowsReturned || 0 : 0).toLocaleString("zh-CN");
  const total = safeMilliseconds(data.totalLatencyMs);
  const latency = total > 0 ? ` · 总耗时 ${total.toFixed(total < 10 ? 1 : 0)} ms` : "";
  switch (data.decision) {
    case "allow": return `已放行 · 返回 ${rows} 行${latency}`;
    case "warn": return `已放行，请注意结果集规模 · 返回 ${rows} 行${latency}`;
    case "approve": return `已转人工审批 · 用户 SQL 未执行${latency}`;
    case "deny": return `已拦截 · 用户 SQL 未执行${latency}`;
    case "error": return `数据库执行失败 · 错误已留痕${latency}`;
  }
}

function Connector({
  orientation,
  color,
  activeColor,
  pendingColor,
  complete,
  active,
  durationMs,
  reducedMotion,
}: {
  orientation: Orientation;
  color: string;
  activeColor: string;
  pendingColor: string;
  complete: boolean;
  active: boolean;
  durationMs: number;
  reducedMotion: boolean;
}) {
  const vertical = orientation === "vertical";
  return (
    <svg className={`stage-connector stage-connector-${orientation}`} viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
      <line x1={vertical ? 50 : 4} y1={vertical ? 4 : 50} x2={vertical ? 50 : 96} y2={vertical ? 96 : 50} stroke={complete ? color : pendingColor} strokeWidth="3" strokeDasharray={complete ? undefined : "6 7"} vectorEffect="non-scaling-stroke" />
      {active && !reducedMotion ? (
        <motion.circle
          cx={vertical ? 50 : 8}
          cy={vertical ? 8 : 50}
          r="5"
          fill={activeColor}
          initial={vertical ? { cy: 8 } : { cx: 8 }}
          animate={vertical ? { cy: 92 } : { cx: 92 }}
          transition={{ duration: durationMs / 1_000, ease: "linear" }}
        />
      ) : null}
    </svg>
  );
}

function fallbackVerdict(data: StageFlowData) {
  if (data.decision !== "error" || data.verdict) return data.verdict;
  return { title: "处理失败", message: data.errorMsg?.trim() || "请求未能完成安全处理" };
}

export function StageFlow({ data, autoPlay = true, replayKey = 0, dense = false, onStepChange, onFinish }: StageFlowProps) {
  const mode = useThemeStore((state) => state.mode);
  const reduceMotion = Boolean(useReducedMotion());
  const [orientation, setOrientation] = useState<Orientation>("horizontal");
  const [activeIndex, setActiveIndex] = useState(-1);
  const [completedThrough, setCompletedThrough] = useState(-1);
  const [finished, setFinished] = useState(false);
  const [blockedStage, setBlockedStage] = useState<StageKey | undefined>();
  const containerRef = useRef<HTMLDivElement>(null);
  const stepCallbackRef = useRef(onStepChange);
  const finishCallbackRef = useRef(onFinish);
  const normalizedSteps = useMemo(() => normalizeSteps(data), [data]);
  const colors = palette[mode];

  useEffect(() => { stepCallbackRef.current = onStepChange; }, [onStepChange]);
  useEffect(() => { finishCallbackRef.current = onFinish; }, [onFinish]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const updateOrientation = () => setOrientation(container.clientWidth < 760 ? "vertical" : "horizontal");
    updateOrientation();
    const observer = new ResizeObserver(updateOrientation);
    observer.observe(container);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    let cancelled = false;
    let timer = 0;
    let frame = 0;
    setActiveIndex(-1);
    setCompletedThrough(-1);
    setFinished(false);
    setBlockedStage(undefined);

    const finish = () => {
      if (cancelled) return;
      setActiveIndex(-1);
      setFinished(true);
      finishCallbackRef.current?.(data.decision);
    };

    if (!autoPlay || reduceMotion) {
      // 静态/降级模式：同步直接呈现终态，不依赖 rAF（后台标签页、省电节流、无窗口环境下 rAF 可能被挂起导致卡在“等待”）
      normalizedSteps.forEach((step, index) => stepCallbackRef.current?.(step.key, index));
      setActiveIndex(-1);
      setCompletedThrough(normalizedSteps.length - 1);
      setFinished(true);
      finishCallbackRef.current?.(data.decision);
      return () => {
        cancelled = true;
      };
    }

    const play = (index: number) => {
      if (cancelled || index >= normalizedSteps.length) {
        if (!cancelled) finish();
        return;
      }
      const step = normalizedSteps[index];
      setActiveIndex(index);
      stepCallbackRef.current?.(step.key, index);
      timer = window.setTimeout(() => {
        if (cancelled) return;
        setCompletedThrough(index);
        if (data.blockAt === step.key) {
          setBlockedStage(step.key);
          finish();
          return;
        }
        play(index + 1);
      }, playbackDuration(step));
    };

    frame = window.requestAnimationFrame(() => play(0));
    return () => {
      cancelled = true;
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, [autoPlay, data, normalizedSteps, reduceMotion, replayKey]);

  const verdict = fallbackVerdict(data);
  const summaryColor = data.decision === "approve"
    ? decisionMeta.approve.color
    : data.decision === "warn"
      ? decisionMeta.warn.color
      : data.decision === "allow"
        ? decisionMeta.allow.color
        : data.decision === "error"
          ? decisionMeta.error.color
          : decisionMeta.deny.color;

  return (
    <section className={`stage-flow${dense ? " stage-flow-dense" : ""}`} ref={containerRef} aria-label="SQL 六段安检流">
      <div className={`stage-flow-track stage-flow-${orientation}`} role="list">
        {normalizedSteps.map((step, index) => {
          const visualStatus: StageStatus = finished
            ? step.status
            : activeIndex === index
              ? "active"
              : completedThrough >= index
                ? step.status
                : "pending";
          const color = data.decision === "approve" && step.key === "decide" && visualStatus === "warn"
            ? decisionMeta.approve.color
            : statusColor(visualStatus, mode, data.decision);
          const visualLabel = nodeStatusLabel(visualStatus, data.decision, step.key);
          const style: StageStyle = { "--stage-color": color, "--stage-surface": colors.surface, "--stage-border": colors.border };
          const shouldShake = blockedStage === step.key && !reduceMotion && (data.decision === "deny" || data.decision === "error");
          const connectorComplete = completedThrough > index && (!data.blockAt || stageNodes.findIndex((node) => node.key === data.blockAt) > index);
          const connectorActive = activeIndex === index && data.blockAt !== step.key;
          const note = step.note;
          return (
            <div className="stage-flow-segment" key={step.key}>
              <motion.div
                className={`stage-node stage-node-${visualStatus}`}
                style={style}
                role="listitem"
                aria-label={`${step.label}：${visualLabel}`}
                initial={false}
                animate={shouldShake ? { x: [0, -5, 5, -3, 3, 0] } : visualStatus === "active" && !reduceMotion ? { scale: [1, 1.05, 1] } : { x: 0, scale: 1 }}
                transition={shouldShake ? { duration: 0.25 } : visualStatus === "active" ? { duration: 0.9, repeat: Infinity } : { duration: 0.18 }}
              >
                <motion.div
                  className="stage-node-icon"
                  key={`${step.key}-${visualStatus}`}
                  aria-hidden="true"
                  initial={visualStatus === "pass" && !reduceMotion ? { opacity: 0.5, scale: 0.76 } : false}
                  animate={{ opacity: 1, scale: 1 }}
                  transition={{ duration: 0.2 }}
                >
                  {statusIcon(visualStatus, stageNodes[index].icon)}
                </motion.div>
                <div className="stage-node-copy">
                  <div className="stage-node-title"><strong>{step.label}</strong><span>{visualLabel}</span></div>
                  <div className="stage-node-note">{note || stageNodes[index].subtitle}</div>
                  <div className="stage-node-latency mono-text">{formatLatency(step)}</div>
                </div>
              </motion.div>
              {index < normalizedSteps.length - 1 ? (
                <Connector
                  orientation={orientation}
                  color={statusColor(step.status, mode, data.decision)}
                  activeColor={colors.brand}
                  pendingColor={colors.border}
                  complete={connectorComplete}
                  active={connectorActive}
                  durationMs={playbackDuration(step)}
                  reducedMotion={reduceMotion}
                />
              ) : null}
            </div>
          );
        })}
      </div>
      {finished ? <div className="stage-flow-summary mono-text" style={{ color: summaryColor }}>{finalSummary(data)}</div> : null}
      {finished && verdict ? <StageVerdict data={verdict} decision={data.decision} dense={dense} /> : null}
    </section>
  );
}
