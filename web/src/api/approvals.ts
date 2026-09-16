import { request } from "./client";
import type { ApprovalDecisionInput, ApprovalView, PageQuery, PageResp } from "./types";

export interface ApprovalQuery extends PageQuery {
  status?: string;
}

export function listApprovals(params: ApprovalQuery = {}, signal?: AbortSignal): Promise<PageResp<ApprovalView>> {
  return request<PageResp<ApprovalView>>({ method: "GET", url: "/approvals", params, signal });
}

export function decideApproval(id: string, input: ApprovalDecisionInput): Promise<ApprovalView> {
  return request<ApprovalView>({ method: "POST", url: `/approvals/${encodeURIComponent(id)}/decide`, data: input });
}
