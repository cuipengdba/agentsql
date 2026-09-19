export const maskSensitiveTypeDefinitions = [
  { value: "phone", label: "手机号", color: "blue", example: "138****1234", description: "按手机号格式保留可读片段" },
  { value: "email", label: "邮箱", color: "cyan", example: "a***@example.com", description: "按邮箱格式保留可读片段" },
  { value: "idcard", label: "身份证", color: "gold", example: "110105********002X", description: "按身份证格式保留可读片段" },
  { value: "bankcard", label: "银行卡", color: "orange", example: "411111******1111", description: "按银行卡格式保留可读片段" },
  { value: "ip", label: "IP 地址", color: "green", example: "192.168.*.*（IPv6 形如 2001:0db8:****）", description: "按 IP 地址格式保留可读片段" },
  { value: "birthdate", label: "出生日期", color: "default", example: "1990-**-**", description: "按日期格式保留可读片段" },
] as const;

export const GENERIC_SENSITIVE_TYPE = "generic" as const;
export const NUMBER_SENSITIVE_TYPE = "number" as const;
export const DATE_SENSITIVE_TYPE = "date" as const;

export const genericSensitiveTypeDefinition = {
  value: GENERIC_SENSITIVE_TYPE,
  label: "通用敏感值",
  color: "purple",
  example: "h.9f8e7d6c…",
  description: "不依赖手机号/证件等格式，对整列原值整体哈希或阻断；仅支持 hash/block 算法",
} as const;

export const rangeSensitiveTypeDefinitions = [
  {
    value: NUMBER_SENSITIVE_TYPE,
    label: "数值",
    color: "geekblue",
    example: "[40,50)",
    description: "将数值按固定宽度分桶，保留粗粒度分布",
  },
  {
    value: DATE_SENSITIVE_TYPE,
    label: "日期",
    color: "lime",
    example: "1990 / 1990Q3 / 1990-08",
    description: "将日期截断到年、季度或月份粒度",
  },
] as const;

export const sensitiveTypeDefinitions = [
  ...maskSensitiveTypeDefinitions,
  genericSensitiveTypeDefinition,
  ...rangeSensitiveTypeDefinitions,
] as const;

export type SensitiveType = (typeof sensitiveTypeDefinitions)[number]["value"];
export type MaskSensitiveType = (typeof maskSensitiveTypeDefinitions)[number]["value"];
export type RangeSensitiveType = (typeof rangeSensitiveTypeDefinitions)[number]["value"];

// 兼容 discovery 的既有导入面：发现流程永远只使用六类，不包含 generic。
export const sensitiveTypes: readonly MaskSensitiveType[] = maskSensitiveTypeDefinitions.map(({ value }) => value);
export const rangeSensitiveTypes: readonly RangeSensitiveType[] = rangeSensitiveTypeDefinitions.map(({ value }) => value);
export const allSensitiveTypes: readonly SensitiveType[] = sensitiveTypeDefinitions.map(({ value }) => value);

export const sensitiveTypeMeta = Object.fromEntries(
  sensitiveTypeDefinitions.map(({ value, ...meta }) => [value, meta]),
) as Record<SensitiveType, Omit<(typeof sensitiveTypeDefinitions)[number], "value">>;

export const sensitiveTypeOptions = sensitiveTypeDefinitions.map(({ value, label, example }) => ({
  value,
  label: `${label} · ${example}`,
}));

export function isSensitiveType(value: unknown): value is SensitiveType {
  return typeof value === "string" && allSensitiveTypes.some((type) => type === value);
}

export const MASK_ALGORITHM = "mask" as const;
export const HASH_ALGORITHM = "hash" as const;
export const BLOCK_ALGORITHM = "block" as const;
export const RANGE_ALGORITHM = "range" as const;
export const HASH_REDACTION_UNAVAILABLE_MESSAGE = "服务端尚未配置脱敏哈希密钥 AGENTSQL_REDACTION_HASH_KEY，无法启用哈希规则；可先保存为停用状态，配置密钥并重启后再启用";

export const maskAlgorithmDefinitions = [
  {
    value: MASK_ALGORITHM,
    label: "打码（mask）",
    color: "blue",
    description: "确定性部分遮蔽，保留可读片段，如 138****5678",
    example: "138****5678",
  },
  {
    value: HASH_ALGORITHM,
    label: "哈希指纹（hash）",
    color: "purple",
    description: "不可逆 HMAC 指纹；相同原值结果一致，可用于跨表等值关联/去重，但无法还原原文；需在服务端配置 AGENTSQL_REDACTION_HASH_KEY，否则启用会被拒绝",
    example: "h.9f8e7d6c…",
  },
  {
    value: BLOCK_ALGORITHM,
    label: "阻断（block）",
    color: "red",
    description: "命中列的每个非空值统一替换为固定 ***，不保留任何原文片段、长度或等值关系，适用于最高敏感列",
    example: "***",
  },
  {
    value: RANGE_ALGORITHM,
    label: "分桶/截断（range）",
    color: "cyan",
    description: "保留粗粒度分布、无需密钥、不是匿名化",
    example: "[40,50) / 1990Q3",
  },
] as const;

export type MaskAlgorithm = (typeof maskAlgorithmDefinitions)[number]["value"];

export const maskAlgorithms: readonly MaskAlgorithm[] = maskAlgorithmDefinitions.map(({ value }) => value);

export const maskAlgorithmMeta = Object.fromEntries(
  maskAlgorithmDefinitions.map(({ value, ...meta }) => [value, meta]),
) as Record<MaskAlgorithm, Omit<(typeof maskAlgorithmDefinitions)[number], "value">>;

export const maskAlgorithmOptions = maskAlgorithmDefinitions.map(({ value, label }) => ({ value, label }));

export function isMaskAlgorithm(value: unknown): value is MaskAlgorithm {
  return typeof value === "string" && maskAlgorithms.some((algorithm) => algorithm === value);
}

export function sensitiveTypeOptionsForAlgorithm(algorithm: MaskAlgorithm) {
  return sensitiveTypeDefinitions
    .filter(({ value }) => isSensitiveTypeAllowedForAlgorithm(value, algorithm))
    .map(({ value, label }) => ({
      value,
      label: `${label} · ${sensitiveTypePresentationForAlgorithm(value, algorithm).optionDetail}`,
    }));
}

export function isSensitiveTypeAllowedForAlgorithm(type: SensitiveType, algorithm: MaskAlgorithm): boolean {
  if (algorithm === MASK_ALGORITHM) return sensitiveTypes.some((candidate) => candidate === type);
  if (algorithm === RANGE_ALGORITHM) return rangeSensitiveTypes.some((candidate) => candidate === type);
  return algorithm === HASH_ALGORITHM || algorithm === BLOCK_ALGORITHM;
}

export type RangeGranularity = "year" | "quarter" | "month";

export interface RangePresentationParams {
  range_bucket_width?: number | null;
  range_bucket_offset?: number | null;
  range_granularity?: RangeGranularity | null;
}

export const INVALID_RANGE_EXAMPLE = "请先填写合法参数";

export function rangeExample(type: SensitiveType, params: RangePresentationParams): string {
  if (type === NUMBER_SENSITIVE_TYPE) {
    const width = params.range_bucket_width;
    const offset = params.range_bucket_offset;
    if (
      !Number.isInteger(width)
      || width === null
      || width === undefined
      || width < 1
      || width > 1_000_000_000
      || !Number.isInteger(offset)
      || offset === null
      || offset === undefined
      || offset < -1_000_000_000
      || offset > 1_000_000_000
    ) return INVALID_RANGE_EXAMPLE;
    const lower = Math.floor((42 - offset) / width) * width + offset;
    return `[${lower},${lower + width})`;
  }
  if (type === DATE_SENSITIVE_TYPE) {
    if (params.range_granularity === "year") return "1990";
    if (params.range_granularity === "quarter") return "1990Q3";
    if (params.range_granularity === "month") return "1990-08";
  }
  return INVALID_RANGE_EXAMPLE;
}

export function sensitiveTypePresentationForAlgorithm(type: SensitiveType, algorithm: MaskAlgorithm) {
  const meta = sensitiveTypeMeta[type];
  if (algorithm === HASH_ALGORITHM) {
    return {
      optionDetail: "原值整体哈希",
      description: `按“${meta.label}”记录敏感类型，对该列原值整体哈希。`,
      example: maskAlgorithmMeta[HASH_ALGORITHM].example,
    };
  }
  if (algorithm === BLOCK_ALGORITHM) {
    return {
      optionDetail: "原值整体阻断为 ***",
      description: `按“${meta.label}”记录敏感类型，对该列原值整体阻断为固定 ***，不保留原文片段、长度或等值关系。`,
      example: maskAlgorithmMeta[BLOCK_ALGORITHM].example,
    };
  }
  if (algorithm === RANGE_ALGORITHM) {
    return {
      optionDetail: type === NUMBER_SENSITIVE_TYPE ? "按宽度分桶" : "按日期粒度截断",
      description: `按“${meta.label}”保留粗粒度分布，无需密钥；同桶值仍可能被关联，不属于匿名化。`,
      example: INVALID_RANGE_EXAMPLE,
    };
  }
  return {
    optionDetail: meta.example,
    description: meta.description,
    example: meta.example,
  };
}
