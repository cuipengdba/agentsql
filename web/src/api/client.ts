import { message } from "antd";
import axios, {
  type AxiosError,
  type AxiosRequestConfig,
  type InternalAxiosRequestConfig,
} from "axios";

import { useAuthStore } from "@/store/authStore";

import type { ApiResponse } from "./types";

const apiClient = axios.create({
  baseURL: "/api/v1",
  timeout: 15_000,
});

const rawClient = axios.create({
  baseURL: "/api/v1",
  timeout: 15_000,
});

function attachAuthorization(request: InternalAxiosRequestConfig): InternalAxiosRequestConfig {
  const token = useAuthStore.getState().token;
  if (token) {
    request.headers.Authorization = `Bearer ${token}`;
  }
  return request;
}

apiClient.interceptors.request.use(attachAuthorization);
rawClient.interceptors.request.use(attachAuthorization);

function isApiResponse(value: unknown): value is ApiResponse<unknown> {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return typeof candidate.code === "number" && typeof candidate.msg === "string" && "data" in candidate;
}

function rejectHTTPError(error: AxiosError<ApiResponse<unknown>>): Promise<never> {
  // AbortController 取消（切换剧本/新请求/卸载）时静默 reject，不弹全局错误提示。
  // Use the stable code without changing the AxiosError type for later branches.
  if (error.code === "ERR_CANCELED") {
    return Promise.reject(error);
  }
  const status = error.response?.status;
  const errorMessage = error.response?.data?.msg || "请求失败，请稍后重试";
  if (status === 401) {
    useAuthStore.getState().clear();
    if (window.location.pathname !== "/login") {
      window.location.assign("/login");
    }
  }
  void message.error(errorMessage);
  return Promise.reject(error);
}

apiClient.interceptors.response.use(
  (response) => {
    const payload: unknown = response.data;
    if (!isApiResponse(payload)) {
      void message.error("服务响应格式无效");
      return Promise.reject(new Error("invalid API response"));
    }
    if (payload.code !== 0) {
      void message.error(payload.msg || "请求失败，请稍后重试");
      return Promise.reject(new Error(payload.msg || "request failed"));
    }
    response.data = payload.data;
    return response;
  },
  rejectHTTPError,
);
rawClient.interceptors.response.use((response) => response, rejectHTTPError);

export async function request<T>(config: AxiosRequestConfig): Promise<T> {
  const response = await apiClient.request<T>(config);
  return response.data;
}

export async function requestRaw<T>(config: AxiosRequestConfig): Promise<T> {
  const response = await rawClient.request<T>(config);
  return response.data;
}
