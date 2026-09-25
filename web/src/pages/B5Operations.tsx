import {
  CheckCircleOutlined,
  ClockCircleOutlined,
  ExclamationCircleOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Drawer,
  Empty,
  Input,
  Modal,
  Progress,
  Row,
  Segmented,
  Select,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { useCallback, useEffect, useMemo, useState } from "react";

import {
  confirmB5Discard,
  getB5Metrics,
  getB5Quarantine,
  getB5Reconciliation,
  getB5Session,
  getB5Sessions,
  getB5Status,
  getB5Transaction,
  getB5Transactions,
  reconcileB5,
  type B5Metrics,
  type B5Quarantine,
  type B5Reconciliation,
  type B5Session,
  type B5Transaction,
} from "@/api/b5";
import { PageContainer } from "@/components/PageContainer";
import { useThemeStore } from "@/theme/useThemeStore";

import { B5StateDonut } from "./b5/B5StateDonut";

const emptyMetrics: B5Metrics = {
  sessions: 0, transactions: 0, unknown: 0, discard_unconfirmed: 0,
  quarantine_count: 0, quarantine_charged_slots: 0, hard_budget: 0,
  quarantine_budget_ratio: 0, session_states: [], transaction_states: [], phase_durations: [],
};

function when(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? "—" : parsed.toLocaleString("zh-CN", { hour12: false });
}

function short(value?: string): string {
  if (!value) return "—";
  return value.length > 18 ? `${value.slice(0, 9)}…${value.slice(-7)}` : value;
}

export function stateColor(state: string): string {
  if (["ACTIVE", "READY", "COMMITTED", "PRIMARY_DURABLE"].includes(state)) return "success";
  if (["UNKNOWN", "DISCARD_UNCONFIRMED", "QUARANTINED", "COMMITTED_AUDIT_PENDING"].includes(state)) return "warning";
  if (["TERMINAL", "EXPIRED", "NOT_COMMITTED"].includes(state)) return "default";
  return "processing";
}

export function B5Operations() {
	const themeMode = useThemeStore((state) => state.mode);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [mode, setMode] = useState<"sessions" | "transactions">("transactions");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const [status, setStatus] = useState<string>();
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(false);
  const [sessions, setSessions] = useState<B5Session[]>([]);
  const [transactions, setTransactions] = useState<B5Transaction[]>([]);
  const [total, setTotal] = useState(0);
  const [metrics, setMetrics] = useState(emptyMetrics);
  const [quarantine, setQuarantine] = useState<B5Quarantine[]>([]);
  const [reconciliation, setReconciliation] = useState<B5Reconciliation[]>([]);
  const [detail, setDetail] = useState<B5Session | B5Transaction>();

  const refresh = useCallback(async () => {
    const capability = await getB5Status();
    setEnabled(capability.enabled);
    if (!capability.enabled) return;
    setLoading(true);
    try {
      const filters = { page, page_size: pageSize, status, q: query || undefined };
      const [listed, metricData, quarantineData, reconciliationData] = await Promise.all([
        mode === "sessions" ? getB5Sessions(filters) : getB5Transactions(filters),
        getB5Metrics(),
        getB5Quarantine({ page: 1, page_size: 20 }),
        getB5Reconciliation({ page: 1, page_size: 20 }),
      ]);
      if (mode === "sessions") setSessions(listed.list as B5Session[]);
      else setTransactions(listed.list as B5Transaction[]);
      setTotal(listed.total);
      setMetrics(metricData);
      setQuarantine(quarantineData.list);
      setReconciliation(reconciliationData.list);
    } finally {
      setLoading(false);
    }
  }, [mode, page, pageSize, query, status]);

  useEffect(() => { void refresh(); }, [refresh]);

  const openSession = async (record: B5Session) => setDetail(await getB5Session(record.id));
  const openTransaction = async (record: B5Transaction) => setDetail(await getB5Transaction(record.id));
  const runReconcile = (transactionId?: string) => {
    Modal.confirm({
      title: "确认触发对账？",
      content: transactionId ? `仅对账事务 ${transactionId}` : "对账所有待处理 WAL/审计记录。该操作会写入管理审计。",
      okText: "确认对账", cancelText: "取消",
      onOk: async () => { await reconcileB5(transactionId); void message.success("对账任务已完成"); await refresh(); },
    });
  };
  const confirmDiscard = (record: B5Quarantine) => {
    Modal.confirm({
      title: "确认 backend 已不存在？",
      content: "仅在独立 inventory 已证明 backend absence 后执行。错误确认会破坏容量与连接隔离。",
      okText: "确认隔离处置", okButtonProps: { danger: true }, cancelText: "取消",
      onOk: async () => { await confirmB5Discard(record.lease_id); void message.success("隔离项已处置"); await refresh(); },
    });
  };

  const sessionColumns: ColumnsType<B5Session> = useMemo(() => [
    { title: "会话", dataIndex: "id", render: (value: string) => <Typography.Text code>{short(value)}</Typography.Text> },
    { title: "状态", dataIndex: "status", render: (value: string) => <Tag color={stateColor(value)}>{value}</Tag> },
    { title: "Owner / Epoch", render: (_, row) => <span>{short(row.owner_instance_id)} · {row.owner_epoch}</span> },
    { title: "Sticky", dataIndex: "sticky_route", ellipsis: true },
    { title: "Idle TTL", dataIndex: "idle_expires_at", render: when },
    { title: "操作", render: (_, row) => <Button type="link" onClick={() => void openSession(row)}>详情</Button> },
  ], []);
  const transactionColumns: ColumnsType<B5Transaction> = useMemo(() => [
    { title: "事务", dataIndex: "id", render: (value: string) => <Typography.Text code>{short(value)}</Typography.Text> },
    { title: "状态", dataIndex: "status", render: (value: string) => <Tag color={stateColor(value)}>{value}</Tag> },
    { title: "阶段", dataIndex: "phase", render: (value: string) => <Tag>{value}</Tag> },
    { title: "数据源", dataIndex: "datasource_id" },
    { title: "序列", dataIndex: "sequence" },
    { title: "Wall deadline", dataIndex: "wall_deadline", render: when },
    { title: "操作", render: (_, row) => <Space><Button type="link" onClick={() => void openTransaction(row)}>详情</Button>{["UNKNOWN", "TERMINAL"].includes(row.status) ? <Button type="link" onClick={() => runReconcile(row.id)}>对账</Button> : null}</Space> },
  ], []);

  if (enabled === false) {
    return <PageContainer title="会话/事务" subtitle="B5 跨请求事务运维面"><Alert type="info" showIcon message="B5 会话功能未启用" description="b5_sessions 保持关闭；入口已从导航隐藏，当前页面不读取或处置任何 B5 数据。" /></PageContainer>;
  }

  return (
    <PageContainer title="会话/事务" subtitle="B5 会话、事务、隔离容量与审计对账">
      <Space direction="vertical" size={16} className="b5-page">
        <Row gutter={[12, 12]}>
          <Col xs={12} lg={4}><Card className="b5-kpi"><Statistic title="会话" value={metrics.sessions} prefix={<CheckCircleOutlined />} /></Card></Col>
          <Col xs={12} lg={4}><Card className="b5-kpi"><Statistic title="事务" value={metrics.transactions} prefix={<ClockCircleOutlined />} /></Card></Col>
          <Col xs={12} lg={4}><Card className="b5-kpi b5-kpi-warn"><Statistic title="UNKNOWN" value={metrics.unknown} prefix={<ExclamationCircleOutlined />} /></Card></Col>
          <Col xs={12} lg={4}><Card className="b5-kpi b5-kpi-warn"><Statistic title="未确认丢弃" value={metrics.discard_unconfirmed} /></Card></Col>
          <Col xs={24} lg={8}><Card className="b5-kpi"><div className="b5-budget"><span>Quarantine 预算占比</span><strong>{(metrics.quarantine_budget_ratio * 100).toFixed(2)}%</strong></div><Progress percent={Math.min(100, metrics.quarantine_budget_ratio * 100)} showInfo={false} strokeColor="#f59e0b" trailColor="rgba(148,163,184,.18)" /><small>{metrics.quarantine_charged_slots} / {metrics.hard_budget || 0} slots</small></Card></Col>
        </Row>

        <Card className="b5-panel" title="运行记录" extra={<Button icon={<ReloadOutlined />} onClick={() => void refresh()}>刷新</Button>}>
          <div className="b5-toolbar">
            <Segmented value={mode} options={[{ label: "事务", value: "transactions" }, { label: "会话", value: "sessions" }]} onChange={(value) => { setMode(value as typeof mode); setPage(1); }} />
            <Input.Search allowClear placeholder="ID / owner / 数据源" value={query} onChange={(event) => setQuery(event.target.value)} onSearch={() => { setPage(1); void refresh(); }} />
            <Select allowClear placeholder="状态" value={status} onChange={(value) => { setStatus(value); setPage(1); }} options={["ACTIVE", "READY", "ROLLBACK_ONLY", "TERMINAL", "UNKNOWN"].map((value) => ({ value, label: value }))} />
          </div>
          {mode === "sessions" ? <Table rowKey="id" loading={loading} columns={sessionColumns} dataSource={sessions} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (next, size) => { setPage(next); setPageSize(size); } }} /> : <Table rowKey="id" loading={loading} columns={transactionColumns} dataSource={transactions} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (next, size) => { setPage(next); setPageSize(size); } }} />}
        </Card>

        <Row gutter={[16, 16]}>
          <Col xs={24} lg={12}><Card className="b5-panel" title="会话状态分布"><B5StateDonut data={metrics.session_states} mode={themeMode} label="会话状态分布" /></Card></Col>
          <Col xs={24} lg={12}><Card className="b5-panel" title="事务状态分布"><B5StateDonut data={metrics.transaction_states} mode={themeMode} label="事务状态分布" /></Card></Col>
        </Row>

        <Row gutter={[16, 16]}>
          <Col xs={24} xl={12}><Card className="b5-panel" title={`Quarantine · ${quarantine.length}`}><Table size="small" rowKey="lease_id" locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="无隔离项" /> }} pagination={false} dataSource={quarantine} columns={[
            { title: "Lease", dataIndex: "lease_id", render: short }, { title: "原因 / 年龄", render: (_, row) => <><Tag color="warning">{row.reason}</Tag><span>{row.age_seconds}s</span></> }, { title: "Backend", render: (_, row) => `${row.backend.database || "—"} / ${row.backend.pid || "—"}` }, { title: "操作", render: (_, row) => <Button danger size="small" onClick={() => confirmDiscard(row)}>确认 discard</Button> },
          ]} /></Card></Col>
          <Col xs={24} xl={12}><Card className="b5-panel" title="对账结果" extra={<Button onClick={() => runReconcile()}>触发对账</Button>}><Table size="small" rowKey="event_uuid" pagination={false} dataSource={reconciliation} columns={[
            { title: "事务", dataIndex: "transaction_id", render: short }, { title: "分类", dataIndex: "classification", render: (value: string) => <Tag color={stateColor(value)}>{value}</Tag> }, { title: "Append", dataIndex: "append_confirmation" }, { title: "更新时间", dataIndex: "updated_at", render: when },
          ]} /></Card></Col>
        </Row>
      </Space>

      <Drawer width={680} title={detail && "phase" in detail ? "事务详情" : "会话详情"} open={Boolean(detail)} onClose={() => setDetail(undefined)}>
        {detail ? <B5Detail value={detail} /> : null}
      </Drawer>
    </PageContainer>
  );
}

function B5Detail({ value }: { value: B5Session | B5Transaction }) {
  if ("phase" in value) {
    return <Space direction="vertical" size={18} className="b5-detail"><Descriptions bordered size="small" column={1} items={[
      { key: "id", label: "事务 ID", children: value.id }, { key: "session", label: "会话", children: value.session_id }, { key: "state", label: "状态 / 阶段", children: <Space><Tag color={stateColor(value.status)}>{value.status}</Tag><Tag>{value.phase}</Tag></Space> }, { key: "plan", label: "Plan digest", children: <Typography.Text copyable code>{value.plan_digest}</Typography.Text> }, { key: "approval", label: "审批", children: value.approval_id || "不需要" }, { key: "deadline", label: "截止时间", children: when(value.wall_deadline) }, { key: "backend", label: "Backend identity", children: value.backend_pid ? `PID ${value.backend_pid} · ${when(value.backend_started_at)}` : "未绑定" }, { key: "chain", label: "序列 / digest chain", children: `${value.sequence} · ${short(value.previous_event_digest)}` },
    ]} /><Typography.Title level={5}>事件链</Typography.Title>{value.events?.length ? <Table size="small" rowKey="sequence" pagination={false} dataSource={value.events} columns={[{ title: "Seq", dataIndex: "sequence" }, { title: "事件", dataIndex: "type" }, { title: "Digest", dataIndex: "digest", render: short }, { title: "时间", dataIndex: "created_at", render: when }]} /> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无事件" />}</Space>;
  }
  return <Descriptions bordered size="small" column={1} items={[
    { key: "id", label: "会话 ID", children: value.id }, { key: "state", label: "状态", children: <Tag color={stateColor(value.status)}>{value.status}</Tag> }, { key: "agent", label: "Agent / Principal", children: `${value.agent_id} / ${value.principal_id}` }, { key: "owner", label: "Owner epoch", children: `${value.owner_instance_id} / ${value.owner_epoch}` }, { key: "sticky", label: "Sticky route", children: value.sticky_route }, { key: "idle", label: "Idle TTL", children: when(value.idle_expires_at) }, { key: "wall", label: "Absolute TTL", children: when(value.absolute_expires_at) },
  ]} />;
}
