import { describe, expect, it } from "vitest";

import { auditExportFilename } from "./audit";

describe("audit export filenames", () => {
  it("maps every API format to a safe fixed extension", () => {
    expect(auditExportFilename("jsonl")).toBe("agentsql-audit.jsonl");
    expect(auditExportFilename("csv")).toBe("agentsql-audit.csv");
    expect(auditExportFilename("pdf")).toBe("agentsql-audit.pdf");
    expect(auditExportFilename("archive")).toBe("agentsql-audit-archive.zip");
  });
});
