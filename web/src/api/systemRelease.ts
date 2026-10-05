import { request } from "./client";

export interface SystemRelease {
  version: string;
  upgrade_mode: "check-and-dry-run";
}

export function getSystemRelease(signal?: AbortSignal): Promise<SystemRelease> {
  return request<SystemRelease>({ method: "GET", url: "/system/release", signal, suppressErrorMessage: true });
}
