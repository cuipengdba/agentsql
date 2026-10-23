import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SQLBotDiagnosticsView } from "./SQLBot";

const localeState = vi.hoisted(() => ({ locale: "zh-CN" as "zh-CN" | "en-US" }));
vi.mock("@/i18n/locale", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/i18n/locale")>(),
  useLocaleStore: (selector: (state: typeof localeState) => unknown) => selector(localeState),
}));
afterEach(() => { localeState.locale = "zh-CN"; });
describe("SQLBot diagnostics", () => {
  const data = { binding_id: "binding-1", revision: 2, configuration_ready: true, live_query_verified: false as const, supported_sqlbot_version: "1.10.2", checks: [{ code: "user_query_permission", ok: true }] };
  it("distinguishes configuration readiness from actual SQLBot query acceptance", () => {
    localeState.locale = "zh-CN";
    const html = renderToStaticMarkup(<SQLBotDiagnosticsView data={data} />);
    expect(html).toContain("配置检查通过");
    expect(html).toContain("未执行业务查询");
    expect(html).toContain("映射人员具有查询权限");
  });
  it("renders readable English checks and revoked permission failures", () => {
    localeState.locale = "en-US";
    const html = renderToStaticMarkup(<SQLBotDiagnosticsView data={{ ...data, configuration_ready: false, checks: [{ code: "user_query_permission", ok: false }] }} />);
    expect(html).toContain("Configuration needs attention");
    expect(html).toContain("No business query was executed");
    expect(html).toContain("Failed");
    expect(html).not.toContain("配置");
  });
});
