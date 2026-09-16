import { request } from "./client";
import type { AgentCreateInput, AgentUpdateInput, AgentView, DeleteView, PageQuery, PageResp } from "./types";

export function listAgents(params: PageQuery = {}, signal?: AbortSignal): Promise<PageResp<AgentView>> {
  return request<PageResp<AgentView>>({ method: "GET", url: "/agents", params, signal });
}

export function getAgent(id: string): Promise<AgentView> {
  return request<AgentView>({ method: "GET", url: `/agents/${encodeURIComponent(id)}` });
}

export function createAgent(input: AgentCreateInput): Promise<AgentView> {
  return request<AgentView>({ method: "POST", url: "/agents", data: input });
}

export function updateAgent(id: string, input: AgentUpdateInput): Promise<AgentView> {
  return request<AgentView>({ method: "PUT", url: `/agents/${encodeURIComponent(id)}`, data: input });
}

export function deleteAgent(id: string): Promise<DeleteView> {
  return request<DeleteView>({ method: "DELETE", url: `/agents/${encodeURIComponent(id)}` });
}

export function rotateAgentKey(id: string): Promise<AgentView> {
  return request<AgentView>({ method: "POST", url: `/agents/${encodeURIComponent(id)}/rotate-key` });
}
