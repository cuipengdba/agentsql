import { describe, expect, it } from "vitest";

import { clientInstructions, parseGatewayAddress } from "./clientInstructions";

describe("client connection instructions", () => {
  it("rejects addresses that could leak credentials or target the wrong path", () => {
    for (const address of ["https://user:secret@example.test", "https://example.test/mcp", "https://example.test/?key=secret", "http://0.0.0.0:7780"]) {
      expect(parseGatewayAddress(address).ok).toBe(false);
    }
    expect(parseGatewayAddress("http://127.0.0.1:7780/")).toEqual({ ok: true, origin: "http://127.0.0.1:7780", loopback: true, insecureRemote: false });
    expect(parseGatewayAddress("http://gateway.example.test:7780")).toEqual({ ok: true, origin: "http://gateway.example.test:7780", loopback: false, insecureRemote: true });
  });

  it("keeps the client protocols separate and never embeds a real key", () => {
    const dify = clientInstructions("dify", "https://gateway.example.test");
    const inspector = clientInstructions("inspector", "https://gateway.example.test");
    expect(dify).toContain("MCP-Protocol-Version: 2025-06-18");
    expect(inspector).not.toContain("MCP-Protocol-Version: 2025-06-18");
    expect(dify).toContain("Bearer <Agent API Key>");
    expect(inspector).toContain("Bearer <Agent API Key>");
    expect(dify).toContain("https://gateway.example.test/mcp");
  });
});
