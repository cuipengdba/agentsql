export const sensitiveTypeDefinitions = [
  { value: "phone", label: "手机号", color: "blue", example: "138****1234" },
  { value: "email", label: "邮箱", color: "cyan", example: "a***@example.com" },
  { value: "idcard", label: "身份证", color: "gold", example: "110105********002X" },
  { value: "bankcard", label: "银行卡", color: "orange", example: "411111******1111" },
  { value: "ip", label: "IP 地址", color: "green", example: "192.168.*.*（IPv6 形如 2001:0db8:****）" },
  { value: "birthdate", label: "出生日期", color: "default", example: "1990-**-**" },
] as const;

export type SensitiveType = (typeof sensitiveTypeDefinitions)[number]["value"];

export const sensitiveTypes: readonly SensitiveType[] = sensitiveTypeDefinitions.map(({ value }) => value);

export const sensitiveTypeMeta = Object.fromEntries(
  sensitiveTypeDefinitions.map(({ value, ...meta }) => [value, meta]),
) as Record<SensitiveType, Omit<(typeof sensitiveTypeDefinitions)[number], "value">>;

export const sensitiveTypeOptions = sensitiveTypeDefinitions.map(({ value, label, example }) => ({
  value,
  label: `${label} · ${example}`,
}));

export function isSensitiveType(value: unknown): value is SensitiveType {
  return typeof value === "string" && sensitiveTypes.some((type) => type === value);
}

export const MASK_ALGORITHM = "mask" as const;
export type MaskAlgorithm = typeof MASK_ALGORITHM;

export const maskAlgorithmMeta: Record<MaskAlgorithm, { label: string; color: string }> = {
  [MASK_ALGORITHM]: { label: "打码", color: "blue" },
};

export const maskAlgorithmOptions = [{
  value: MASK_ALGORITHM,
  label: `${maskAlgorithmMeta[MASK_ALGORITHM].label}（${MASK_ALGORITHM}）`,
}];

export function isMaskAlgorithm(value: unknown): value is MaskAlgorithm {
  return value === MASK_ALGORITHM;
}
