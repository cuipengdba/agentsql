import { describe, expect, it } from "vitest";

import { createSSEParser, parseAudit } from "./useEventStream";

describe("audit SSE cursor contract", () => {
  it("accepts a tenant-local resume cursor distinct from the audit ID", () => {
    const events: ReturnType<typeof parseAudit>[] = [];
    const parser = createSSEParser((frame) => {
      if (frame.event === "audit") events.push(parseAudit(frame));
    }, () => undefined);
    parser.push('event: audit\nid: 2\ndata:{"id":1042,"ts":"2026-10-24T00:00:00Z","decision":"allow"}\n\n');
    expect(events).toEqual([{ id: 1042, ts: "2026-10-24T00:00:00Z", decision: "allow" }]);
  });

  it("rejects missing or unsafe cursor values", () => {
    const data = '{"id":1042,"ts":"2026-10-24T00:00:00Z","decision":"allow"}';
    expect(parseAudit({ event: "audit", data })).toBeNull();
    expect(parseAudit({ event: "audit", id: "9007199254740992", data })).toBeNull();
    expect(parseAudit({ event: "audit", id: "-1", data })).toBeNull();
  });
});
