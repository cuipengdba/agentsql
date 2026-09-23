import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { MaskRules } from "./MaskRules";

describe("MaskRules redaction key summary", () => {
  it("explains restart-style switching and links to the read-only key page", () => {
    const html = renderToStaticMarkup(<MemoryRouter><MaskRules /></MemoryRouter>);
    expect(html).toContain("当前 active 版本");
    expect(html).toContain("重启式计划切换");
    expect(html).toContain("/settings/redaction-keys");
  });
});
