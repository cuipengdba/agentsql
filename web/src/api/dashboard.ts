import { request } from "./client";
import type { AuditOutboxStatus, DashboardSummary } from "./types";

export function getDashboardSummary(days = 14, signal?: AbortSignal): Promise<DashboardSummary> {
  return request<DashboardSummary>({ method: "GET", url: "/dashboard/summary", params: { days }, signal });
}

export function getAuditOutboxStatus(signal?: AbortSignal): Promise<AuditOutboxStatus> {
  return request<AuditOutboxStatus>({ method: "GET", url: "/audit/outbox-status", signal });
}
