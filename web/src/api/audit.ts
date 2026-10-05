import { request, requestRaw } from "./client";
import type { AuditQuery, AuditView, PageResp } from "./types";

export type AuditExportFormat = "jsonl" | "csv" | "pdf" | "archive";

export function auditExportFilename(format: AuditExportFormat): string {
  if (format === "archive") return "agentsql-audit-archive.zip";
  return `agentsql-audit.${format}`;
}

export function listAudit(params: AuditQuery = {}, signal?: AbortSignal): Promise<PageResp<AuditView>> {
  return request<PageResp<AuditView>>({ method: "GET", url: "/audit", params, signal });
}

export function exportAudit(
  params: Omit<AuditQuery, "page" | "page_size"> = {},
  format: AuditExportFormat = "jsonl",
): Promise<Blob> {
  return requestRaw<Blob>({
    method: "GET",
    url: "/audit/export",
    params: { ...params, format },
    responseType: "blob",
    suppressErrorMessage: true,
    timeout: 60_000,
  });
}
