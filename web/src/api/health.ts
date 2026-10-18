export interface DemoProjection {
  enabled: true;
  banner: string;
}

export type GatewayReadiness = "ready" | "not_ready" | "unreachable";

export async function fetchGatewayReadiness(): Promise<GatewayReadiness> {
  try {
    const response = await fetch("/readyz", { cache: "no-store" });
    if (response.ok) return "ready";
    if (response.status === 503) return "not_ready";
    return "unreachable";
  } catch {
    return "unreachable";
  }
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
    if (!banner) return null;

    return { enabled: true, banner };
  } catch {
    return null;
  }
}
