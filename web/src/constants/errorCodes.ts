export interface ErrorCodeMeta {
  code: string;
  label: string;
}

const errorCodeLabels = {
  DB_OBJECT_NOT_FOUND: "表或对象不存在",
  DB_COLUMN_NOT_FOUND: "列不存在",
  DB_OBJECT_ALREADY_EXISTS: "对象已存在",
  DB_SYNTAX_ERROR: "SQL 语法有误",
  DB_SEMANTIC_ERROR: "SQL 语义有误",
  DB_DATA_EXCEPTION: "数据异常",
  DB_CONSTRAINT_VIOLATION: "数据库约束冲突",
  DB_RETRYABLE_CONFLICT: "数据库并发冲突",
  DB_TRANSACTION_STATE: "事务状态异常",
  DB_RESOURCE_EXHAUSTED: "数据库资源不足",
  DB_QUERY_TIMEOUT: "查询超时",
  DB_QUERY_INTERRUPTED: "查询被中断",
  DB_PERMISSION_DENIED: "数据库权限不足",
  DB_READ_ONLY_VIOLATION: "只读写入被拒绝",
  DB_AUTHENTICATION_FAILED: "数据库认证失败",
  DB_DATABASE_NOT_FOUND: "目标数据库不存在",
  DB_DATASOURCE_UNREACHABLE: "数据源不可达",
  DB_EXECUTION_FAILED: "数据库执行失败",
  GATEWAY_INTERNAL: "网关内部错误",
  AUDIT_UNAVAILABLE: "审计服务不可用",
  COMMIT_OUTCOME_UNKNOWN: "提交结果未知",
} as const;

export type StableErrorCode = keyof typeof errorCodeLabels;

export function getErrorCodeMeta(code: string | null | undefined): ErrorCodeMeta | undefined {
  const normalized = (code || "").trim().toUpperCase();
  if (!Object.prototype.hasOwnProperty.call(errorCodeLabels, normalized)) return undefined;
  return { code: normalized, label: errorCodeLabels[normalized as StableErrorCode] };
}

export const stableErrorCodes = Object.freeze(Object.keys(errorCodeLabels) as StableErrorCode[]);
