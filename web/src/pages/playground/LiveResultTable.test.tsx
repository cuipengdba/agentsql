import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import type { PlaygroundRunResponse } from "@/api/types";

import { LiveResultTable } from "./LiveResultTable";

vi.mock("react-router-dom", () => ({ useNavigate: () => () => undefined }));

const errorResult: PlaygroundRunResponse = {
  decision: "error",
  error_code: "DB_OBJECT_NOT_FOUND",
  error_stage: "explain",
  error_message: "表或对象不存在",
  suggestion: "请检查对象名称和当前数据库",
  assessment: {
    decision: "allow",
    risk: 0,
    stmt_type: "SELECT",
    hits: [],
    est_scan_rows: 0,
    reason: "",
    suggestion: "",
    normalized: "SELECT * FROM missing_table",
    objects: [],
    stage_latency: { parse: 1, guard_dynamic: 2 },
  },
  result: { columns: [], rows: [], row_count: 0, truncated: false, latency_ms: 0 },
  redact: { touched_columns: {}, masked_cells: 0 },
  audit_id: 337,
  datasource_id: "ds-demo-pg",
  agent_profile: "ro",
};

describe("LiveResultTable error response", () => {
  it("renders the business error and audit entry without success table metadata", () => {
    const html = renderToStaticMarkup(<LiveResultTable result={errorResult} />);

    expect(html).toContain("执行出错");
    expect(html).toContain("表或对象不存在");
    expect(html).toContain("DB_OBJECT_NOT_FOUND");
    expect(html).toContain("请检查对象名称和当前数据库");
    expect(html).toContain("查看该审计");
    expect(html).not.toContain("返回行数");
    expect(html).not.toContain("当前展示");
    expect(html).not.toContain("本次结果未命中脱敏列");
  });
});
