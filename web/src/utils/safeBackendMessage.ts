export function safeBackendMessage(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const text = value.trim();
  if (!text || text.length > 160 || /[\r\n]/.test(text)) return null;
  if (/api.?key|bearer|password|passwd|dsn|stack|panic|sqlstate|driver|:\/\/[^\s]+@|(?:host|port)\s*=/i.test(text)) return null;
  return text;
}
