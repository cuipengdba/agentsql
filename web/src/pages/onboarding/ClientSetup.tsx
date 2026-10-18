import { Alert, Button, Card, Input, Radio, Space, Typography, message } from "antd";
import { useState } from "react";

import { clientInstructions, parseGatewayAddress, type ClientKind } from "./clientInstructions";

export function ClientSetup({ agentID }: { agentID: string }) {
  const [client, setClient] = useState<ClientKind>("dify");
  const [address, setAddress] = useState("");
  const parsed = parseGatewayAddress(address);
  const instructions = parsed.ok ? clientInstructions(client, parsed.origin) : "";

  const copy = async () => {
    if (!instructions) return;
    try {
      await navigator.clipboard.writeText(instructions);
      void message.success("已复制不含真实密钥的接入说明");
    } catch {
      void message.error("复制失败，请手动选择文本复制");
    }
  };

  return <Card title="配置 MCP 客户端" style={{ marginTop: 20 }}>
    <Typography.Paragraph type="secondary">选择已实测的客户端版本，填写该客户端能访问的 AgentSQL 地址。下面只生成配置说明；Agent API Key 请在客户端安全填写，不要粘贴到此页面。</Typography.Paragraph>
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      {!agentID.trim() && <Alert type="info" showIcon message="先在上方选定 Agent，再使用该 Agent 的 API Key 配置客户端。" />}
      <Radio.Group value={client} onChange={(event) => setClient(event.target.value as ClientKind)} options={[{ label: "Dify 1.17.1", value: "dify" }, { label: "MCP Inspector 0.16.8", value: "inspector" }]} optionType="button" buttonStyle="solid" />
      <Input aria-label="AgentSQL 根地址" placeholder="https://gateway.example.com" value={address} onChange={(event) => setAddress(event.target.value)} style={{ maxWidth: 560 }} />
      {address.trim() && !parsed.ok && <Alert type="warning" showIcon message={parsed.error} />}
      {parsed.ok && <>
        {client === "dify" && parsed.loopback && <Alert type="warning" showIcon message="Dify 容器内的 127.0.0.1/localhost 指向 Dify 自身；请填写 Dify API 容器可达的 AgentSQL 地址。" />}
        {parsed.insecureRemote && <Alert type="warning" showIcon message="远端 HTTP 会以明文传输 Agent API Key；请改用 HTTPS 或仅在隔离内网中使用。" />}
        <Typography.Text type="secondary">{client === "dify" ? "在 Dify 的 MCP 工具管理界面填写 URL 与两项请求头。" : "在 Inspector 选择 Streamable HTTP，只添加 Authorization 自定义请求头。"}</Typography.Text>
        <pre style={{ overflowX: "auto", padding: 16, borderRadius: 8, background: "var(--ant-color-fill-quaternary, #f5f5f5)", whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{instructions}</pre>
        <Button onClick={() => void copy()}>复制接入说明</Button>
      </>}
      <Alert type="info" showIcon message="真实验收还需允许与拒绝两条请求" description="配置保存后，在客户端检查工具发现，用该 Agent 的 Key 发起授权和越权请求，再核对持久审计。连接测试和离线预检都不能替代真实调用。" />
      <Typography.Link href="https://github.com/cuipengdba/agentsql/blob/main/docs/INTEGRATIONS.md" target="_blank" rel="noreferrer">查看 Dify 与 MCP Inspector 完整接入步骤</Typography.Link>
    </Space>
  </Card>;
}
