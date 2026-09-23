import { request } from "./client";

export interface RedactionKeyVersion {
  id: string;
  state: "standby" | "active" | "legacy" | "retired";
  commitment: string;
  label: string;
  config_revision: string;
  created_at: string;
  updated_at: string;
  activated_at?: string | null;
  retired_at?: string | null;
}

export interface RedactionDriftDetail {
  number: number;
  kind: string;
  version?: number;
  message: string;
}

export interface RedactionKeysResponse {
  registered: RedactionKeyVersion[];
  observed: {
    status: string;
    mode?: string;
    active_version?: number;
    keys?: Array<{ id: number; commitment: string }>;
    revision?: string;
    ready?: boolean;
    drift?: { unsatisfied: number[]; warnings: RedactionDriftDetail[]; information: RedactionDriftDetail[] };
  };
}

export function getRedactionKeys(signal?: AbortSignal): Promise<RedactionKeysResponse> {
  return request<RedactionKeysResponse>({ method: "GET", url: "/redaction/keys", signal });
}
