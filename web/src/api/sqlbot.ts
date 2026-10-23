import { request } from "./client";

export interface SQLBotBinding {
  id: string;
  tenant_id: string;
  workspace_id: string;
  datasource_name: string;
  sqlbot_user_id: string;
  agentsql_user_id: string;
  agent_id: string;
  datasource_id: string;
  schema_name: string;
  numeric_columns: string[];
  agent_key_env: string;
  connector_token_env: string;
  enabled: boolean;
  revision: number;
  created_at: string;
  updated_at: string;
}
export type SQLBotBindingInput = Omit<SQLBotBinding, "id" | "tenant_id" | "created_at" | "updated_at">;
export interface SQLBotCredential { binding: SQLBotBinding; connector_token: string }
export interface SQLBotDiagnostics {
  binding_id: string;
  revision: number;
  configuration_ready: boolean;
  live_query_verified: false;
  checks: { code: string; ok: boolean }[];
  supported_sqlbot_version: string;
}
const base = "/integrations/sqlbot";
export const listSQLBotBindings = () => request<SQLBotBinding[]>({ method: "GET", url: `${base}/bindings` });
export const createSQLBotBinding = (data: SQLBotBindingInput) => request<SQLBotCredential>({ method: "POST", url: `${base}/bindings`, data });
export const updateSQLBotBinding = (id: string, data: SQLBotBindingInput) => request<SQLBotBinding>({ method: "PUT", url: `${base}/bindings/${encodeURIComponent(id)}`, data });
export const deleteSQLBotBinding = (id: string, revision: number) => request<{ deleted: boolean }>({ method: "DELETE", url: `${base}/bindings/${encodeURIComponent(id)}`, data: { revision } });
export const rotateSQLBotConnector = (id: string, revision: number) => request<SQLBotCredential>({ method: "POST", url: `${base}/bindings/${encodeURIComponent(id)}/rotate`, data: { revision } });
export const diagnoseSQLBotBinding = (id: string) => request<SQLBotDiagnostics>({ method: "GET", url: `${base}/bindings/${encodeURIComponent(id)}/diagnostics` });
export const exportSQLBotConfig = (endpoint: string) => request<Record<string, unknown>>({ method: "POST", url: `${base}/export`, data: { endpoint } });
