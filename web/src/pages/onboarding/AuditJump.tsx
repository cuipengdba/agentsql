import { Alert, Button, Card, Input, Space, Typography } from "antd";
import { useState } from "react";
import { Link } from "react-router-dom";

export function AuditJump() {
  const [auditID, setAuditID] = useState("");
  const id = auditID.trim();
  const valid = /^[1-9]\d*$/.test(id) && Number.isSafeInteger(Number(id));

  return <Card title="核对真实调用的审计" style={{ marginTop: 20 }}>
    <Typography.Paragraph type="secondary">客户端完成允许和拒绝请求后，分别填入响应中的审计 ID，打开记录核对 Agent、数据源、决策、规则及脱敏。没有审计 ID 时先确认请求是否已通过认证并到达网关。</Typography.Paragraph>
    <Space wrap>
      <Input aria-label="审计 ID" value={auditID} onChange={(event) => setAuditID(event.target.value)} placeholder="审计 ID" maxLength={20} style={{ width: 220 }} />
      {valid ? <Link to={`/audit?focus=${encodeURIComponent(id)}`}><Button type="primary">定位审计记录</Button></Link> : <Button type="primary" disabled>定位审计记录</Button>}
      <Link to="/audit">浏览审计列表</Link>
    </Space>
    {id && !valid && <Alert type="warning" showIcon message="审计 ID 应为正整数" style={{ marginTop: 12 }} />}
  </Card>;
}
