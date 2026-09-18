export interface ApiResponse<T> {
  code: number;
  msg: string;
  data: T;
}

export interface PageResp<T> {
  total: number;
  page: number;
  page_size: number;
  list: T[];
}

export interface PageQuery {
  page?: number;
  page_size?: number;
}

export interface LoginInput {
  username: string;
  password: string;
}

export interface LoginView {
  token: string;
  expires_at: string;
}

export interface AdminView {
  username: string;
}

export interface AgentView {
  id: string;
  name: string;
  owner?: string | null;
  status: string;
  level: string;
  expires_at?: string | null;
  created_at: string;
  updated_at: string;
  api_key?: string;
}

export interface AgentCreateInput {
  id: string;
  name: string;
  owner?: string | null;
  status?: string;
  level: string;
  expires_at?: string | null;
}

export interface AgentUpdateInput {
  name?: string;
  owner?: string | null;
  status?: string;
  level?: string;
  expires_at?: string | null;
}

export interface DatasourceView {
  id: string;
  name: string;
  db_type: string;
  host: string;
  port: number;
  database: string;
  username: string;
  conn_limit: number;
  stmt_timeout_ms: number;
  row_limit: number;
  has_password: boolean;
}

export interface DatasourceInput {
  id: string;
  name: string;
  db_type: string;
  host: string;
  port: number;
  database: string;
  username: string;
  password?: string;
  conn_limit: number;
  stmt_timeout_ms: number;
  row_limit: number;
}

export interface PingView {
  ok: boolean;
  latency_ms: number;
}

export interface PolicyView {
  id: string;
  agent_id: string;
  datasource_id: string;
  object_type: string;
  object_name: string;
  columns?: string | null;
  row_filter?: string | null;
  action: string;
  created_at: string;
  updated_at: string;
}

export interface PolicyInput {
  id: string;
  agent_id: string;
  datasource_id: string;
  object_type: string;
  object_name: string;
  columns?: string | null;
  row_filter?: string | null;
  action: string;
}

export interface RuleView {
  id: string;
  db_type: string;
  title: string;
  risk_level: number;
  pattern_type: string;
  definition: string;
  enabled: boolean;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface RuleInput {
  id?: string;
  db_type?: string;
  title?: string;
  risk_level?: number;
  pattern_type?: string;
  definition?: string;
  enabled?: boolean;
  builtin?: boolean;
}

export interface MaskRuleView {
  id: string;
  datasource_id?: string | null;
  table_name: string;
  column_name: string;
  sensitive_type: string;
  algo: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface MaskRuleInput {
  id: string;
  datasource_id?: string | null;
  table_name?: string;
  column_name: string;
  sensitive_type: string;
  algo: string;
  enabled?: boolean;
}

export interface AuditView {
  id: number;
  ts: string;
  agent_id?: string | null;
  datasource_id?: string | null;
  session_id?: string | null;
  conversation_id?: string | null;
  mcp_tool?: string | null;
  db_type?: string | null;
  sql_raw?: string | null;
  sql_norm?: string | null;
  stmt_type?: string | null;
  objects?: string | null;
  decision: string;
  rule_hits?: string | null;
  risk_level?: number | null;
  est_rows?: number | null;
  rows_returned?: number | null;
  latency_ms?: number | null;
  client_ip?: string | null;
  model_name?: string | null;
  error_msg?: string | null;
}

export type StreamStatus = "live" | "reconnecting" | "polling-fallback";

export interface StreamHello {
  version: string;
  demo: boolean;
}

export interface AuditStreamEvent {
  id: number;
  ts: string;
  decision: string;
  agent_id?: string | null;
  datasource_id?: string | null;
  mcp_tool?: string | null;
  db_type?: string | null;
  stmt_type?: string | null;
  objects?: string | null;
  rule_hits?: string | null;
  risk_level?: number | null;
  est_rows?: number | null;
  rows_returned?: number | null;
  latency_ms?: number | null;
  model_name?: string | null;
}

export interface AuditQuery extends PageQuery {
  time_start?: string;
  time_end?: string;
  agent_id?: string;
  datasource_id?: string;
  session_id?: string;
  mcp_tool?: string;
  decisions?: string;
  stmt_types?: string;
  risk_min?: number;
  risk_max?: number;
  keyword?: string;
  object?: string;
}

export interface ApprovalView {
  id: string;
  audit_id?: number | null;
  agent_id?: string | null;
  sql_raw?: string | null;
  reason?: string | null;
  status: string;
  approver?: string | null;
  decided_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface ApprovalDecisionInput {
  decision: "approve" | "reject";
  comment?: string;
}

export interface DashboardKPI {
  total_requests: number;
  blocked: number;
  pending_approvals: number;
  active_agents: number;
  datasources_total: number;
  total_requests_change_pct: number | null;
  blocked_change_pct: number | null;
}

export interface TrendDay {
  date: string;
  total: number;
  deny: number;
  warn: number;
  approve: number;
  allow: number;
}

export interface DecisionCount {
  decision: string;
  count: number;
}

export interface RiskTopEntry {
  rule_id: string;
  count: number;
}

export interface AgentRankingItem {
  agent_id: string;
  name: string;
  blocked_count: number;
}

export interface BattleReport {
  blocked_count: number;
  est_rows_saved: number;
}

export interface DashboardSummary {
  kpi: DashboardKPI;
  trend_14d: TrendDay[];
  decision_distribution: DecisionCount[];
  risk_top: RiskTopEntry[];
  agent_ranking: AgentRankingItem[];
  battle_report: BattleReport;
}

export interface DeleteView {
  deleted: boolean;
}

export type NotificationChannelKind = "webhook" | "syslog";
export type NotificationDecision = "deny" | "error" | "warn" | "allow" | "approve";
export type NotificationWebhookTemplate = "generic" | "feishu" | "dingtalk" | "wecom" | "slack";
export type NotificationSyslogTransport = "udp" | "tcp";

export interface NotificationWebhook {
  template: NotificationWebhookTemplate;
  url: string;
  url_configured: boolean;
  bearer_token: string;
  bearer_token_configured: boolean;
  headers: Record<string, string>;
  headers_configured: boolean;
  secret: string;
  secret_configured: boolean;
}

export interface NotificationSyslog {
  host: string;
  port: number;
  transport: NotificationSyslogTransport;
  facility: number;
}

export interface NotificationChannel {
  id: string;
  enabled: boolean;
  kind: NotificationChannelKind;
  decisions: NotificationDecision[];
  include_sql: boolean;
  allow_private_endpoints: boolean;
  webhook?: NotificationWebhook;
  syslog?: NotificationSyslog;
}

export interface NotificationConfig {
  enabled: boolean;
  queue_size: number;
  channels: NotificationChannel[];
}

export interface NotificationTestResult {
  channel_id: string;
  success: boolean;
  category: string;
}

export interface NotificationHealthChannel {
  id: string;
  sent: number;
  failed: number;
  dropped: number;
  last_error?: string;
  last_success_at?: string;
}

export interface NotificationHealth {
  channels: NotificationHealthChannel[];
}

export interface PlaygroundAssessRequest {
  sql: string;
  db_type: "postgres" | "mysql";
  agent_level?: "readonly" | "dml" | "ddl";
}

export interface PlaygroundHitView {
  RuleID: string;
  Risk: number;
  Decision: string;
  Message: string;
  Suggestion: string;
}

export interface PlaygroundObjectView {
  Schema: string;
  Table: string;
  Alias: string;
}

export interface PlaygroundStageLatency {
  auth: number;
  load: number;
  parse: number;
  guard_static: number;
  guard_dynamic: number;
  execute: number;
  redact: number;
  audit: number;
}

export interface PlaygroundAssessView {
  Decision: string;
  Risk: number;
  StmtType: string;
  Hits: PlaygroundHitView[];
  EstScanRows: number;
  Reason: string;
  Suggestion: string;
  Normalized: string;
  Objects: PlaygroundObjectView[];
  StageLatency: PlaygroundStageLatency;
  ParseError: string;
  StaticOnly: boolean;
  DBType: string;
  AgentLevel: string;
  SQL: string;
}

export type PlaygroundDemoDatasourceID = "ds-demo-pg" | "ds-demo-mysql";
export type PlaygroundDemoAgentProfile = "ro" | "dml";

export interface PlaygroundRunRequest {
  sql: string;
  datasource_id: PlaygroundDemoDatasourceID;
  agent_profile: PlaygroundDemoAgentProfile;
}

export interface PlaygroundRunHitView {
  rule_id: string;
  risk: number;
  decision: string;
  message: string;
  suggestion: string;
}

export interface PlaygroundRunObjectView {
  schema: string;
  table: string;
  alias: string;
}

export interface PlaygroundRunAssessmentView {
  decision: string;
  risk: number;
  stmt_type: string;
  hits: PlaygroundRunHitView[];
  est_scan_rows: number;
  reason: string;
  suggestion: string;
  normalized: string;
  objects: PlaygroundRunObjectView[];
  stage_latency: Record<string, number>;
}

export interface PlaygroundRunResultView {
  columns: string[];
  rows: string[][];
  row_count: number;
  truncated: boolean;
  latency_ms: number;
}

export interface PlaygroundRunRedactView {
  touched_columns: Record<string, string>;
  masked_cells: number;
}

export interface PlaygroundRunResponse {
  decision: string;
  assessment: PlaygroundRunAssessmentView;
  result: PlaygroundRunResultView;
  redact: PlaygroundRunRedactView;
  audit_id: number;
  datasource_id: PlaygroundDemoDatasourceID;
  agent_profile: PlaygroundDemoAgentProfile;
}
