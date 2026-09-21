import { ReloadOutlined } from "@ant-design/icons";
import { Button, Empty, Input, List, Pagination, Select, Skeleton, Tag, Tooltip, message } from "antd";
import { motion } from "framer-motion";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { listAudit } from "@/api/audit";
import type { AuditStreamEvent, AuditView, StreamStatus } from "@/api/types";
import { getErrorCodeMeta } from "@/constants/errorCodes";
import { decisionMeta, getDecisionMeta, statementLabel, stmtTypeLabels, type Decision } from "@/constants/labels";

import { PAGE_SIZE, buildEventQuery, canMergeLive, clampPage, mergeLiveIntoPage, type DisplayEvent } from "./eventQuery";

interface EventStreamProps {
  liveEvents: AuditStreamEvent[];
  streamStatus: StreamStatus;
  pollEnabled: boolean;
  realtimeDenyIDs: ReadonlySet<number>;
  refreshToken: number;
  onRefreshingChange: (refreshing: boolean) => void;
}

interface ActiveRequest {
  controller: AbortController;
  key: string;
  sequence: number;
}

const decisionOrder: Decision[] = ["allow", "warn", "approve", "deny", "error"];

function formatTime(value: string): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return "--:--:--";
  return new Date(timestamp).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

function shortAgent(value: string | null | undefined): string {
  const agentID = (value || "").trim();
  if (!agentID) return "—";
  return agentID.length > 12 ? `${agentID.slice(0, 8)}…${agentID.slice(-3)}` : agentID;
}

function sqlSummary(value: string | null | undefined): string {
  return (value || "").replace(/\s+/g, " ").trim() || "—";
}

function meaningfulText(value: string | null | undefined): string | undefined {
  return value && value.trim() ? value : undefined;
}

export function EventStream({ liveEvents, streamStatus, pollEnabled, realtimeDenyIDs, refreshToken, onRefreshingChange }: EventStreamProps) {
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [decision, setDecision] = useState<Decision>();
  const [stmtType, setStmtType] = useState<string>();
  const [keywordDraft, setKeywordDraft] = useState("");
  const [appliedKeyword, setAppliedKeyword] = useState("");
  const [polledEvents, setPolledEvents] = useState<AuditView[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [newDenyIDs, setNewDenyIDs] = useState<Set<number>>(new Set());

  const polledEventsRef = useRef<AuditView[]>([]);
  const mountedRef = useRef(false);
  const activeRequestRef = useRef<ActiveRequest | null>(null);
  const requestSequenceRef = useRef(0);
  const queryIdentityRef = useRef("");
  const desiredQueryKeyRef = useRef("");
  const highlightTimersRef = useRef<Map<number, number>>(new Map());
  const highlightedDenyIDsRef = useRef<Set<number>>(new Set());

  const query = useMemo(
    () => buildEventQuery({ page, pageSize: PAGE_SIZE, decision, stmtType, appliedKeyword }),
    [appliedKeyword, decision, page, stmtType],
  );
  const queryKey = `${page}|${decision || ""}|${stmtType || ""}|${appliedKeyword.trim()}`;
  desiredQueryKeyRef.current = queryKey;
  const mergeLive = canMergeLive({ page, decision, stmtType, appliedKeyword });
  const hasFilters = Boolean(decision || stmtType || appliedKeyword.trim());
  const queryMatchesDisplayedData = queryIdentityRef.current === queryKey;
  const displayedPolledEvents = queryMatchesDisplayedData ? polledEvents : [];
  const displayedTotal = queryMatchesDisplayedData ? total : 0;
  const displayLoading = loading || !queryMatchesDisplayedData;

  const events = useMemo<DisplayEvent[]>(
    () => mergeLive ? mergeLiveIntoPage({ polled: displayedPolledEvents, live: liveEvents }) : displayedPolledEvents,
    [displayedPolledEvents, liveEvents, mergeLive],
  );

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      activeRequestRef.current?.controller.abort();
      activeRequestRef.current = null;
      highlightTimersRef.current.forEach((timer) => window.clearTimeout(timer));
      highlightTimersRef.current.clear();
    };
  }, []);

  const requestPage = useCallback(async (skipIfSameQueryInFlight = false) => {
    if (!mountedRef.current) return;
    const active = activeRequestRef.current;
    if (active) {
      if (skipIfSameQueryInFlight && active.key === queryKey) return;
      active.controller.abort();
    }

    const controller = new AbortController();
    const sequence = requestSequenceRef.current + 1;
    requestSequenceRef.current = sequence;
    activeRequestRef.current = { controller, key: queryKey, sequence };
    setLoading(true);
    onRefreshingChange(true);

    try {
      const response = await listAudit(query, controller.signal);
      const current = activeRequestRef.current;
      if (!mountedRef.current || controller.signal.aborted || current?.controller !== controller
        || current.sequence !== sequence || desiredQueryKeyRef.current !== queryKey) return;

      const nextTotal = Math.max(0, response.total);
      setTotal(nextTotal);
      const clamped = clampPage(page, nextTotal, PAGE_SIZE);
      if (page > clamped) {
        setPage(clamped);
        return;
      }

      const incoming = response.list.slice(0, PAGE_SIZE);
      polledEventsRef.current = incoming;
      setPolledEvents(incoming);
      setError(false);
    } catch {
      const current = activeRequestRef.current;
      if (!controller.signal.aborted && mountedRef.current && current?.controller === controller
        && current.sequence === sequence && desiredQueryKeyRef.current === queryKey) {
        setError(true);
        if (polledEventsRef.current.length > 0) {
          void message.warning("风险事件刷新失败，已保留上次结果");
        }
      }
    } finally {
      const current = activeRequestRef.current;
      if (mountedRef.current && current?.controller === controller && current.sequence === sequence) {
        activeRequestRef.current = null;
        setLoading(false);
        onRefreshingChange(false);
      }
    }
  }, [onRefreshingChange, page, query, queryKey]);

  useEffect(() => {
    if (queryIdentityRef.current !== queryKey) {
      queryIdentityRef.current = queryKey;
      polledEventsRef.current = [];
      setPolledEvents([]);
      setTotal(0);
      setError(false);
    }
    void requestPage();
  }, [appliedKeyword, decision, page, refreshToken, requestPage, stmtType, queryKey]);

  useEffect(() => {
    if (!pollEnabled || page !== 1) return;
    const timer = window.setInterval(() => void requestPage(true), 30_000);
    return () => window.clearInterval(timer);
  }, [page, pollEnabled, requestPage]);

  useEffect(() => {
    if (streamStatus === "polling-fallback" && pollEnabled && page === 1) {
      void requestPage(true);
    }
  }, [page, pollEnabled, requestPage, streamStatus]);

  useEffect(() => {
    const activeWindow = new Set<number>(liveEvents.slice(0, 100).map((event) => event.id));
    highlightedDenyIDsRef.current.forEach((id) => {
      if (!activeWindow.has(id)) highlightedDenyIDsRef.current.delete(id);
    });

    if (!mergeLive) {
      highlightTimersRef.current.forEach((timer) => window.clearTimeout(timer));
      highlightTimersRef.current.clear();
      setNewDenyIDs(new Set());
      liveEvents.forEach((event) => {
        if (event.decision.trim().toLowerCase() === "deny" && realtimeDenyIDs.has(event.id)) {
          highlightedDenyIDsRef.current.add(event.id);
        }
      });
      return;
    }

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
  }, [liveEvents, mergeLive, realtimeDenyIDs]);

  const handleKeywordSearch = (value: string) => {
    setAppliedKeyword(value.trim());
    setPage(1);
  };

  return (
    <div className="event-stream">
      <div className="section-toolbar">
        <span className="section-caption">最近风险事件</span>
        <Button
          type="text"
          size="small"
          icon={<ReloadOutlined spin={displayLoading} />}
          loading={displayLoading}
          onClick={() => void requestPage()}
          aria-label="刷新风险事件"
        />
      </div>
      <div className="event-filters">
        <Select<Decision>
          allowClear
          value={decision}
          placeholder="全部决策"
          aria-label="按决策筛选"
          options={decisionOrder.map((value) => ({ value, label: decisionMeta[value].label }))}
          onChange={(value) => { setDecision(value); setPage(1); }}
        />
        <Select<string>
          allowClear
          value={stmtType}
          placeholder="全部类型"
          aria-label="按语句类型筛选"
          options={Object.entries(stmtTypeLabels).map(([value, label]) => ({ value, label }))}
          onChange={(value) => { setStmtType(value); setPage(1); }}
        />
        <Input.Search
          allowClear
          value={keywordDraft}
          placeholder="搜索 SQL 文本"
          aria-label="搜索 SQL"
          onChange={(event) => setKeywordDraft(event.target.value)}
          onSearch={handleKeywordSearch}
        />
      </div>
      <div className="event-content">
        {displayLoading && events.length === 0 ? (
          <div className="event-skeleton">
            <Skeleton active paragraph={{ rows: 4 }} title={false} />
          </div>
        ) : error && displayedPolledEvents.length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="风险事件暂时不可用" />
        ) : events.length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={hasFilters ? "没有符合条件的风险事件" : "当前区间暂无风险事件"} />
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
      <Pagination
        className="event-pagination"
        size="small"
        current={page}
        pageSize={PAGE_SIZE}
        total={displayedTotal}
        showSizeChanger={false}
        showTotal={(count) => `共 ${count} 条`}
        onChange={setPage}
      />
    </div>
  );
}
