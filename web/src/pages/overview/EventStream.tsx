import { ReloadOutlined } from "@ant-design/icons";
import { Button, Empty, List, Skeleton, Tag, Tooltip, message } from "antd";
import { motion } from "framer-motion";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { listAudit } from "@/api/audit";
import type { AuditStreamEvent, AuditView, StreamStatus } from "@/api/types";
import { getErrorCodeMeta } from "@/constants/errorCodes";
import { getDecisionMeta, statementLabel } from "@/constants/labels";

interface EventStreamProps {
  liveEvents: AuditStreamEvent[];
  streamStatus: StreamStatus;
  pollEnabled: boolean;
  realtimeDenyIDs: ReadonlySet<number>;
  refreshToken: number;
  onRefreshingChange: (refreshing: boolean) => void;
}

type DisplayEvent = AuditStreamEvent & Partial<AuditView>;

function formatTime(value: string): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) {
    return "--:--:--";
  }
  return new Date(timestamp).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

function shortAgent(value: string | null | undefined): string {
  const agentID = (value || "").trim();
  if (!agentID) {
    return "—";
  }
  return agentID.length > 12 ? `${agentID.slice(0, 8)}…${agentID.slice(-3)}` : agentID;
}

function sqlSummary(value: string | null | undefined): string {
  return (value || "").replace(/\s+/g, " ").trim() || "—";
}

function meaningfulText(value: string | null | undefined): string | undefined {
  return value && value.trim() ? value : undefined;
}

function compareEvents(left: { id: number; ts: string }, right: { id: number; ts: string }): number {
  const leftTime = Date.parse(left.ts);
  const rightTime = Date.parse(right.ts);
  const safeLeftTime = Number.isFinite(leftTime) ? leftTime : Number.NEGATIVE_INFINITY;
  const safeRightTime = Number.isFinite(rightTime) ? rightTime : Number.NEGATIVE_INFINITY;
  return safeRightTime - safeLeftTime || right.id - left.id;
}

function mergePolledEvents(current: AuditView[], incoming: AuditView[]): AuditView[] {
  const byID = new Map<number, AuditView>();
  current.forEach((event) => byID.set(event.id, event));
  incoming.forEach((event) => byID.set(event.id, event));
  return [...byID.values()].sort(compareEvents).slice(0, 100);
}

export function EventStream({ liveEvents, streamStatus, pollEnabled, realtimeDenyIDs, refreshToken, onRefreshingChange }: EventStreamProps) {
  const [polledEvents, setPolledEvents] = useState<AuditView[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [newDenyIDs, setNewDenyIDs] = useState<Set<number>>(new Set());
  const polledEventsRef = useRef<AuditView[]>([]);
  const liveEventsRef = useRef(liveEvents);
  const mountedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const highlightTimersRef = useRef<Map<number, number>>(new Map());
  const highlightedDenyIDsRef = useRef<Set<number>>(new Set());

  liveEventsRef.current = liveEvents;

  const events = useMemo<DisplayEvent[]>(() => {
    const byID = new Map<number, DisplayEvent>();
    liveEvents.forEach((event) => byID.set(event.id, event));
    polledEvents.forEach((event) => {
      const realtime = byID.get(event.id);
      byID.set(event.id, realtime ? { ...realtime, ...event } : event);
    });
    return [...byID.values()].sort(compareEvents).slice(0, 100);
  }, [liveEvents, polledEvents]);

  const refresh = useCallback(async () => {
    if (controllerRef.current !== null || !mountedRef.current) {
      return;
    }
    onRefreshingChange(true);
    const controller = new AbortController();
    controllerRef.current = controller;
    try {
      const page = await listAudit({ page: 1, page_size: 100 }, controller.signal);
      if (!mountedRef.current || controller.signal.aborted) {
        return;
      }
      const incoming = page.list.slice(0, 100);
      const merged = mergePolledEvents(polledEventsRef.current, incoming);
      polledEventsRef.current = merged;
      setPolledEvents(merged);
      setError(false);
    } catch {
      if (!controller.signal.aborted && mountedRef.current) {
        setError(true);
        if (polledEventsRef.current.length > 0 || liveEventsRef.current.length > 0) {
          void message.warning("风险事件刷新失败，已保留上次结果");
        }
      }
    } finally {
      if (mountedRef.current && controllerRef.current === controller) {
        setLoading(false);
        onRefreshingChange(false);
      }
      if (controllerRef.current === controller) {
        controllerRef.current = null;
      }
    }
  }, [onRefreshingChange]);

  useEffect(() => {
    mountedRef.current = true;
    void refresh();
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      controllerRef.current = null;
      highlightTimersRef.current.forEach((timer) => window.clearTimeout(timer));
      highlightTimersRef.current.clear();
    };
  }, [refresh]);

  useEffect(() => {
    if (!pollEnabled) {
      return;
    }
    const timer = window.setInterval(() => void refresh(), 30_000);
    return () => window.clearInterval(timer);
  }, [pollEnabled, refresh]);

  useEffect(() => {
    if (streamStatus !== "polling-fallback" || !pollEnabled) return;
    void refresh();
  }, [pollEnabled, refresh, streamStatus]);

  useEffect(() => {
    if (refreshToken > 0) {
      void refresh();
    }
  }, [refresh, refreshToken]);

  useEffect(() => {
    const freshDenyIDs = liveEvents
      .filter((event) => event.decision.trim().toLowerCase() === "deny"
        && realtimeDenyIDs.has(event.id)
        && !highlightedDenyIDsRef.current.has(event.id))
      .map((event) => event.id);
    if (freshDenyIDs.length === 0) return;

    freshDenyIDs.forEach((id) => highlightedDenyIDsRef.current.add(id));
    setNewDenyIDs((current) => new Set([...current, ...freshDenyIDs]));
    freshDenyIDs.forEach((id) => {
      const timer = window.setTimeout(() => {
        highlightTimersRef.current.delete(id);
        if (mountedRef.current) {
          setNewDenyIDs((current) => {
            const next = new Set(current);
            next.delete(id);
            return next;
          });
        }
      }, 2_000);
      highlightTimersRef.current.set(id, timer);
    });
  }, [liveEvents, realtimeDenyIDs]);

  return (
    <div className="event-stream">
      <div className="section-toolbar">
        <span className="section-caption">最近风险事件</span>
        <Button
          type="text"
          size="small"
          icon={<ReloadOutlined spin={loading} />}
          loading={loading}
          onClick={() => void refresh()}
          aria-label="刷新风险事件"
        />
      </div>
      {loading && events.length === 0 ? (
        <div className="event-skeleton">
          <Skeleton active paragraph={{ rows: 4 }} title={false} />
        </div>
      ) : events.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={error ? "风险事件暂时不可用" : "当前区间暂无风险事件"} />
      ) : (
        <List
          className="event-list"
          dataSource={events}
          rowKey={(event) => event.id}
          split={false}
          renderItem={(event) => {
            const meta = getDecisionMeta(event.decision);
            const errorCode = event.decision.trim().toLowerCase() === "error"
              ? getErrorCodeMeta(event.error_code)
              : undefined;
            const deny = event.decision.trim().toLowerCase() === "deny";
            const isNewDeny = newDenyIDs.has(event.id);
            const rawSQL = meaningfulText(event.sql_raw);
            const summary = rawSQL
              ? sqlSummary(rawSQL)
              : sqlSummary(meaningfulText(event.objects) || meaningfulText(event.stmt_type));
            const sqlCell = <span className="event-sql mono-text">{summary}</span>;
            const row = (
              <div className={`event-row${deny ? " event-row-deny" : ""}${isNewDeny ? " event-row-new" : ""}`}>
                <span className="event-time mono-text">{formatTime(event.ts)}</span>
                <span className="event-agent mono-text">{shortAgent(event.agent_id)}</span>
                <span className="event-tags">
                  <Tag color={meta.tagColor}>{meta.label}</Tag>
                  {errorCode ? <Tag color="volcano">{errorCode.label}</Tag> : null}
                </span>
                <span className="event-stmt">{statementLabel(event.stmt_type)}</span>
                {rawSQL ? <Tooltip title={rawSQL}>{sqlCell}</Tooltip> : sqlCell}
              </div>
            );
            return deny ? (
              <motion.div
                key={`${event.id}-${isNewDeny ? "new" : "steady"}`}
                layout
                initial={isNewDeny ? { opacity: 0, y: -18 } : false}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.25 }}
              >
                {row}
              </motion.div>
            ) : (
              <div key={event.id}>{row}</div>
            );
          }}
        />
      )}
    </div>
  );
}
