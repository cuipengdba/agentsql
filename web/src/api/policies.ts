import { request } from "./client";
import type { DeleteView, PageQuery, PageResp, PolicyInput, PolicyView } from "./types";

export interface PolicyQuery extends PageQuery {
  agent_id?: string;
  datasource_id?: string;
}

export function listPolicies(params: PolicyQuery = {}): Promise<PageResp<PolicyView>> {
  return request<PageResp<PolicyView>>({ method: "GET", url: "/policies", params });
}

export function createPolicy(input: PolicyInput): Promise<PolicyView> {
  return request<PolicyView>({ method: "POST", url: "/policies", data: input });
}

export function updatePolicy(id: string, input: PolicyInput): Promise<PolicyView> {
  return request<PolicyView>({ method: "PUT", url: `/policies/${encodeURIComponent(id)}`, data: input });
}

export function deletePolicy(id: string): Promise<DeleteView> {
  return request<DeleteView>({ method: "DELETE", url: `/policies/${encodeURIComponent(id)}` });
}
