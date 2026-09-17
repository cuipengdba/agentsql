export interface DemoProjection {
  enabled: true;
  banner: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

export async function fetchDemoProjection(): Promise<DemoProjection | null> {
  try {
    const response = await fetch("/healthz", { cache: "no-store" });
    if (!response.ok) return null;

    const payload: unknown = await response.json();
    if (!isRecord(payload) || !isRecord(payload.demo)) return null;
    if (payload.demo.enabled !== true || typeof payload.demo.banner !== "string") return null;

    const banner = payload.demo.banner.trim();
    return banner ? { enabled: true, banner } : null;
  } catch {
    return null;
  }
}
