import { Alert, Card, Select, Space, Typography } from "antd";
import { useState } from "react";
import { Link } from "react-router-dom";

type Failure = {
  code: string;
  title: string;
  cause: string;
  action: string;
  path: string;
  link: string;
};

const failures: Failure[] = [
  { code: "401", title: "Agent 认证失败", cause: "密钥无效、已撤销、过期，或把管理员登录 token 用作 Agent Key。", action: "核对客户端每次请求的 Bearer Key、Agent 状态及有效期。临时任务 Key 还需检查原授权是否过期或撤销。", path: "/agents", link: "核对 Agent" },
  { code: "403", title: "管理权限不足", cause: "当前人类账号缺少该操作所需的角色权限，或资源不属于当前租户。", action: "让租户管理员核对角色、租户和操作权限；不要使用其他租户身份绕过。", path: "/roles", link: "核对角色" },
  { code: "R010", title: "对象或列权限拒绝", cause: "请求的表、列或操作超出 Agent 当前策略。", action: "在审计中核对 Agent、数据源、对象和命中规则，再按需要授予最小权限。", path: "/audit", link: "查看审计" },
  { code: "AUDIT_UNAVAILABLE", title: "审计依赖不可用", cause: "持久审计无法确认写入，网关关闭执行路径。", action: "查看 /readyz 与审计库连接、磁盘和写入延迟。恢复后重新发起允许及拒绝请求并核对持久审计。", path: "/audit", link: "查看审计" },
  { code: "AUDIT_OVERLOADED", title: "审计写入过载", cause: "审计队列或存储超过当前服务能力。", action: "查看就绪状态、审计库负载和队列积压；恢复后以新请求 ID 重试只读请求。", path: "/audit", link: "查看审计" },
  { code: "DB_PERMISSION_DENIED", title: "数据库账号权限不足", cause: "数据源使用的数据库账号没有目标对象的读取权限。", action: "由数据库管理员核对该账号的最小只读授权，并重新运行连接预检与目标 SQL。", path: "/datasources", link: "检查数据源" },
  { code: "MCP_PROTOCOL_UNSUPPORTED", title: "MCP 协议版本不匹配", cause: "客户端所选入口与发送的协议版本或会话信息不一致。", action: "核对 /mcp 与 /mcp/2026-07-28 的实际兼容性、协议头及新入口的逐请求身份。", path: "/onboarding", link: "查看接入步骤" },
];

export function FailureGuide() {
  const [code, setCode] = useState<string>();
  const selected = failures.find((item) => item.code === code);
  return <Card title="按错误码定位接入故障" style={{ marginTop: 20 }}>
    <Typography.Paragraph type="secondary">从客户端响应或审计记录中选择错误码。先确定拒绝发生在认证、授权、数据库执行还是审计阶段。</Typography.Paragraph>
    <Select showSearch allowClear value={code} onChange={setCode} placeholder="选择错误码" style={{ minWidth: 290, maxWidth: "100%" }} options={failures.map((item) => ({ value: item.code, label: `${item.code} · ${item.title}` }))} />
    {selected && <Alert type="warning" showIcon style={{ marginTop: 16 }} message={`${selected.code} · ${selected.title}`} description={<Space direction="vertical">
      <span>可能原因：{selected.cause}</span>
      <span>下一步：{selected.action}</span>
      <Link to={selected.path}>{selected.link}</Link>
    </Space>} />}
  </Card>;
}
