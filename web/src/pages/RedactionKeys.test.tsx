import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import type { RedactionKeysResponse } from "@/api/redactionKeys";
import { RedactionKeysView } from "./RedactionKeys";

const data: RedactionKeysResponse = {
  registered: [{ id: "1", state: "active", commitment: "a".repeat(64), label: "", config_revision: "r1", created_at: "2026-09-23T00:00:00Z", updated_at: "2026-09-23T00:00:00Z" }, { id: "2", state: "standby", commitment: "b".repeat(64), label: "", config_revision: "r1", created_at: "2026-09-23T00:00:00Z", updated_at: "2026-09-23T00:00:00Z" }],
  observed: {
    status: "available", mode: "manifest", active_version: 1, revision: "r1", ready: false,
    keys: [{ id: 1, commitment: "a".repeat(64) }],
    drift: {
      unsatisfied: [3],
      warnings: [
        { number: 3, kind: "active_not_registered", version: 1, message: "active mismatch" },
        { number: 4, kind: "configured_commitment_mismatch", version: 2, message: "commitment mismatch" },
        { number: 7, kind: "revision_mismatch", version: 1, message: "revision mismatch" },
      ],
      information: [{ number: 6, kind: "extra_registered", version: 3, message: "id set differs" }],
    },
  },
};

describe("RedactionKeysView", () => {
  it("renders observed, registered, every drift item and only operational command text", () => {
    const html = renderToStaticMarkup(<RedactionKeysView data={data} />);
    expect(html).toContain("进程观察");
    expect(html).toContain("登记状态");
    expect(html).toContain("active_not_registered");
    expect(html).toContain("configured_commitment_mismatch");
    expect(html).toContain("revision_mismatch");
    expect(html).toContain("extra_registered");
    expect(html).toContain("redaction-key registry-mark-active");
    expect(html).not.toContain("新增密钥");
    expect(html).not.toContain("立即激活");
  });
});
