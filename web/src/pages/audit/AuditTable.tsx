import { EyeOutlined } from "@ant-design/icons";
import { Button, Empty, Table, Tag, Tooltip } from "antd";
import type { TableProps } from "antd";
import { useEffect, useMemo, useRef, useState } from "react";

import type { AuditView } from "@/api/types";
import { decisionMeta, getDecisionMeta, statementLabel } from "@/constants/labels";
import { palette } from "@/theme/tokens";

interface AuditTableProps {
  list: AuditView[];
  loading: boolean;
  failed: boolean;
  onView: (record: AuditView) => void;
  onRetry: () => void;
}

function displayText(value: string | null | undefined): string {
  return value?.trim() || "—";
}

function shortText(value: string | null | undefined, max = 12): string {
  const text = displayText(value);
  return text.length > max ? `${text.slice(0, max)}…` : text;
}

function numberText(value: number | null | undefined): string {
  return value !== null && value !== undefined && Number.isFinite(value) ? value.toLocaleString("zh-CN") : "—";
}

function timeText(value: string | null | undefined): string {
  const timestamp = Date.parse(value || "");
  if (!Number.isFinite(timestamp)) return "—";
  const date = new Date(timestamp);
  const padded = (part: number, size = 2) => String(part).padStart(size, "0");
  return `${padded(date.getMonth() + 1)}-${padded(date.getDate())} ${padded(date.getHours())}:${padded(date.getMinutes())}:${padded(date.getSeconds())}.${padded(date.getMilliseconds(), 3)}`;
}

function decisionTag(value: string | null | undefined) {
  const raw = value?.trim() || "未知";
  const normalized = raw.toLowerCase();
  if (Object.prototype.hasOwnProperty.call(decisionMeta, normalized)) {
    const meta = getDecisionMeta(normalized);
    return <Tag color={meta.tagColor}>{meta.label}</Tag>;
  }
  return <Tag className="audit-decision-unknown">{raw}</Tag>;
}

function riskColor(risk: number): string {
  // 风险等级越高越暖越红：1-2 低(绿) / 3 中(黄) / 4 高(橙) / 5 严重(红)
  if (risk >= 5) return palette.semantic.deny;
  if (risk === 4) return palette.semantic.approve;
  if (risk === 3) return palette.semantic.warn;
  return palette.semantic.allow;
}

export function AuditTable({ list, loading, failed, onView, onRetry }: AuditTableProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [scrollY, setScrollY] = useState(420);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const resize = () => {
      const top = container.getBoundingClientRect().top;
      setScrollY(Math.max(320, Math.floor(window.innerHeight - top - 176)));
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(container);
    window.addEventListener("resize", resize);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", resize);
    };
  }, []);

  const columns = useMemo<TableProps<AuditView>["columns"]>(() => [
    { title: "时间", dataIndex: "ts", width: 150, fixed: "left", render: (value: string | null | undefined) => <Tooltip title={displayText(value)}><span className="mono-text">{timeText(value)}</span></Tooltip> },
    { title: "Agent", dataIndex: "agent_id", width: 130, ellipsis: true, render: (value: string | null | undefined) => <Tooltip title={displayText(value)}>{shortText(value)}</Tooltip> },
    { title: "数据源", dataIndex: "datasource_id", width: 130, ellipsis: true, render: (value: string | null | undefined) => <Tooltip title={displayText(value)}>{shortText(value)}</Tooltip> },
    { title: "类型", dataIndex: "stmt_type", width: 100, render: (value: string | null | undefined) => <Tag>{statementLabel(value)}</Tag> },
    { title: "决策", dataIndex: "decision", width: 92, render: (value: string | null | undefined) => decisionTag(value) },
    {
      title: "风险",
      dataIndex: "risk_level",
      width: 90,
      render: (value: number | null) => value !== null && value !== undefined && Number.isFinite(value) && value >= 1 && value <= 5
        ? <span className="audit-risk"><i style={{ background: riskColor(value) }} />{value}</span>
        : "—",
    },
    {
      title: "SQL 摘要",
      dataIndex: "sql_raw",
      width: 330,
      ellipsis: true,
      render: (value: string | null | undefined, record: AuditView) => <Tooltip title={displayText(value)}><code className={`audit-table-sql${["deny", "error"].includes((record.decision || "").toLowerCase()) ? " audit-table-sql-risk" : ""}`}>{displayText(value).replace(/\s+/g, " ")}</code></Tooltip>,
    },
    { title: "预估行", dataIndex: "est_rows", width: 105, align: "right", render: numberText },
    { title: "行数", dataIndex: "rows_returned", width: 90, align: "right", render: numberText },
    { title: "耗时", dataIndex: "latency_ms", width: 100, align: "right", render: (value: number | null) => value !== null && value !== undefined && Number.isFinite(value) ? `${value.toLocaleString("zh-CN")} ms` : "—" },
    { title: "会话", dataIndex: "session_id", width: 120, ellipsis: true, render: (value: string | null | undefined) => <Tooltip title={displayText(value)}><span className="mono-text">{shortText(value, 10)}</span></Tooltip> },
    { title: "操作", key: "action", width: 76, fixed: "right", render: (_value: unknown, record: AuditView) => <Button type="link" size="small" icon={<EyeOutlined />} onClick={() => onView(record)}>查看</Button> },
  ], [onView]);

  return (
    <section className="audit-table-wrap" ref={containerRef}>
      <Table<AuditView>
        rowKey="id"
        columns={columns}
        dataSource={list}
        loading={loading}
        pagination={false}
        virtual
        size="middle"
        scroll={{ x: 1_620, y: scrollY }}
        locale={{
          emptyText: failed
            ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<span>审计记录加载失败 <Button type="link" onClick={onRetry}>重试</Button></span>} />
            : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无审计记录，请接入 Agent 或调整筛选条件" />,
        }}
      />
    </section>
  );
}
