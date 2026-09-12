import { request, requestRaw } from "./client";
import type { AuditQuery, AuditView, PageResp } from "./types";

export function listAudit(params: AuditQuery = {}, signal?: AbortSignal): Promise<PageResp<AuditView>> {
  return request<PageResp<AuditView>>({ method: "GET", url: "/audit", params, signal });
}

export function exportAudit(params: Omit<AuditQuery, "page" | "page_size"> = {}): Promise<Blob> {
  return requestRaw<Blob>({ method: "GET", url: "/audit/export", params, responseType: "blob" });
}
