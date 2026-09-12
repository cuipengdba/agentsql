import { ReloadOutlined } from "@ant-design/icons";
import { Button, Empty, List, Skeleton, Tag, Tooltip, message } from "antd";
import { motion } from "framer-motion";
import { useCallback, useEffect, useRef, useState } from "react";

import { listAudit } from "@/api/audit";
import type { AuditView } from "@/api/types";
import { getDecisionMeta, statementLabel } from "@/constants/labels";

interface EventStreamProps {
  autoRefresh: boolean;
  refreshToken: number;
  onRefreshingChange: (refreshing: boolean) => void;
}

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

export function EventStream({ autoRefresh, refreshToken, onRefreshingChange }: EventStreamProps) {
  const [events, setEvents] = useState<AuditView[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [newDenyIDs, setNewDenyIDs] = useState<Set<number>>(new Set());
  const eventsRef = useRef<AuditView[]>([]);
  const mountedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const highlightTimersRef = useRef<Map<number, number>>(new Map());

  const refresh = useCallback(async () => {
    if (controllerRef.current !== null || !mountedRef.current) {
      return;
    }
    onRefreshingChange(true);
    const controller = new AbortController();
    controllerRef.current = controller;
    try {
      const page = await listAudit({ page: 1, page_size: 20 }, controller.signal);
      if (!mountedRef.current || controller.signal.aborted) {
        return;
      }
      const incoming = page.list.slice(0, 20);
      const previousIDs = new Set(eventsRef.current.map((event) => event.id));
      const freshDenyIDs = new Set(
        incoming
          .filter((event) => event.decision.toLowerCase() === "deny" && !previousIDs.has(event.id))
          .map((event) => event.id),
      );
      setEvents(incoming);
      eventsRef.current = incoming;
      setError(false);
      if (freshDenyIDs.size > 0) {
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
      }
    } catch {
      if (!controller.signal.aborted && mountedRef.current) {
        setError(true);
        if (eventsRef.current.length > 0) {
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
    if (!autoRefresh) {
      return;
    }
    const timer = window.setInterval(() => void refresh(), 30_000);
    return () => window.clearInterval(timer);
  }, [autoRefresh, refresh]);

  useEffect(() => {
    if (refreshToken > 0) {
      void refresh();
    }
  }, [refresh, refreshToken]);

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
            const deny = event.decision.toLowerCase() === "deny";
            const row = (
              <div className={`event-row${deny ? " event-row-deny" : ""}${newDenyIDs.has(event.id) ? " event-row-new" : ""}`}>
                <span className="event-time mono-text">{formatTime(event.ts)}</span>
                <span className="event-agent mono-text">{shortAgent(event.agent_id)}</span>
                <Tag color={meta.tagColor}>{meta.label}</Tag>
                <span className="event-stmt">{statementLabel(event.stmt_type)}</span>
                <Tooltip title={event.sql_raw || "—"}>
                  <span className="event-sql mono-text">{sqlSummary(event.sql_raw)}</span>
                </Tooltip>
              </div>
            );
            return deny ? (
              <motion.div
                key={event.id}
                layout
                initial={{ opacity: 0, y: -18 }}
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
