import { request } from "./client";
import type {
  NotificationChannel,
  NotificationConfig,
  NotificationHealth,
  NotificationTestResult,
} from "./types";

const baseURL = "/integrations/notifications";

export function getNotificationConfig(signal?: AbortSignal): Promise<NotificationConfig> {
  return request<NotificationConfig>({
    method: "GET",
    url: baseURL,
    signal,
    suppressErrorMessage: true,
  });
}

export function putNotificationConfig(input: NotificationConfig): Promise<NotificationConfig> {
  return request<NotificationConfig>({
    method: "PUT",
    url: baseURL,
    data: input,
    suppressErrorMessage: true,
  });
}

export function testNotificationChannel(channel: NotificationChannel): Promise<NotificationTestResult> {
  return request<NotificationTestResult>({
    method: "POST",
    url: `${baseURL}/test`,
    data: { channel },
    suppressErrorMessage: true,
  });
}

export function getNotificationHealth(signal?: AbortSignal): Promise<NotificationHealth> {
  return request<NotificationHealth>({
    method: "GET",
    url: `${baseURL}/health`,
    signal,
    suppressErrorMessage: true,
  });
}
