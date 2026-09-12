import { request } from "./client";
import type { DashboardSummary } from "./types";

export function getDashboardSummary(days = 14, signal?: AbortSignal): Promise<DashboardSummary> {
  return request<DashboardSummary>({ method: "GET", url: "/dashboard/summary", params: { days }, signal });
}
