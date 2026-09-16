import { request } from "./client";
import type { DeleteView, PageQuery, PageResp, RuleInput, RuleView } from "./types";

export interface RuleQuery extends PageQuery {
  db_type?: string;
}

export function listRules(params: RuleQuery = {}, signal?: AbortSignal): Promise<PageResp<RuleView>> {
  return request<PageResp<RuleView>>({ method: "GET", url: "/rules", params, signal });
}

export function createRule(input: RuleInput): Promise<RuleView> {
  return request<RuleView>({ method: "POST", url: "/rules", data: input });
}

export function updateRule(id: string, input: RuleInput): Promise<RuleView> {
  return request<RuleView>({ method: "PUT", url: `/rules/${encodeURIComponent(id)}`, data: input });
}

export function deleteRule(id: string): Promise<DeleteView> {
  return request<DeleteView>({ method: "DELETE", url: `/rules/${encodeURIComponent(id)}` });
}
