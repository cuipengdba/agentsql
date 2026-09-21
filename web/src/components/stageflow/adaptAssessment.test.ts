import { describe, expect, it } from "vitest";

import { adaptAssessment, errorStageToStageKey } from "./adaptAssessment";
import type { StageKey } from "./types";

const stageOrder: StageKey[] = ["auth", "parse", "guard", "decide", "execute", "audit"];

function expectFailureAt(errorStage: string | undefined, expected: StageKey) {
  const flow = adaptAssessment({ Decision: "error" }, errorStage);
  expect(errorStageToStageKey(errorStage)).toBe(expected);
  expect(flow.blockAt).toBe(expected);
  const failedIndex = stageOrder.indexOf(expected);
  flow.steps.forEach((step, index) => {
    expect(step.status).toBe(index < failedIndex ? "pass" : index === failedIndex ? "block" : "skip");
  });
}

describe("adaptAssessment database error stages", () => {
  it.each([
    ["parse", "parse"],
    ["explain", "guard"],
    ["metadata", "guard"],
    ["connect", "execute"],
    ["ping", "execute"],
    ["acquire", "execute"],
    ["begin_tx", "execute"],
    ["query", "execute"],
    ["read_rows", "execute"],
    ["execute", "execute"],
    ["commit", "execute"],
    ["rollback", "execute"],
  ] as const)("maps %s to %s", (errorStage, expected) => {
    expectFailureAt(errorStage, expected);
  });

  it("falls back old error records to execute and keeps parse passed", () => {
    expectFailureAt(undefined, "execute");
    const flow = adaptAssessment({ Decision: "error" });
    expect(flow.steps.find((step) => step.key === "parse")?.status).toBe("pass");
  });

  it("shows EXPLAIN failure and prioritizes the safe backend message and suggestion", () => {
    const flow = adaptAssessment({
      Decision: "error",
      Reason: "旧判词",
      ErrorMessage: "表或对象不存在",
      Suggestion: "请检查对象名称和当前数据库",
    }, "explain");
    expect(flow.steps.find((step) => step.key === "guard")?.note).toBe("EXPLAIN 失败");
    expect(flow.verdict).toMatchObject({
      title: "数据库执行失败",
      message: "表或对象不存在",
      suggestion: "请检查对象名称和当前数据库",
    });
  });
});
