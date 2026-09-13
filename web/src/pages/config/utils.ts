import type { ApiResponse } from "@/api/types";

interface HTTPErrorLike {
  code?: unknown;
  message?: unknown;
  response?: { status?: unknown; data?: unknown };
}

function errorLike(error: unknown): HTTPErrorLike | null {
  return typeof error === "object" && error !== null ? error as HTTPErrorLike : null;
}

export function isCanceled(error: unknown): boolean {
  return errorLike(error)?.code === "ERR_CANCELED";
}

export function httpStatus(error: unknown): number | undefined {
  const status = errorLike(error)?.response?.status;
  return typeof status === "number" ? status : undefined;
}

export function apiErrorMessage(error: unknown, fallback: string): string {
  const data = errorLike(error)?.response?.data;
  if (typeof data === "object" && data !== null) {
    const msg = (data as Partial<ApiResponse<unknown>>).msg;
    if (typeof msg === "string" && msg.trim()) return msg;
  }
  const message = errorLike(error)?.message;
  return typeof message === "string" && message.trim() ? message : fallback;
}

export function formatDateTime(value: string | null | undefined, empty = "—"): string {
  const timestamp = Date.parse(value || "");
  return Number.isFinite(timestamp) ? new Date(timestamp).toLocaleString("zh-CN", { hour12: false }) : empty;
}

export async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  let copied = false;
  try {
    textarea.select();
    copied = document.execCommand("copy");
  } finally {
    document.body.removeChild(textarea);
  }
  if (!copied) throw new Error("copy command failed");
}
