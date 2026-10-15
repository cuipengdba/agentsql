import { request } from "./client";
import type { TaskReadGrantInput, TaskReadGrantView } from "./types";

export function listTaskReadGrants(): Promise<TaskReadGrantView[]> {
  return request<TaskReadGrantView[]>({ method: "GET", url: "/task-read-grants" });
}

export function createTaskReadGrant(input: TaskReadGrantInput): Promise<TaskReadGrantView> {
  return request<TaskReadGrantView>({ method: "POST", url: "/task-read-grants", data: input });
}

export function revokeTaskReadGrant(id: string): Promise<TaskReadGrantView> {
  return request<TaskReadGrantView>({ method: "POST", url: `/task-read-grants/${encodeURIComponent(id)}/revoke` });
}
