import {
  AuditOutlined,
  CheckCircleFilled,
  CloseCircleFilled,
  DashboardOutlined,
  EyeInvisibleOutlined,
  StopFilled,
  WarningFilled,
} from "@ant-design/icons";
import { Alert, Button, Empty, Table, Tag } from "antd";
import type { TableProps } from "antd";
import { useMemo } from "react";
import { useNavigate } from "react-router-dom";

import type { PlaygroundRunHitView, PlaygroundRunResponse } from "@/api/types";
import { normalizeErrorStage } from "@/components/stageflow/adaptAssessment";
import { getErrorCodeMeta } from "@/constants/errorCodes";
import { getDecisionMeta, statementLabel } from "@/constants/labels";
import { safeBackendMessage } from "@/utils/safeBackendMessage";

interface LiveResultTableProps {
  result: PlaygroundRunResponse;
}

interface LiveTableRow {
  key: string;
  cells: string[];
}

function finiteNumber(value: number | null | undefined, fallback = 0): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function validAuditID(value: number | null | undefined): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : null;
}

function safeHits(value: PlaygroundRunHitView[] | null | undefined): PlaygroundRunHitView[] {
  return Array.isArray(value) ? value : [];
}

const ruleLabels: Readonly<Record<string, string>> = {
  R002: "写操作缺少 WHERE 条件",
  R005: "大结果集或全表扫描风险",
  R010: "对象访问未获授权",
  DEMO_NON_SELECT: "演示通道禁止非只读语句",
  DEMO_WRITE_APPROVAL: "写操作进入人工审批",
  DEMO_PARSE: "演示语句无法安全解析",
};

function displayCell(column: string, index: number, value: string, touched: Record<string, string>): string {
  const sensitiveColumn = ["phone", "email"].includes(column.trim().toLowerCase()) || String(index) in touched;
  if (sensitiveColumn && !value.includes("*")) return "[已脱敏]";
  return value;
}

export function LiveResultTable({ result }: LiveResultTableProps) {
  const navigate = useNavigate();
  const responseDecision = (result.decision || "").trim().toLowerCase();
  const assessmentDecision = (result.assessment?.decision || "").trim().toLowerCase();
  const decision = responseDecision === "error" || assessmentDecision === "error"
    ? "error"
    : responseDecision === "deny" || assessmentDecision === "deny"
      ? "deny"
      : responseDecision || assessmentDecision;
  const decisionInfo = getDecisionMeta(decision);
  const errorCode = getErrorCodeMeta(result.error_code);
  const errorMessage = safeBackendMessage(result.error_message)
    || safeBackendMessage(result.assessment?.reason)
    || "请求执行失败";
  const errorSuggestion = safeBackendMessage(result.suggestion);
  const columns = Array.isArray(result.result?.columns) ? result.result.columns : [];
  const rows = Array.isArray(result.result?.rows) ? result.result.rows : [];
  const hits = safeHits(result.assessment?.hits);
  const auditID = validAuditID(result.audit_id);
  const touchedColumnMap = result.redact?.touched_columns && typeof result.redact.touched_columns === "object"
    ? result.redact.touched_columns
    : {};
  const touchedColumns = Object.entries(touchedColumnMap);

  const tableColumns = useMemo<TableProps<LiveTableRow>["columns"]>(() => columns.map((column, index) => ({
    title: column?.trim() || `第 ${index + 1} 列`,
    key: `column-${index}`,
    ellipsis: true,
    render: (_value: unknown, record: LiveTableRow) => (
      <span className="mono-text">{displayCell(column, index, record.cells[index] ?? "", touchedColumnMap)}</span>
    ),
  })), [columns, touchedColumnMap]);

  const tableRows = useMemo<LiveTableRow[]>(() => rows.map((row, rowIndex) => ({
    key: `row-${rowIndex}`,
    cells: Array.isArray(row) ? row.map((value) => typeof value === "string" ? value : String(value ?? "")) : [],
  })), [rows]);

  const displayJSON = useMemo(() => JSON.stringify({
    ...result,
    error_code: errorCode?.code,
    error_stage: normalizeErrorStage(result.error_stage),
    error_message: safeBackendMessage(result.error_message) || undefined,
    suggestion: safeBackendMessage(result.suggestion) || undefined,
    assessment: {
      ...result.assessment,
      reason: safeBackendMessage(result.assessment?.reason) || undefined,
      suggestion: safeBackendMessage(result.assessment?.suggestion) || undefined,
      hits: safeHits(result.assessment?.hits).map((hit) => ({
        ...hit,
        message: safeBackendMessage(hit.message) || undefined,
        suggestion: safeBackendMessage(hit.suggestion) || undefined,
      })),
    },
    result: {
      ...result.result,
      columns,
      rows: rows.map((row) => Array.isArray(row)
        ? row.map((value, index) => displayCell(columns[index] ?? "", index, typeof value === "string" ? value : String(value ?? ""), touchedColumnMap))
        : []),
    },
  }, null, 2), [columns, errorCode?.code, result, rows, touchedColumnMap]);

  const decisionIcon = decision === "allow"
    ? <CheckCircleFilled />
    : decision === "warn" || decision === "approve"
      ? <WarningFilled />
      : decision === "error"
        ? <CloseCircleFilled />
        : <StopFilled />;

  return (
    <div className={`live-result live-result-${decision || "unknown"}`}>
      <div className="live-result-summary">
        <span className="live-result-decision">{decisionIcon}<Tag color={decisionInfo.tagColor}>{decisionInfo.label}</Tag></span>
        <Tag>{statementLabel(result.assessment?.stmt_type)}</Tag>
        <span>风险 <strong>{finiteNumber(result.assessment?.risk)}</strong></span>
        <span>预估扫描 <strong>{finiteNumber(result.assessment?.est_scan_rows).toLocaleString("zh-CN")}</strong> 行</span>
        {decision !== "error" ? <span>执行耗时 <strong>{finiteNumber(result.result?.latency_ms).toLocaleString("zh-CN")}</strong> ms</span> : null}
        <span className="live-result-audit mono-text">audit_id：{auditID ?? "—"}</span>
        <Button
          size="small"
          icon={<AuditOutlined />}
          disabled={auditID === null}
          onClick={() => {
            if (auditID !== null) navigate(`/audit?focus=${auditID}`);
          }}
        >
          查看该审计
        </Button>
        <Button size="small" icon={<DashboardOutlined />} onClick={() => navigate("/")}>去大屏回看</Button>
      </div>

      {hits.length > 0 ? (
        <section className="live-hit-section" aria-label="命中规则">
          <strong>命中规则</strong>
          <div className="live-hit-list">
            {hits.map((hit, index) => (
              <article className="live-hit" key={`${hit.rule_id || "rule"}-${index}`}>
                <Tag color={decision === "deny" ? "error" : "warning"}>{hit.rule_id || "未命名规则"}</Tag>
                <div>
                  <strong>{ruleLabels[hit.rule_id] || "安全规则命中"}</strong>
                  <p>{hit.message?.trim() || "本次请求命中了网关安全规则。"}</p>
                  <p className="live-hit-suggestion">建议：{hit.suggestion?.trim() || "请根据规则要求调整 SQL 后重试。"}</p>
                </div>
              </article>
            ))}
          </div>
        </section>
      ) : null}

      {decision === "error" ? (
        <section className="live-error-panel" aria-label="数据库或执行阶段出错">
          <Alert
            type="error"
            showIcon
            message={(
              <span className="live-error-heading">
                <strong>执行出错</strong>
                {errorCode ? (
                  <Tag color="volcano">
                    {errorCode.label} <code>{errorCode.code}</code>
                  </Tag>
                ) : null}
              </span>
            )}
            description={(
              <div className="live-error-description">
                <span>{errorMessage}</span>
                {errorSuggestion ? <span>建议：{errorSuggestion}</span> : null}
              </div>
            )}
          />
        </section>
      ) : decision === "deny" ? (
        <section className="live-deny-panel" aria-label="请求已被安全网关拦截">
          <Alert
            type="error"
            showIcon
            message="请求已在触达数据库前被拦截"
            description="仅展示安全网关判词；数据库未执行该语句，也没有返回业务数据。"
          />
        </section>
      ) : decision === "approve" ? (
        <section className="live-approve-panel" aria-label="请求已进入人工审批">
          <Alert
            type="warning"
            showIcon
            message="请求已进入 DBA 人工审批"
            description={result.assessment?.reason?.trim() || "审批完成前不会执行该语句；可通过 audit_id 回看本次请求。"}
          />
        </section>
      ) : (
        <>
          {decision === "warn" ? (
            <Alert
              className="live-warning"
              type="warning"
              showIcon
              message={result.assessment?.reason?.trim() || "查询已放行，但命中了风险告警"}
              description={hits.map((hit) => `${hit.rule_id}: ${hit.message}`).filter(Boolean).join("；") || undefined}
            />
          ) : null}

          {result.result?.truncated ? (
            <Alert
              className="live-truncated"
              type="warning"
              showIcon
              message={`结果超过上限，仅显示前 ${tableRows.length} 行`}
            />
          ) : null}

          <div className="live-table-meta">
            <span>返回行数：<strong>{finiteNumber(result.result?.row_count, tableRows.length).toLocaleString("zh-CN")}</strong></span>
            <span>当前展示：<strong>{tableRows.length.toLocaleString("zh-CN")}</strong> 行</span>
            <span>耗时：<strong>{finiteNumber(result.result?.latency_ms).toLocaleString("zh-CN")}</strong> ms</span>
          </div>

          {columns.length > 0 && tableRows.length > 0 ? (
            <Table<LiveTableRow>
              className="live-data-table"
              rowKey="key"
              columns={tableColumns}
              dataSource={tableRows}
              pagination={false}
              size="small"
              scroll={{ x: "max-content" }}
              locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="无返回数据（语句被拦截或无结果集）" /> }}
            />
          ) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="无返回数据（语句被拦截或无结果集）" />}

          <section className="live-redact-panel">
            <div><EyeInvisibleOutlined /><strong>脱敏信息</strong></div>
            <span>已掩码单元格：<strong>{finiteNumber(result.redact?.masked_cells).toLocaleString("zh-CN")}</strong></span>
            <div className="live-redact-columns">
              {touchedColumns.length > 0
                ? touchedColumns.map(([columnIndex, sensitiveType]) => {
                  const parsedIndex = Number(columnIndex);
                  const columnName = Number.isInteger(parsedIndex) && parsedIndex >= 0
                    ? columns[parsedIndex]?.trim() || `第 ${parsedIndex + 1} 列`
                    : columnIndex;
                  return <Tag key={columnIndex}>{columnName} · {sensitiveType || "已脱敏"}</Tag>;
                })
                : <span>本次结果未命中脱敏列</span>}
            </div>
          </section>
        </>
      )}

      <details className="playground-raw-json">
        <summary>查看试运行原始 JSON（敏感结果已脱敏）</summary>
        <pre>{displayJSON}</pre>
      </details>
    </div>
  );
}
