import { request } from "./client";
import type { TaskReadGrantRequestInput, TaskReadGrantRequestView, TaskReadGrantView } from "./types";

export function listTaskReadGrants(): Promise<TaskReadGrantView[]> {
  return request<TaskReadGrantView[]>({ method: "GET", url: "/task-read-grants" });
}

export function listTaskReadGrantRequests(): Promise<TaskReadGrantRequestView[]> {
  return request<TaskReadGrantRequestView[]>({ method: "GET", url: "/task-read-grant-requests" });
}

export function getTaskReadGrantRequest(id: string): Promise<TaskReadGrantRequestView> {
  return request<TaskReadGrantRequestView>({ method: "GET", url: `/task-read-grant-requests/${encodeURIComponent(id)}` });
}

export function createTaskReadGrantRequest(input: TaskReadGrantRequestInput): Promise<TaskReadGrantRequestView> {
  return request<TaskReadGrantRequestView>({ method: "POST", url: "/task-read-grant-requests", data: input });
}

export function approveTaskReadGrantRequest(id: string): Promise<TaskReadGrantRequestView> {
  return request<TaskReadGrantRequestView>({ method: "POST", url: `/task-read-grant-requests/${encodeURIComponent(id)}/approve` });
}

export function rejectTaskReadGrantRequest(id: string): Promise<TaskReadGrantRequestView> {
  return request<TaskReadGrantRequestView>({ method: "POST", url: `/task-read-grant-requests/${encodeURIComponent(id)}/reject` });
}

export function issueTaskReadGrantRequest(id: string): Promise<TaskReadGrantView> {
  return request<TaskReadGrantView>({ method: "POST", url: `/task-read-grant-requests/${encodeURIComponent(id)}/issue` });
}

export function revokeTaskReadGrant(id: string): Promise<TaskReadGrantView> {
  return request<TaskReadGrantView>({ method: "POST", url: `/task-read-grants/${encodeURIComponent(id)}/revoke` });
}
