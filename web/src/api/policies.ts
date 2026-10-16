import { request } from "./client";
import type { DeleteView, PageQuery, PageResp, PolicyInput, PolicyPreflightView, PolicyView } from "./types";

export interface PolicyQuery extends PageQuery {
  agent_id?: string;
  datasource_id?: string;
}

export function listPolicies(params: PolicyQuery = {}, signal?: AbortSignal): Promise<PageResp<PolicyView>> {
  return request<PageResp<PolicyView>>({ method: "GET", url: "/policies", params, signal });
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

export function preflightPolicy(input: { agent_id: string; datasource_id: string; sql: string }): Promise<PolicyPreflightView> {
  return request<PolicyPreflightView>({ method: "POST", url: "/policies/preflight", data: input });
}
