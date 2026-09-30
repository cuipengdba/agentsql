export interface DemoProjection {
  enabled: true;
  banner: string;
  adminUsername?: string;
  adminPassword?: string;
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

    const adminUsername =
      typeof payload.demo.admin_username === "string" && payload.demo.admin_username.trim()
        ? payload.demo.admin_username
        : undefined;
    const adminPassword =
      typeof payload.demo.admin_password === "string" && payload.demo.admin_password.trim()
        ? payload.demo.admin_password
        : undefined;
    return { enabled: true, banner, adminUsername, adminPassword };
  } catch {
    return null;
  }
}
