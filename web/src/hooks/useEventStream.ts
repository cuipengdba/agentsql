import { useCallback, useEffect, useRef, useState } from "react";

import type { AuditStreamEvent, StreamHello, StreamStatus } from "@/api/types";
import { useAuthStore } from "@/store/authStore";

interface EventStreamInput {
  enabled?: boolean;
  onAudit?: (event: AuditStreamEvent) => void;
  onHello?: (hello: StreamHello) => void;
}

interface SSEFrame {
  event: string;
  id?: string;
  data: string;
}

interface SSEParser {
  push: (text: string) => void;
  finish: () => void;
}

export function createSSEParser(onFrame: (frame: SSEFrame) => void, onComment: () => void): SSEParser {
  let buffer = "";
  let eventName = "";
  let eventID: string | undefined;
  let dataLines: string[] = [];

  const resetEvent = () => {
    eventName = "";
    eventID = undefined;
    dataLines = [];
  };

  const dispatch = () => {
    if (dataLines.length > 0) {
      onFrame({ event: eventName || "message", id: eventID, data: dataLines.join("\n") });
    }
    resetEvent();
  };

  const consumeLine = (rawLine: string) => {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (line === "") {
      dispatch();
      return;
    }
    if (line.startsWith(":")) {
      onComment();
      return;
    }

    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) {
      value = value.slice(1);
    }

    if (field === "event") {
      eventName = value;
    } else if (field === "id") {
      eventID = value;
    } else if (field === "data") {
      dataLines.push(value);
    }
  };

  return {
    push(text) {
      buffer += text;
      let newline = buffer.indexOf("\n");
      while (newline !== -1) {
        consumeLine(buffer.slice(0, newline));
        buffer = buffer.slice(newline + 1);
        newline = buffer.indexOf("\n");
      }
    },
    finish() {
      if (buffer.length > 0) {
        consumeLine(buffer);
        buffer = "";
      }
    },
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parseHello(data: string): StreamHello | null {
  try {
    const value: unknown = JSON.parse(data);
    if (!isRecord(value) || typeof value.version !== "string" || typeof value.demo !== "boolean") {
      return null;
    }
    return { version: value.version, demo: value.demo };
  } catch {
    return null;
  }
}

const optionalStringFields = [
  "agent_id",
  "datasource_id",
  "mcp_tool",
  "db_type",
  "stmt_type",
  "objects",
  "rule_hits",
  "model_name",
] as const;

const optionalNumberFields = ["risk_level", "est_rows", "rows_returned", "latency_ms"] as const;

function parseAudit(frame: SSEFrame): AuditStreamEvent | null {
  try {
    const value: unknown = JSON.parse(frame.data);
    if (!isRecord(value) || !Number.isSafeInteger(value.id) || typeof value.ts !== "string" || typeof value.decision !== "string") {
      return null;
    }
    const frameIDText = frame.id?.trim();
    if (!frameIDText) {
      return null;
    }
    const frameID = Number(frameIDText);
    if (!Number.isSafeInteger(frameID) || frameID !== value.id) {
      return null;
    }

    const event: AuditStreamEvent = {
      id: value.id as number,
      ts: value.ts,
      decision: value.decision,
    };
    optionalStringFields.forEach((field) => {
      const fieldValue = value[field];
      if (typeof fieldValue === "string" || fieldValue === null) {
        event[field] = fieldValue;
      }
    });
    optionalNumberFields.forEach((field) => {
      const fieldValue = value[field];
      if ((typeof fieldValue === "number" && Number.isFinite(fieldValue)) || fieldValue === null) {
        event[field] = fieldValue;
      }
    });
    return event;
  } catch {
    return null;
  }
}

function retryDelay(failureCount: number): number {
  const base = Math.min(30_000, 1_000 * (2 ** Math.max(0, failureCount - 1)));
  return Math.round(base * (0.8 + Math.random() * 0.4));
}

export function useEventStream({ enabled = true, onAudit, onHello }: EventStreamInput): {
  status: StreamStatus;
  failureCount: number;
  reconnectNow: () => void;
} {
  const [status, setStatus] = useState<StreamStatus>("reconnecting");
  const [failureCount, setFailureCount] = useState(0);
  const statusRef = useRef<StreamStatus>("reconnecting");
  const failureCountRef = useRef(0);
  const onAuditRef = useRef(onAudit);
  const onHelloRef = useRef(onHello);
  const startedRef = useRef(false);
  const generationRef = useRef(0);
  const disposedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const readerRef = useRef<ReadableStreamDefaultReader<Uint8Array> | null>(null);
  const retryTimerRef = useRef<number | null>(null);
  const stableTimerRef = useRef<number | null>(null);
  const lastActivityRef = useRef(0);
  const authFailureHandledRef = useRef(false);
  const fallbackModeRef = useRef(false);
  const reconnectRequestRef = useRef<() => void>(() => undefined);

  onAuditRef.current = onAudit;
  onHelloRef.current = onHello;

  const reconnectNow = useCallback(() => {
    reconnectRequestRef.current();
  }, []);

  useEffect(() => {
    if (!enabled || startedRef.current) {
      return;
    }

    startedRef.current = true;
    disposedRef.current = false;
    authFailureHandledRef.current = false;
    fallbackModeRef.current = false;

    const isCurrent = (generation: number) => !disposedRef.current && generationRef.current === generation;

    const updateStatus = (next: StreamStatus, generation: number) => {
      if (!isCurrent(generation)) return;
      statusRef.current = next;
      setStatus(next);
    };

    const clearRetryTimer = () => {
      if (retryTimerRef.current !== null) {
        window.clearTimeout(retryTimerRef.current);
        retryTimerRef.current = null;
      }
    };

    const clearStableTimer = () => {
      if (stableTimerRef.current !== null) {
        window.clearTimeout(stableTimerRef.current);
        stableTimerRef.current = null;
      }
    };

    const cancelConnection = () => {
      controllerRef.current?.abort();
      controllerRef.current = null;
      const reader = readerRef.current;
      readerRef.current = null;
      if (reader) {
        void reader.cancel().catch(() => undefined);
      }
      clearStableTimer();
    };

    const stopForUnauthorized = (generation: number) => {
      if (!isCurrent(generation) || authFailureHandledRef.current) return;
      authFailureHandledRef.current = true;
      clearRetryTimer();
      cancelConnection();
      generationRef.current += 1;
      useAuthStore.getState().clear();
      window.location.assign("/login");
    };

    const enterFallback = (generation: number) => {
      if (!isCurrent(generation)) return;
      clearRetryTimer();
      fallbackModeRef.current = true;
      updateStatus("polling-fallback", generation);
    };

    let startConnection: (fallbackProbe?: boolean) => void;

    const scheduleRetry = (generation: number, fallbackProbe: boolean) => {
      if (!isCurrent(generation)) return;
      const nextFailureCount = failureCountRef.current + 1;
      failureCountRef.current = nextFailureCount;
      setFailureCount(nextFailureCount);

      if (fallbackProbe || nextFailureCount >= 6) {
        enterFallback(generation);
        return;
      }

      updateStatus("reconnecting", generation);
      clearRetryTimer();
      retryTimerRef.current = window.setTimeout(() => {
        retryTimerRef.current = null;
        if (isCurrent(generation)) {
          startConnection(false);
        }
      }, retryDelay(nextFailureCount));
    };

    startConnection = (fallbackProbe = false) => {
      if (disposedRef.current || authFailureHandledRef.current) return;

      clearRetryTimer();
      cancelConnection();
      const generation = generationRef.current + 1;
      generationRef.current = generation;
      updateStatus("reconnecting", generation);

      const token = useAuthStore.getState().token;
      if (!token) {
        stopForUnauthorized(generation);
        return;
      }

      const controller = new AbortController();
      controllerRef.current = controller;

      void (async () => {
        try {
          const response = await fetch("/api/v1/stream", {
            headers: { Authorization: `Bearer ${token}` },
            signal: controller.signal,
          });
          if (!isCurrent(generation)) return;

          if (response.status === 401) {
            stopForUnauthorized(generation);
            return;
          }
          if (response.status === 503 || response.status >= 500) {
            scheduleRetry(generation, fallbackProbe);
            return;
          }
          const contentType = response.headers.get("content-type")?.toLowerCase() || "";
          if (!response.ok || !contentType.includes("text/event-stream") || !response.body) {
            if (response.body) void response.body.cancel().catch(() => undefined);
            enterFallback(generation);
            return;
          }

          const reader = response.body.getReader();
          readerRef.current = reader;
          const decoder = new TextDecoder("utf-8");
          let helloReceived = false;
          const parser = createSSEParser(
            (frame) => {
              if (!isCurrent(generation)) return;
              lastActivityRef.current = Date.now();
              if (frame.event === "hello") {
                const hello = parseHello(frame.data);
                if (!hello || helloReceived) return;
                helloReceived = true;
                try {
                  onHelloRef.current?.(hello);
                } catch {
                  // Consumer callbacks must not tear down the stream.
                }
                fallbackModeRef.current = false;
                updateStatus("live", generation);
                clearStableTimer();
                stableTimerRef.current = window.setTimeout(() => {
                  stableTimerRef.current = null;
                  if (!isCurrent(generation)) return;
                  failureCountRef.current = 0;
                  setFailureCount(0);
                }, 30_000);
                return;
              }
              if (frame.event === "audit") {
                const audit = parseAudit(frame);
                if (!audit) return;
                try {
                  onAuditRef.current?.(audit);
                } catch {
                  // A rendering callback failure must not poison later SSE frames.
                }
              }
            },
            () => {
              if (isCurrent(generation)) {
                lastActivityRef.current = Date.now();
              }
            },
          );

          while (isCurrent(generation)) {
            const chunk = await reader.read();
            if (!isCurrent(generation)) return;
            if (chunk.done) {
              const tail = decoder.decode();
              if (tail) parser.push(tail);
              parser.finish();
              scheduleRetry(generation, fallbackProbe);
              return;
            }
            parser.push(decoder.decode(chunk.value, { stream: true }));
          }
        } catch {
          if (!controller.signal.aborted && isCurrent(generation)) {
            scheduleRetry(generation, fallbackProbe);
          }
        } finally {
          if (controllerRef.current === controller) {
            controllerRef.current = null;
          }
          if (isCurrent(generation)) {
            readerRef.current = null;
            clearStableTimer();
          }
        }
      })();
    };

    reconnectRequestRef.current = () => {
      if (disposedRef.current || authFailureHandledRef.current || statusRef.current === "live") return;
      startConnection(fallbackModeRef.current);
    };

    const handleVisibilityChange = () => {
      if (document.visibilityState === "visible" && statusRef.current !== "live") {
        reconnectRequestRef.current();
      }
    };

    document.addEventListener("visibilitychange", handleVisibilityChange);
    startConnection(false);

    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      disposedRef.current = true;
      generationRef.current += 1;
      clearRetryTimer();
      cancelConnection();
      reconnectRequestRef.current = () => undefined;
      startedRef.current = false;
    };
  }, [enabled]);

  return { status, failureCount, reconnectNow };
}
