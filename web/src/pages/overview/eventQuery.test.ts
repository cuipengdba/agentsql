import { describe, expect, it } from "vitest";

import type { AuditStreamEvent, AuditView } from "@/api/types";

import { PAGE_SIZE, buildEventQuery, canMergeLive, clampPage, mergeLiveIntoPage } from "./eventQuery";

function polledEvent(id: number, ts: string, sqlRaw?: string): AuditView {
  return { id, ts, decision: "deny", sql_raw: sqlRaw, agent_id: `polled-${id}` };
}

function liveEvent(id: number, ts: string): AuditStreamEvent {
  return { id, ts, decision: "deny", agent_id: `live-${id}`, stmt_type: "SELECT" };
}

describe("buildEventQuery", () => {
  it("always includes paging defaults", () => {
    expect(buildEventQuery({ page: 1, pageSize: PAGE_SIZE, appliedKeyword: "" }))
      .toEqual({ page: 1, page_size: 10 });
  });

  it("maps one decision and one statement type to backend parameters", () => {
    expect(buildEventQuery({ page: 2, pageSize: PAGE_SIZE, decision: "deny", stmtType: "UPDATE", appliedKeyword: "" }))
      .toEqual({ page: 2, page_size: 10, decisions: "deny", stmt_types: "UPDATE" });
  });

  it("trims keyword and omits blank optional values", () => {
    expect(buildEventQuery({ page: 3, pageSize: PAGE_SIZE, decision: " ", stmtType: "", appliedKeyword: "  drop table  " }))
      .toEqual({ page: 3, page_size: 10, keyword: "drop table" });
  });
});

describe("canMergeLive", () => {
  it("allows only the unfiltered first page", () => {
    expect(canMergeLive({ page: 1, appliedKeyword: "" })).toBe(true);
    expect(canMergeLive({ page: 2, appliedKeyword: "" })).toBe(false);
    expect(canMergeLive({ page: 1, decision: "deny", appliedKeyword: "" })).toBe(false);
    expect(canMergeLive({ page: 1, stmtType: "SELECT", appliedKeyword: "" })).toBe(false);
    expect(canMergeLive({ page: 1, appliedKeyword: "select" })).toBe(false);
  });
});

describe("mergeLiveIntoPage", () => {
  it("deduplicates, prefers complete polled records, sorts descending, and caps at ten", () => {
    const live = Array.from({ length: 12 }, (_, index) => liveEvent(index + 1, `2026-09-22T00:00:${String(index).padStart(2, "0")}Z`));
    const polled = [polledEvent(8, "2026-09-22T00:00:30Z", "SELECT * FROM audit")];
    const merged = mergeLiveIntoPage({ polled, live });

    expect(merged).toHaveLength(10);
    expect(merged[0]).toMatchObject({ id: 8, agent_id: "polled-8", sql_raw: "SELECT * FROM audit", stmt_type: "SELECT" });
    expect(new Set(merged.map((event) => event.id)).size).toBe(10);
    expect(merged.map((event) => event.id)).toEqual([8, 12, 11, 10, 9, 7, 6, 5, 4, 3]);
  });
});

describe("clampPage", () => {
  it("keeps the page within the available range", () => {
    expect(clampPage(4, 0, PAGE_SIZE)).toBe(1);
    expect(clampPage(5, 32, PAGE_SIZE)).toBe(4);
    expect(clampPage(0, 32, PAGE_SIZE)).toBe(1);
  });
});
