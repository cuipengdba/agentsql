import { request } from "./client";

export type AuditChainDomain = "management" | "traffic";

export interface AuditChainVerificationStatus {
  result?: string | null;
  last_verified_at?: string | null;
  last_verified_head_seq?: number | null;
  break_seq?: number | null;
  break_id?: number | null;
  break_reason?: string | null;
}

export interface AuditChainStatus {
  chain_id: string;
  status: string;
  mode?: string | null;
  head_seq: number;
  head_id?: number | null;
  protected_since?: number | null;
  genesis_at?: string | null;
  observed_instance?: string | null;
  verification: AuditChainVerificationStatus;
}

export interface AuditChainBreak {
  seq: number;
  id: number;
  reason: string;
}

export interface AuditChainVerifyResult {
  result: string;
  head_seq: number;
  total: number;
  unchained: number;
  break?: AuditChainBreak | null;
}

export function getAuditChainStatus(domain: AuditChainDomain, signal?: AbortSignal): Promise<AuditChainStatus> {
  return request<AuditChainStatus>({
    method: "GET",
    url: "/audit-chain/status",
    params: { domain },
    signal,
    suppressErrorMessage: true,
  });
}

export function verifyAuditChain(domain: AuditChainDomain): Promise<AuditChainVerifyResult> {
  return request<AuditChainVerifyResult>({
    method: "POST",
    url: "/audit-chain/verify",
    params: { domain },
    suppressErrorMessage: true,
  });
}
