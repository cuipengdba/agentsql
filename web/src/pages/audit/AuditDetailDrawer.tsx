import { currentLocale } from "@/i18n/locale";
import { Alert, Descriptions, Drawer, Empty, Tag } from "antd";
import type { CSSProperties, ReactNode } from "react";
import { useMemo } from "react";

import type { AuditView } from "@/api/types";
import { StageFlow } from "@/components/stageflow/StageFlow";
import { StageVerdict } from "@/components/stageflow/StageVerdict";
import type { StageVerdictData } from "@/components/stageflow/types";
import { getErrorCodeMeta } from "@/constants/errorCodes";
import { auditActivityLabel, decisionMeta, getDecisionMeta, statementLabel } from "@/constants/labels";
import { getRuleMeta } from "@/constants/ruleMeta";
import { palette } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";
import { safeBackendMessage } from "@/utils/safeBackendMessage";

import { auditToFlow, normalizeAuditDecision, parseAuditRuleHits } from "./auditToFlow";
import { SQLHighlight } from "./sqlHighlight";

interface AuditDetailDrawerProps {
  record: AuditView | null;
  open: boolean;
  onClose: () => void;
}

interface ConclusionStyle extends CSSProperties {
  "--audit-decision-color": string;
  "--audit-decision-surface": string;
}

function text(value: string | null | undefined): string {
  return value?.trim() || "—";
}

function numberText(value: number | null | undefined, suffix = ""): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  return `${value.toLocaleString(currentLocale())}${suffix}`;
}

function dateTime(value: string): string {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? new Date(timestamp).toLocaleString(currentLocale(), { hour12: false }) : "—";
}

function knownDecision(value: string): boolean {
  return Object.prototype.hasOwnProperty.call(decisionMeta, value.trim().toLowerCase());
}

function verdictData(hit: ReturnType<typeof parseAuditRuleHits>[number]): StageVerdictData {
  const meta = getRuleMeta(hit.RuleID);
  return {
    ruleId: hit.RuleID || undefined,
    title: meta.title,
    message: hit.Message || "规则命中，未提供详细判词",
    suggestion: hit.Suggestion || undefined,
    risk: Number.isFinite(hit.Risk) ? hit.Risk : meta.risk,
  };
}

interface MetricItem {
  label: string;
  value: ReactNode;
}

interface AuditPhase {
  phase: "intent" | "outcome";
  relatedAuditID?: number;
}

interface TaskGrantInfo {
  grantID?: string;
  requestID?: string;
  issuerID?: string;
}

function parseTaskGrantInfo(encoded: string | null | undefined): TaskGrantInfo | undefined {
  if (!encoded?.trim()) return undefined;
  try {
    const value: unknown = JSON.parse(encoded);
    if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
    const fields = value as Record<string, unknown>;
    const bounded = (candidate: unknown): string | undefined => typeof candidate === "string" && candidate.length > 0 && candidate.length <= 128 ? candidate : undefined;
    const grantID = bounded(fields.task_grant_id) || bounded(fields.grant_id);
    const requestID = bounded(fields.request_id);
    const issuerID = bounded(fields.task_grant_issuer_id) || bounded(fields.issuer_user_id);
    return grantID || requestID ? { grantID, requestID, issuerID } : undefined;
  } catch {
    return undefined;
  }
}

function parseAuditPhase(encoded: string | null | undefined): AuditPhase | undefined {
  if (!encoded?.trim()) return undefined;
  try {
    const value: unknown = JSON.parse(encoded);
    if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
    const fields = value as Record<string, unknown>;
    if (fields.audit_phase !== "intent" && fields.audit_phase !== "outcome") return undefined;
    const relatedAuditID = Number.isSafeInteger(fields.related_audit_id) && Number(fields.related_audit_id) > 0
      ? Number(fields.related_audit_id)
      : undefined;
    return { phase: fields.audit_phase, relatedAuditID };
  } catch {
    return undefined;
  }
}

export function AuditDetailDrawer({ record, open, onClose }: AuditDetailDrawerProps) {
  const mode = useThemeStore((state) => state.mode);
  const flow = useMemo(() => record ? auditToFlow(record) : null, [record]);
  const hits = useMemo(() => parseAuditRuleHits(record?.rule_hits), [record?.rule_hits]);
  const metrics = useMemo<MetricItem[]>(() => {
    if (!record) return [];
    const items: MetricItem[] = [];
    if (record.est_rows !== null && record.est_rows !== undefined && Number.isFinite(record.est_rows)) items.push({ label: "预估扫描行", value: numberText(record.est_rows) });
    if (record.rows_returned !== null && record.rows_returned !== undefined && Number.isFinite(record.rows_returned)) items.push({ label: "实际返回", value: numberText(record.rows_returned, " 行") });
    if (record.latency_ms !== null && record.latency_ms !== undefined && Number.isFinite(record.latency_ms)) items.push({ label: "总耗时", value: numberText(record.latency_ms, " ms") });
    if (record.stmt_type?.trim()) items.push({ label: "语句类型", value: statementLabel(record.stmt_type) });
    if (record.objects?.trim()) items.push({ label: "涉及对象", value: record.objects });
    return items;
  }, [record]);

  if (!record || !flow) return null;
  const management = Boolean(record.action?.trim());
  const rawDecision = record.decision?.trim() || "error";
  const normalizedDecision = rawDecision.toLowerCase();
  const decisionInfo = getDecisionMeta(rawDecision);
  const errorCode = normalizedDecision === "error" ? getErrorCodeMeta(record.error_code) : undefined;
  const errorMessage = safeBackendMessage(record.error_msg);
  const auditPhase = parseAuditPhase(record.details_json);
  const taskGrant = parseTaskGrantInfo(record.details_json);
  const colors = palette[mode];
  const conclusionStyle: ConclusionStyle = {
    "--audit-decision-color": knownDecision(rawDecision) ? decisionInfo.color : palette.semantic.deny,
    "--audit-decision-surface": colors.elevated,
  };

  return (
    <Drawer title={management ? "管理审计" : "审计证据链"} open={open} onClose={onClose} width="min(720px, 100vw)" destroyOnClose rootClassName="audit-detail-drawer">
      <section className="audit-conclusion" style={conclusionStyle}>
        <div className="audit-conclusion-bar" />
        <div className="audit-conclusion-content">
          {knownDecision(rawDecision) ? <Tag color={decisionInfo.tagColor}>{decisionInfo.label}</Tag> : <Tag className="audit-decision-unknown">{rawDecision}</Tag>}
          {management ? <span>{auditActivityLabel(record.stmt_type, record.action)}</span> : <span>风险 {numberText(record.risk_level)}</span>}
          <time>{dateTime(record.ts)}</time>
          <span className="mono-text">#{record.id}</span>
          {auditPhase ? (
            <Tag color={auditPhase.phase === "intent" ? "blue" : "cyan"}>
              {auditPhase.phase === "intent" ? "意图" : "结果"}
              {auditPhase.phase === "outcome" && auditPhase.relatedAuditID ? ` · 关联 #${auditPhase.relatedAuditID}` : ""}
            </Tag>
          ) : null}
        </div>
      </section>

      <section className="audit-detail-section">
        <h3>基本信息</h3>
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2 }}>
          {management ? <>
            <Descriptions.Item label="管理操作">{auditActivityLabel(record.stmt_type, record.action)}</Descriptions.Item>
            <Descriptions.Item label="操作者">{text(record.actor_id)}</Descriptions.Item>
            <Descriptions.Item label="Agent">{text(record.agent_id)}</Descriptions.Item>
            <Descriptions.Item label="数据源">{text(record.datasource_id)}</Descriptions.Item>
          </> : <>
            <Descriptions.Item label="Agent">{text(record.agent_id)}</Descriptions.Item>
            <Descriptions.Item label="数据源">{text(record.datasource_id)}</Descriptions.Item>
            <Descriptions.Item label="数据库类型">{text(record.db_type)}</Descriptions.Item>
            <Descriptions.Item label="MCP 工具">{text(record.mcp_tool)}</Descriptions.Item>
            <Descriptions.Item label="会话">{text(record.session_id)}</Descriptions.Item>
            <Descriptions.Item label="对话">{text(record.conversation_id)}</Descriptions.Item>
            <Descriptions.Item label="客户端 IP">{text(record.client_ip)}</Descriptions.Item>
            <Descriptions.Item label="模型">{text(record.model_name)}</Descriptions.Item>
          </>}
        </Descriptions>
      </section>

      {taskGrant ? <section className="audit-detail-section">
        <h3>任务授权关联</h3>
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2 }}>
          {taskGrant.grantID && taskGrant.grantID === taskGrant.requestID ?
            <Descriptions.Item label="申请 / 授权 ID" span={2}><code>{taskGrant.grantID}</code></Descriptions.Item> : <>
              {taskGrant.grantID && <Descriptions.Item label="授权 ID"><code>{taskGrant.grantID}</code></Descriptions.Item>}
              {taskGrant.requestID && <Descriptions.Item label="申请 ID"><code>{taskGrant.requestID}</code></Descriptions.Item>}
            </>}
          {taskGrant.issuerID && <Descriptions.Item label="申请人"><code>{taskGrant.issuerID}</code></Descriptions.Item>}
        </Descriptions>
      </section> : null}

      {!management ? <>
      <section className="audit-detail-section">
        <h3>六段安检时间线</h3>
        <StageFlow data={flow} autoPlay={false} replayKey={record.id} dense />
      </section>

      <section className="audit-detail-section">
        <h3>SQL 证据</h3>
        <div className="audit-sql-grid">
          <SQLHighlight title="SQL 原文" value={record.sql_raw} />
          <SQLHighlight title="归一化 SQL" value={record.sql_norm} />
        </div>
      </section>

      <section className="audit-detail-section">
        <h3>命中规则判词</h3>
          {hits.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={normalizedDecision === "deny" ? "没有规则判词；请查看下方拒绝原因与授权边界" : normalizedDecision === "error" ? "没有规则判词；请查看下方错误信息" : "未命中规则"} /> : (
          <div className="audit-verdict-list">
            {hits.map((hit, index) => <StageVerdict key={`${hit.RuleID || "rule"}-${index}`} data={verdictData(hit)} decision={normalizeAuditDecision(hit.Decision)} dense />)}
          </div>
        )}
      </section>

      <section className="audit-detail-section">
        <h3>执行评估</h3>
        {metrics.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="本次审计没有执行评估数据" /> : (
          <div className="audit-metrics">
            {metrics.map((item) => <div className="audit-metric" key={item.label}><span>{item.label}</span><strong className="mono-text">{item.value}</strong></div>)}
          </div>
        )}
        <p className="audit-plan-note">详细 EXPLAIN 计划留存规划于 v1.1</p>
      </section>
      </> : null}

      {errorCode || errorMessage ? (
        <section className="audit-detail-section">
          <h3>{normalizedDecision === "deny" ? "拒绝原因" : "错误信息"}</h3>
          <Alert
            type="error"
            showIcon
            message={errorCode ? (
              <span className="audit-error-heading">
                <Tag color="volcano">{errorCode.label} <code>{errorCode.code}</code></Tag>
                <span>{errorMessage || errorCode.label}</span>
              </span>
            ) : errorMessage}
            description={auditPhase ? `审计阶段：${auditPhase.phase === "intent" ? "意图" : "结果"}${auditPhase.relatedAuditID ? `，关联审计 #${auditPhase.relatedAuditID}` : ""}` : undefined}
          />
        </section>
      ) : null}
    </Drawer>
  );
}
