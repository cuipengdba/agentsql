import { describe, expect, it } from "vitest";

import { getErrorCodeMeta, stableErrorCodes } from "./errorCodes";

describe("errorCodes", () => {
  it("contains the 18 database codes and 4 reserved public codes", () => {
    expect(stableErrorCodes).toHaveLength(22);
    expect(getErrorCodeMeta("DB_OBJECT_NOT_FOUND")).toEqual({
      code: "DB_OBJECT_NOT_FOUND",
      label: "表或对象不存在",
    });
    expect(getErrorCodeMeta("db_query_timeout")?.label).toBe("查询超时");
    expect(getErrorCodeMeta("AUDIT_OVERLOADED")?.label).toBe("系统繁忙，请稍后重试");
    expect(getErrorCodeMeta("COMMIT_OUTCOME_UNKNOWN")?.label).toBe("提交结果未知");
  });

  it("does not invent metadata for missing or unknown codes", () => {
    expect(getErrorCodeMeta(undefined)).toBeUndefined();
    expect(getErrorCodeMeta("DB_VENDOR_RAW_ERROR")).toBeUndefined();
  });
});
