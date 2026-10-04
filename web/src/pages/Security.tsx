import { CopyOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Form, Input, Space, Typography, message } from "antd";
import { useEffect, useState } from "react";

import { authConfig, confirmMFA, disableMFA, enrollMFA, mfaStatus, type MFAEnrollment, type MFAStatus } from "@/api/auth";
import { PageContainer } from "@/components/PageContainer";

export function Security() {
  const [status, setStatus] = useState<MFAStatus | null>(null);
  const [enrollment, setEnrollment] = useState<MFAEnrollment | null>(null);
  const [available, setAvailable] = useState(false);
  const [busy, setBusy] = useState(false);

  const reload = async () => setStatus(await mfaStatus());
  useEffect(() => { void authConfig().then((value) => { setAvailable(value.mfa_enabled); if (value.mfa_enabled) void reload(); }); }, []);

  const begin = async () => {
    setBusy(true);
    try { setEnrollment(await enrollMFA()); await reload(); } finally { setBusy(false); }
  };

  const confirm = async ({ code }: { code: string }) => {
    setBusy(true);
    try { await confirmMFA(code); setEnrollment(null); await reload(); void message.success("多因素认证已启用"); } finally { setBusy(false); }
  };

  const disable = async ({ code }: { code: string }) => {
    setBusy(true);
    try { await disableMFA(code.includes("-") ? { recovery_code: code } : { code }); await reload(); void message.success("多因素认证已停用"); } finally { setBusy(false); }
  };

  return (
    <PageContainer title="登录安全" subtitle="管理当前账号的 TOTP 多因素认证和一次性恢复码。">
      <Card title={<Space><SafetyCertificateOutlined />多因素认证</Space>}>
        {!available ? <Alert type="warning" showIcon message="管理员尚未启用 MFA 功能" /> : null}
        {available && status && !status.enabled && !enrollment ? (
          <Space direction="vertical">
            <Typography.Text>使用支持 TOTP 的认证器为账号增加第二道验证。</Typography.Text>
            <Button type="primary" loading={busy} onClick={() => void begin()}>开始设置</Button>
          </Space>
        ) : null}
        {enrollment ? (
          <Space direction="vertical" size="middle" style={{ width: "100%" }}>
            <Alert type="warning" showIcon message="恢复码只显示一次" description="请离线保存这些恢复码；每枚恢复码只能使用一次。" />
            <Typography.Text strong>手动密钥</Typography.Text>
            <Input value={enrollment.secret} readOnly addonAfter={<CopyOutlined onClick={() => void navigator.clipboard.writeText(enrollment.secret)} />} />
            <Typography.Text strong>恢复码</Typography.Text>
            <pre className="mfa-recovery-codes">{enrollment.recovery_codes.join("\n")}</pre>
            <Form layout="inline" onFinish={(values) => void confirm(values)}>
              <Form.Item name="code" rules={[{ required: true, len: 6, message: "请输入 6 位动态码" }]}><Input placeholder="6 位动态码" maxLength={6} autoComplete="one-time-code" /></Form.Item>
              <Button type="primary" htmlType="submit" loading={busy}>确认启用</Button>
            </Form>
          </Space>
        ) : null}
        {available && status?.enabled ? (
          <Space direction="vertical">
            <Alert type="success" showIcon message="MFA 已启用" />
            <Form layout="inline" onFinish={(values) => void disable(values)}>
              <Form.Item name="code" rules={[{ required: true, message: "请输入动态码或恢复码" }]}><Input placeholder="动态码或恢复码" autoComplete="one-time-code" /></Form.Item>
              <Button danger htmlType="submit" loading={busy}>验证并停用</Button>
            </Form>
          </Space>
        ) : null}
      </Card>
    </PageContainer>
  );
}
