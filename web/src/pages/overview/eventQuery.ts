import type { AuditQuery, AuditStreamEvent, AuditView } from "@/api/types";

export const PAGE_SIZE = 10;

interface BuildEventQueryInput {
  page: number;
  pageSize: number;
  decision?: string;
  stmtType?: string;
  appliedKeyword: string;
}

interface CanMergeLiveInput {
  page: number;
  decision?: string;
  stmtType?: string;
  appliedKeyword: string;
}

export type DisplayEvent = AuditStreamEvent & Partial<AuditView>;

export function buildEventQuery({
  page,
  pageSize,
  decision,
  stmtType,
  appliedKeyword,
}: BuildEventQueryInput): AuditQuery {
  const query: AuditQuery = { page, page_size: pageSize };
  if (decision?.trim()) query.decisions = decision.trim();
  if (stmtType?.trim()) query.stmt_types = stmtType.trim();
  const keyword = appliedKeyword.trim();
  if (keyword) query.keyword = keyword;
  return query;
}

export function canMergeLive({ page, decision, stmtType, appliedKeyword }: CanMergeLiveInput): boolean {
  return page === 1 && !decision?.trim() && !stmtType?.trim() && !appliedKeyword.trim();
}

function compareEvents(left: { id: number; ts: string }, right: { id: number; ts: string }): number {
  const leftTime = Date.parse(left.ts);
  const rightTime = Date.parse(right.ts);
  const safeLeftTime = Number.isFinite(leftTime) ? leftTime : Number.NEGATIVE_INFINITY;
  const safeRightTime = Number.isFinite(rightTime) ? rightTime : Number.NEGATIVE_INFINITY;
  return safeRightTime - safeLeftTime || right.id - left.id;
}

export function mergeLiveIntoPage({
  polled,
  live,
}: {
  polled: AuditView[];
  live: AuditStreamEvent[];
}): DisplayEvent[] {
  const byID = new Map<number, DisplayEvent>();
  live.forEach((event) => byID.set(event.id, event));
  polled.forEach((event) => {
    const realtime = byID.get(event.id);
    byID.set(event.id, realtime ? { ...realtime, ...event } : event);
  });
  return [...byID.values()].sort(compareEvents).slice(0, PAGE_SIZE);
}

export function clampPage(page: number, total: number, pageSize: number): number {
  const lastPage = Math.max(1, Math.ceil(Math.max(0, total) / Math.max(1, pageSize)));
  return Math.min(Math.max(1, page), lastPage);
}
