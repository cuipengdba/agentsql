export type ClientKind = "dify" | "inspector";

export type GatewayAddress =
  | { ok: true; origin: string; loopback: boolean; insecureRemote: boolean }
  | { ok: false; error: string };

export function parseGatewayAddress(input: string): GatewayAddress {
  let address: URL;
  try {
    address = new URL(input.trim());
  } catch {
    return { ok: false, error: "填写客户端可达的完整 http(s) 根地址。" };
  }
  if (!["http:", "https:"].includes(address.protocol) || !address.hostname ||
    address.username || address.password || address.search || address.hash ||
    !["", "/"].includes(address.pathname)) {
    return { ok: false, error: "仅填写不含账号、路径、查询串和片段的 http(s) 根地址。" };
  }
  if (address.hostname === "0.0.0.0" || address.hostname === "[::]") {
    return { ok: false, error: "0.0.0.0/:: 是监听地址，不能作为客户端连接地址。" };
  }
  const host = address.hostname.toLowerCase();
  const loopback = ["localhost", "127.0.0.1", "[::1]"].includes(host);
  return { ok: true, origin: address.origin, loopback, insecureRemote: address.protocol === "http:" && !loopback };
}

export function clientInstructions(client: ClientKind, origin: string): string {
  const endpoint = `${origin}/mcp`;
  if (client === "dify") {
    return [
      "Dify 1.17.1 · Streamable HTTP",
      `Server URL: ${endpoint}`,
      "Headers:",
      "Authorization: Bearer <Agent API Key>",
      "MCP-Protocol-Version: 2025-06-18",
    ].join("\n");
  }
  return [
    "MCP Inspector 0.16.8 · Streamable HTTP",
    `Server URL: ${endpoint}`,
    "Custom Headers:",
    "Authorization: Bearer <Agent API Key>",
    "请勿手工添加 MCP-Protocol-Version；该版本客户端初始化后自动发送。",
  ].join("\n");
}
