import { request } from "./client";

export interface B5Status {
  enabled: boolean;
  state: string;
  reason: string;
  ready: boolean;
}

export interface Page<T> {
  total: number;
  page: number;
  page_size: number;
  list: T[];
}

export interface B5Session {
  id: string;
  agent_id: string;
  tenant_id: string;
  principal_id: string;
  status: string;
  owner_instance_id: string;
  owner_epoch: number;
  sticky_route: string;
  idle_expires_at: string;
  absolute_expires_at: string;
  created_at: string;
  updated_at: string;
}

export interface B5Event {
  sequence: number;
  type: string;
  schema_id: string;
  schema_version: number;
  previous_digest: string;
  digest: string;
  created_at: string;
}

export interface B5Transaction {
  id: string;
  session_id: string;
  datasource_id: string;
  status: string;
  phase: string;
  plan_digest: string;
  approval_id?: string;
  owner_epoch: number;
  idle_deadline: string;
  wall_deadline: string;
  statement_deadline?: string;
  backend_pid?: number;
  backend_started_at?: string;
  connection_generation: number;
  lease_generation: number;
  statement_count: number;
  sequence: number;
  previous_event_digest: string;
  created_at: string;
  updated_at: string;
  events: B5Event[];
}

export interface B5BackendIdentity {
  server_id: string;
  database: string;
  pid: number;
  backend_started_at?: string;
  dial_permit_id: string;
}

export interface B5Quarantine {
  lease_id: string;
  claim_id: string;
  datasource_id: string;
  reason: string;
  age_seconds: number;
  charged_slots: number;
  generation: number;
  inventory_epoch: number;
  backend: B5BackendIdentity;
}

export interface B5Metrics {
  sessions: number;
  transactions: number;
  unknown: number;
  discard_unconfirmed: number;
  quarantine_count: number;
  quarantine_charged_slots: number;
  hard_budget: number;
  quarantine_budget_ratio: number;
  session_states: Array<{ state: string; count: number }>;
  transaction_states: Array<{ state: string; count: number }>;
  phase_durations: Array<{ phase: string; p50_ms: number; p95_ms: number; p99_ms: number }>;
}

export interface B5Reconciliation {
  event_uuid: string;
  transaction_id: string;
  sequence: number;
  classification: string;
  reported_durability: string;
  append_confirmation: string;
  reconciliation: string;
  replayed: boolean;
  updated_at: string;
}

export interface B5Filters {
  page?: number;
  page_size?: number;
  status?: string;
  phase?: string;
  datasource_id?: string;
  owner?: string;
  q?: string;
}

export const getB5Status = (signal?: AbortSignal) => request<B5Status>({ method: "GET", url: "/b5/status", signal, suppressErrorMessage: true });
export const getB5Sessions = (params: B5Filters, signal?: AbortSignal) => request<Page<B5Session>>({ method: "GET", url: "/b5/sessions", params, signal });
export const getB5Session = (id: string) => request<B5Session>({ method: "GET", url: `/b5/sessions/${encodeURIComponent(id)}` });
export const getB5Transactions = (params: B5Filters, signal?: AbortSignal) => request<Page<B5Transaction>>({ method: "GET", url: "/b5/transactions", params, signal });
export const getB5Transaction = (id: string) => request<B5Transaction>({ method: "GET", url: `/b5/transactions/${encodeURIComponent(id)}` });
export const getB5Quarantine = (params: B5Filters, signal?: AbortSignal) => request<Page<B5Quarantine>>({ method: "GET", url: "/b5/quarantine", params, signal });
export const getB5Metrics = (datasourceId?: string, signal?: AbortSignal) => request<B5Metrics>({ method: "GET", url: "/b5/metrics", params: { datasource_id: datasourceId || undefined }, signal });
export const getB5Reconciliation = (params: B5Filters, signal?: AbortSignal) => request<Page<B5Reconciliation>>({ method: "GET", url: "/b5/reconciliation", params, signal });
export const confirmB5Discard = (leaseId: string) => request<B5Quarantine>({ method: "POST", url: `/b5/quarantine/${encodeURIComponent(leaseId)}/confirm-discard`, data: { confirm: true } });
export const reconcileB5 = (transactionId?: string, datasourceId?: string) => request<B5Reconciliation[]>({ method: "POST", url: "/b5/reconciliation", data: { transaction_id: transactionId, datasource_id: datasourceId, confirm: true } });
