import axios from "axios";

import { request } from "@/api/client";
import type {
  PlaygroundAssessRequest,
  PlaygroundAssessView,
  PlaygroundRunRequest,
  PlaygroundRunResponse,
} from "@/api/types";

export function assessPlayground(body: PlaygroundAssessRequest, signal?: AbortSignal): Promise<PlaygroundAssessView> {
  return request<PlaygroundAssessView>({ method: "POST", url: "/playground/assess", data: body, signal });
}

export function runPlayground(body: PlaygroundRunRequest, signal?: AbortSignal): Promise<PlaygroundRunResponse> {
  return request<PlaygroundRunResponse>({
    method: "POST",
    url: "/playground/run",
    data: body,
    signal,
    suppressErrorMessage: true,
  });
}

const runErrorFallbacks: Readonly<Record<number, string>> = {
  400: "试运行请求格式无效，请检查 SQL 后重试",
  401: "登录状态已失效，请重新登录",
  403: "所选数据源不允许用于演示",
  404: "真实试运行仅在 Live Demo 环境开放",
  422: "SQL 或演示选项无效，请检查后重试",
  500: "演示服务暂时不可用，请稍后重试",
};

function safeBackendMessage(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const text = value.trim();
  if (!text || text.length > 160 || /[\r\n]/.test(text)) return null;
  if (/api.?key|bearer|password|passwd|dsn|stack|panic|:\/\/[^\s]+@/i.test(text)) return null;
  return text;
}

export function playgroundRunErrorMessage(error: unknown): string {
  if (!axios.isAxiosError(error)) return "真实试运行失败，请稍后重试";
  const status = error.response?.status;
  const payload: unknown = error.response?.data;
  const backendMessage = typeof payload === "object" && payload !== null
    ? safeBackendMessage((payload as Record<string, unknown>).msg)
    : null;
  if (backendMessage && backendMessage !== "internal error") return backendMessage;
  return status ? runErrorFallbacks[status] || "真实试运行失败，请稍后重试" : "无法连接演示服务，请稍后重试";
}

export function isPlaygroundRequestCanceled(error: unknown): boolean {
  return axios.isCancel(error) || (axios.isAxiosError(error) && error.code === "ERR_CANCELED");
}
