import {
  AuditOutlined,
  DatabaseOutlined,
  FileSearchOutlined,
  FullscreenExitOutlined,
  FullscreenOutlined,
  ReloadOutlined,
  RobotOutlined,
  WarningOutlined,
} from "@ant-design/icons";
import { Alert, Badge, Button, Card, Col, Empty, Row, Segmented, Skeleton, Space, Statistic, Switch, Tooltip, message } from "antd";
import type { ReactNode } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { getAuditOutboxStatus, getDashboardSummary } from "@/api/dashboard";
import { listDatasources, pingDatasource } from "@/api/datasources";
import { fetchGatewayReadiness, type GatewayReadiness } from "@/api/health";
import type { AuditOutboxStatus, AuditStreamEvent, DashboardKPI, DashboardSummary, StreamStatus, TrendDay } from "@/api/types";
import type { Decision } from "@/constants/labels";
import { PageContainer } from "@/components/PageContainer";
import { useEventStream } from "@/hooks/useEventStream";
import { useAuthStore } from "@/store/authStore";
import { selectIsDemo, useDemoStore } from "@/store/demoStore";
import { palette, type ThemeMode } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";

import { BattlePanel } from "./overview/BattlePanel";
import { DecisionDonut } from "./overview/DecisionDonut";
import { EventStream } from "./overview/EventStream";
import { TrendChart } from "./overview/TrendChart";
import { useCountUp } from "./overview/useCountUp";

type Days = 7 | 14 | 30;
type KnownDecision = Decision;

interface AppliedStreamEvent {
  seq: number;
  generation: number;
  event: AuditStreamEvent;
}

const emptyKPI: DashboardKPI = {
  total_requests: 0,
  blocked: 0,
  pending_approvals: 0,
  active_agents: 0,
  datasources_total: 0,
  total_requests_change_pct: null,
  blocked_change_pct: null,
};

function finiteNumber(value: number | null | undefined): number {
  return value !== null && value !== undefined && Number.isFinite(value) ? value : 0;
}

function formatChange(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return "环比 —";
  }
  const direction = value > 0 ? "↑" : value < 0 ? "↓" : "→";
  return `环比 ${direction} ${Math.abs(value).toFixed(1)}%`;
}

function hasTrendData(data: TrendDay[]): boolean {
  return data.some((day) => [day.total, day.allow, day.warn, day.approve, day.deny].some((value) => finiteNumber(value) > 0));
}

function localDateKey(date = new Date()): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function knownDecision(value: string): KnownDecision | null {
  const normalized = value.trim().toLowerCase();
  return normalized === "allow" || normalized === "warn" || normalized === "approve" || normalized === "deny" || normalized === "error"
    ? normalized
    : null;
}

function incrementSummary(current: DashboardSummary, event: AuditStreamEvent): DashboardSummary {
  const decision = knownDecision(event.decision);
  if (!decision) return current;

  const today = localDateKey();
  const trend = current.trend_14d.map((item) => {
    if (item.date !== today) return item;
    if (decision === "error") return { ...item, total: finiteNumber(item.total) + 1 };
    return { ...item, total: finiteNumber(item.total) + 1, [decision]: finiteNumber(item[decision]) + 1 };
  });
  let matchedDistribution = false;
  const distribution = current.decision_distribution.map((item) => {
    if (item.decision.trim().toLowerCase() !== decision) return item;
    matchedDistribution = true;
    return { ...item, count: finiteNumber(item.count) + 1 };
  });
  if (!matchedDistribution) {
    distribution.push({ decision, count: 1 });
  }

  return {
    ...current,
    kpi: {
      ...current.kpi,
      total_requests: finiteNumber(current.kpi.total_requests) + 1,
      blocked: finiteNumber(current.kpi.blocked) + (decision === "deny" ? 1 : 0),
      pending_approvals: finiteNumber(current.kpi.pending_approvals) + (decision === "approve" ? 1 : 0),
    },
    trend_14d: trend,
    decision_distribution: distribution,
  };
}

function compareStreamEvents(left: AuditStreamEvent, right: AuditStreamEvent): number {
  const leftTime = Date.parse(left.ts);
  const rightTime = Date.parse(right.ts);
  const safeLeftTime = Number.isFinite(leftTime) ? leftTime : Number.NEGATIVE_INFINITY;
  const safeRightTime = Number.isFinite(rightTime) ? rightTime : Number.NEGATIVE_INFINITY;
  return safeRightTime - safeLeftTime || right.id - left.id;
}

function StreamStatusBadge({ status }: { status: StreamStatus }) {
  if (status === "live") return <Badge status="success" text="实时" />;
  if (status === "reconnecting") return <Badge status="warning" text="重连中" />;
  return <Badge status="default" text="轮询" />;
}

function CountValue({ value }: { value: number }) {
  const count = useCountUp(value);
  return <>{Math.round(count).toLocaleString("zh-CN")}</>;
}

interface KPIItem {
  label: string;
  value: number;
  icon: ReactNode;
  color: string;
  change?: number | null;
}

function KPICard({ item }: { item: KPIItem }) {
  return (
    <Card className="overview-kpi-card" bordered>
      <div className="kpi-label">
        <span>{item.label}</span>
        <span className="kpi-icon" style={{ color: item.color }} aria-hidden="true">{item.icon}</span>
      </div>
      <Statistic value={item.value} formatter={() => <CountValue value={item.value} />} valueStyle={{ color: item.color }} />
      <span className={`kpi-change ${item.change !== undefined && finiteNumber(item.change) > 0 ? "kpi-change-up" : ""}`}>
        {item.change === undefined ? "环比 —" : formatChange(item.change)}
      </span>
    </Card>
  );
}

function TrendPanel({ data, mode }: { data: TrendDay[]; mode: ThemeMode }) {
  return (
    <Card className="overview-panel chart-panel" title="近阶段请求趋势" bordered>
      {data.length === 0 || !hasTrendData(data) ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="当前区间暂无趋势数据" /> : <TrendChart data={data} mode={mode} />}
    </Card>
  );
}

function DecisionPanel({ data, mode }: { data: DashboardSummary["decision_distribution"]; mode: ThemeMode }) {
  const total = data.reduce((sum, item) => sum + finiteNumber(item.count), 0);
  return (
    <Card className="overview-panel chart-panel" title="决策分布" bordered>
      {data.length === 0 || total <= 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="当前区间暂无决策记录" /> : <DecisionDonut data={data} mode={mode} />}
    </Card>
  );
}

function DemoReadiness({ refreshToken }: { refreshToken: number }) {
  const [readiness, setReadiness] = useState<GatewayReadiness | "checking">("checking");
  const [sources, setSources] = useState<Array<{ id: string; name: string; ready: boolean }> | null>(null);
  const [sourceCheckedAt, setSourceCheckedAt] = useState<Date | null>(null);
  useEffect(() => {
    let active = true;
    const refreshGateway = async () => {
      const next = await fetchGatewayReadiness();
      if (active) setReadiness(next);
    };
    const refreshSources = async () => {
      try {
        const page = await listDatasources({ page: 1, page_size: 100 });
        const checks = await Promise.allSettled(page.list.map((source) => pingDatasource(source.id)));
        if (active) {
          setSources(page.total > page.list.length ? null : page.list.map((source, index) => ({
            id: source.id, name: source.name,
            ready: checks[index].status === "fulfilled" && checks[index].value.ok,
          })));
          setSourceCheckedAt(new Date());
        }
      } catch {
        if (active) { setSources(null); setSourceCheckedAt(new Date()); }
      }
    };
    void refreshGateway();
    void refreshSources();
    const timer = window.setInterval(() => void refreshGateway(), 30_000);
    return () => { active = false; window.clearInterval(timer); };
  }, [refreshToken]);
  const status = readiness === "ready" ? "网关依赖就绪" : readiness === "not_ready" ? "网关依赖未就绪" : readiness === "checking" ? "正在检查网关依赖" : "网关就绪状态不可确认";
  return <Alert
    type={readiness === "ready" ? "info" : readiness === "checking" ? "warning" : "error"}
    showIcon
    message={`Live Demo：${status}`}
    description={<Space direction="vertical" size={4}>
      <span><code>/healthz</code> 仅表示服务存活；<code>/readyz</code> 检查网关依赖。网关就绪不代表每个演示数据源可用。</span>
      <span>演示数据源：{sources === null ? "状态尚未确认" : sources.length === 0 ? "尚未配置" : sources.map((source) => `${source.name} ${source.ready ? "连接正常" : "连接不可用"}`).join("；")}。{sourceCheckedAt ? `检查于 ${sourceCheckedAt.toLocaleTimeString("zh-CN")}` : "正在检查"}；可在 <Link to="/datasources">数据源</Link> 页面复测。</span>
    </Space>}
    style={{ marginBottom: 16 }}
  />;
}

export function Overview() {
  const mode = useThemeStore((state) => state.mode);
  const demoEnabled = useDemoStore(selectIsDemo);
  const streamEnabled = useAuthStore((state) => Boolean(state.token) && state.isAuthenticated());
  const [days, setDays] = useState<Days>(14);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [summary, setSummary] = useState<DashboardSummary | null>(null);
  const [outbox, setOutbox] = useState<AuditOutboxStatus | null>(null);
  const [outboxError, setOutboxError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [eventRefreshing, setEventRefreshing] = useState(false);
  const [refreshToken, setRefreshToken] = useState(0);
  const [fullscreen, setFullscreen] = useState(false);
  const [liveEvents, setLiveEvents] = useState<AuditStreamEvent[]>([]);
  const [realtimeDenyIDs, setRealtimeDenyIDs] = useState<ReadonlySet<number>>(() => new Set());
  const dashboardRef = useRef<HTMLDivElement>(null);
  const summaryControllerRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);
  const summaryRef = useRef<DashboardSummary | null>(null);
  const seenStreamIDsRef = useRef<Set<number>>(new Set());
  const connectionGateRef = useRef(Number.POSITIVE_INFINITY);
  const windowGenerationRef = useRef(0);
  const aggregateSequenceRef = useRef(0);
  const aggregateJournalRef = useRef<AppliedStreamEvent[]>([]);

  const handleHello = useCallback(() => {
    connectionGateRef.current = Date.now();
  }, []);

  const handleAudit = useCallback((event: AuditStreamEvent) => {
    if (!mountedRef.current || seenStreamIDsRef.current.has(event.id)) return;
    seenStreamIDsRef.current.add(event.id);
    setLiveEvents((current) => [event, ...current].sort(compareStreamEvents).slice(0, 100));

    const eventTime = Date.parse(event.ts);
    if (!Number.isFinite(eventTime) || eventTime < connectionGateRef.current || !knownDecision(event.decision)) return;

    const generation = windowGenerationRef.current;
    const seq = aggregateSequenceRef.current + 1;
    aggregateSequenceRef.current = seq;
    aggregateJournalRef.current.push({ seq, generation, event });
    setSummary((current) => {
      if (!current || generation !== windowGenerationRef.current) return current;
      const next = incrementSummary(current, event);
      summaryRef.current = next;
      return next;
    });

    if (knownDecision(event.decision) === "deny") {
      setRealtimeDenyIDs((current) => {
        const next = new Set(current);
        next.add(event.id);
        while (next.size > 100) {
          const oldest = next.values().next().value as number | undefined;
          if (oldest === undefined) break;
          next.delete(oldest);
        }
        return next;
      });
    }
  }, []);

  const { status: streamStatus, reconnectNow } = useEventStream({
    enabled: streamEnabled,
    onAudit: handleAudit,
    onHello: handleHello,
  });
  const pollEnabled = autoRefresh || streamStatus === "polling-fallback";

  const loadSummary = useCallback(async () => {
    if (!mountedRef.current) return;
    summaryControllerRef.current?.abort();
    const controller = new AbortController();
    summaryControllerRef.current = controller;
    const requestGeneration = windowGenerationRef.current;
    const cut = aggregateSequenceRef.current;
    setRefreshing(true);
    try {
      const next = await getDashboardSummary(days, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || requestGeneration !== windowGenerationRef.current) return;
      const reconciled = aggregateJournalRef.current
        .filter((item) => item.generation === requestGeneration && item.seq > cut)
        .reduce((current, item) => incrementSummary(current, item.event), next);
      aggregateJournalRef.current = aggregateJournalRef.current.filter(
        (item) => item.generation === requestGeneration && item.seq > cut,
      );
      summaryRef.current = reconciled;
      setSummary(reconciled);
      setError(false);
    } catch {
      if (!controller.signal.aborted && mountedRef.current && requestGeneration === windowGenerationRef.current) {
        setError(true);
        if (summaryRef.current) void message.warning("总览数据刷新失败，已保留上次结果");
      }
    } finally {
      if (summaryControllerRef.current === controller) {
        summaryControllerRef.current = null;
      }
      if (summaryControllerRef.current === null && mountedRef.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [days]);

  const loadOutbox = useCallback(async () => {
    try {
      const status = await getAuditOutboxStatus();
      if (mountedRef.current) {
        setOutbox(status);
        setOutboxError(false);
      }
    } catch {
      if (mountedRef.current) setOutboxError(true);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      summaryControllerRef.current?.abort();
      summaryControllerRef.current = null;
    };
  }, []);

  useEffect(() => {
    void loadSummary();
  }, [loadSummary]);

  useEffect(() => {
    void loadOutbox();
    const timer = window.setInterval(() => void loadOutbox(), 30_000);
    return () => window.clearInterval(timer);
  }, [loadOutbox]);

  useEffect(() => {
    if (!pollEnabled) return;
    const timer = window.setInterval(() => void loadSummary(), 30_000);
    return () => window.clearInterval(timer);
  }, [loadSummary, pollEnabled]);

  const previousPollEnabledRef = useRef(pollEnabled);
  useEffect(() => {
    if (pollEnabled && !previousPollEnabledRef.current) {
      void loadSummary();
    }
    previousPollEnabledRef.current = pollEnabled;
  }, [loadSummary, pollEnabled]);

  useEffect(() => {
    const handleFullscreen = () => setFullscreen(Boolean(document.fullscreenElement));
    document.addEventListener("fullscreenchange", handleFullscreen);
    return () => document.removeEventListener("fullscreenchange", handleFullscreen);
  }, []);

  const toggleFullscreen = async () => {
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else await dashboardRef.current?.requestFullscreen();
    } catch {
      void message.error("当前浏览器不支持全屏模式");
    }
  };

  const handleManualRefresh = async () => {
    reconnectNow();
    setRefreshToken((value) => value + 1);
    await Promise.all([loadSummary(), loadOutbox()]);
  };

  const handleDaysChange = (nextDays: Days) => {
    if (nextDays === days) return;
    windowGenerationRef.current += 1;
    aggregateJournalRef.current = [];
    summaryControllerRef.current?.abort();
    setDays(nextDays);
  };

  const kpi = summary?.kpi || emptyKPI;
  const kpiItems = useMemo<KPIItem[]>(() => [
    { label: "总请求", value: finiteNumber(kpi.total_requests), icon: <FileSearchOutlined />, color: palette[mode].brand, change: kpi.total_requests_change_pct },
    { label: "拦截", value: finiteNumber(kpi.blocked), icon: <WarningOutlined />, color: palette.semantic.deny, change: kpi.blocked_change_pct },
    { label: "待审批", value: finiteNumber(kpi.pending_approvals), icon: <AuditOutlined />, color: palette.semantic.approve },
    { label: "活跃 Agent", value: finiteNumber(kpi.active_agents), icon: <RobotOutlined />, color: palette[mode].brand },
    { label: "已配置数据源", value: finiteNumber(kpi.datasources_total), icon: <DatabaseOutlined />, color: palette[mode].brand },
  ], [kpi.active_agents, kpi.blocked, kpi.blocked_change_pct, kpi.datasources_total, kpi.pending_approvals, kpi.total_requests, kpi.total_requests_change_pct, mode]);

  const pageExtra = (
    <Space wrap className="overview-controls">
      <Segmented<Days> value={days} options={[{ label: "近7天", value: 7 }, { label: "近14天", value: 14 }, { label: "近30天", value: 30 }]} onChange={handleDaysChange} />
      <span className="refresh-switch"><Switch size="small" checked={autoRefresh} onChange={setAutoRefresh} /><span>自动刷新</span></span>
      <StreamStatusBadge status={streamStatus} />
      <Tooltip title="刷新总览与风险事件"><Button icon={<ReloadOutlined />} loading={refreshing || eventRefreshing} onClick={() => void handleManualRefresh()}>立即刷新</Button></Tooltip>
      <Tooltip title={fullscreen ? "退出全屏" : "进入全屏"}><Button icon={fullscreen ? <FullscreenExitOutlined /> : <FullscreenOutlined />} onClick={() => void toggleFullscreen()} aria-label="切换全屏" /></Tooltip>
    </Space>
  );

  return (
    <div className="overview-dashboard" ref={dashboardRef}>
      <PageContainer title="总览" subtitle="数据库访问安全态势" extra={pageExtra}>
        {demoEnabled ? <DemoReadiness refreshToken={refreshToken} /> : null}
        {outboxError ? <Alert type="error" showIcon message="管理审计投递状态不可用" description="请检查元数据库连接和管理审计投递进程；当前无法确认积压情况。" style={{ marginBottom: 16 }} /> : null}
        {!outboxError && outbox?.enabled ? <Alert
          type={outbox.state === "critical" ? "error" : outbox.state === "lagging" ? "warning" : "success"}
          showIcon
          message={`管理审计投递：${outbox.state === "critical" ? "严重积压" : outbox.state === "lagging" ? "投递延迟" : "正常"}`}
          description={`当前租户待投递 ${outbox.pending} 条，重试中 ${outbox.retrying} 条，最早待投递已等待 ${outbox.oldest_age_seconds} 秒。`}
          style={{ marginBottom: 16 }}
        /> : null}
        {loading && !summary ? <Card className="overview-loading" bordered><Skeleton active paragraph={{ rows: 8 }} /></Card> : error && !summary ? (
          <Alert type="error" showIcon message="总览数据加载失败" description="请检查管理 API 连接后重试。" action={<Button onClick={() => void loadSummary()}>重试</Button>} />
        ) : (
          <>
            <Row gutter={[16, 16]} className="overview-kpi-row">{kpiItems.map((item) => <Col xs={24} sm={12} md={8} lg={6} flex="1 1 210px" key={item.label}><KPICard item={item} /></Col>)}</Row>
            <Row gutter={[16, 16]} className="overview-section-row"><Col xs={24} xl={16}><TrendPanel data={summary?.trend_14d || []} mode={mode} /></Col><Col xs={24} xl={8}><DecisionPanel data={summary?.decision_distribution || []} mode={mode} /></Col></Row>
            <Row gutter={[16, 16]} className="overview-section-row"><Col xs={24} xl={16}><Card className="overview-panel event-panel" bordered><EventStream liveEvents={liveEvents} streamStatus={streamStatus} pollEnabled={pollEnabled} realtimeDenyIDs={realtimeDenyIDs} refreshToken={refreshToken} onRefreshingChange={setEventRefreshing} /></Card></Col><Col xs={24} xl={8}><BattlePanel summary={summary} /></Col></Row>
          </>
        )}
      </PageContainer>
    </div>
  );
}
