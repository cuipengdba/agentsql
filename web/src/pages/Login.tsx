import { LockOutlined, SafetyCertificateFilled, SafetyOutlined, UserOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Divider, Form, Input, Segmented, Space, Typography } from "antd";
import { useEffect, useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";

import { authConfig, ldapLogin, login, me, verifyMFA } from "@/api/auth";
import { fetchDemoProjection } from "@/api/health";
import type { HumanAuthConfig, LoginInput, LoginView } from "@/api/types";
import { useAuthStore } from "@/store/authStore";

type LoginMode = "local" | "ldap";

export function Login() {
  const [form] = Form.useForm<LoginInput>();
  const [mfaForm] = Form.useForm<{ code: string }>();
  const [loading, setLoading] = useState(false);
  const [configuration, setConfiguration] = useState<HumanAuthConfig>({ mfa_enabled: false, oidc_enabled: false, ldap_enabled: false });
  const [mode, setMode] = useState<LoginMode>("local");
  const [challenge, setChallenge] = useState<{ token: string; username: string } | null>(null);
  const [useRecoveryCode, setUseRecoveryCode] = useState(false);
  const [demoCredentialsLoaded, setDemoCredentialsLoaded] = useState(false);
  const navigate = useNavigate();
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const storeLogin = useAuthStore((state) => state.login);
  const setUsername = useAuthStore((state) => state.setUsername);
  const clear = useAuthStore((state) => state.clear);

  useEffect(() => {
    let active = true;
    void authConfig().then((value) => { if (active) setConfiguration(value); }).catch(() => undefined);
    void fetchDemoProjection().then((demo) => {
      if (!active || !demo?.adminUsername || !demo.adminPassword) return;
      form.setFieldsValue({ username: demo.adminUsername, password: demo.adminPassword });
      setDemoCredentialsLoaded(true);
    });
    return () => { active = false; };
  }, [form]);

  if (isAuthenticated()) return <Navigate to="/" replace />;

  const finishSession = async (session: LoginView, username: string) => {
    if (!session.token || !session.expires_at) throw new Error("登录响应缺少会话令牌");
    storeLogin({ token: session.token, expiresAt: session.expires_at, username });
    const profile = await me();
    setUsername(profile.username);
    navigate("/", { replace: true });
  };

  const handleSubmit = async (input: LoginInput) => {
    setLoading(true);
    try {
      const session = mode === "ldap" ? await ldapLogin(input) : await login(input);
      if (session.mfa_required && session.challenge_token) {
        setChallenge({ token: session.challenge_token, username: input.username });
        return;
      }
      await finishSession(session, input.username);
    } catch {
      clear();
    } finally {
      setLoading(false);
    }
  };

  const handleMFA = async ({ code }: { code: string }) => {
    if (!challenge) return;
    setLoading(true);
    try {
      const session = await verifyMFA({ challenge_token: challenge.token,
        ...(useRecoveryCode ? { recovery_code: code } : { code }) });
      await finishSession(session, challenge.username);
    } catch {
      setChallenge(null);
      mfaForm.resetFields();
    } finally {
      setLoading(false);
    }
  };

  return (
    <main className="login-page">
      <Card className="login-card" bordered>
        <div className="login-brand">
          <span className="login-brand-mark" aria-hidden="true"><SafetyCertificateFilled /></span>
          <Typography.Title level={2}>AgentSQL 智盾</Typography.Title>
          <Typography.Paragraph>控制平面身份验证 · 最小权限 · 全程审计</Typography.Paragraph>
        </div>
        {challenge ? (
          <>
            <Alert type="info" showIcon message="需要多因素认证" description="输入认证器中的 6 位动态码，或使用一枚未使用的恢复码。每次失败后需重新验证密码。" style={{ marginBottom: 24 }} />
            <Form form={mfaForm} layout="vertical" requiredMark={false} onFinish={(values) => void handleMFA(values)}>
              <Form.Item name="code" label={useRecoveryCode ? "恢复码" : "动态验证码"} rules={[{ required: true, message: "请输入验证码" }]}>
                <Input prefix={<SafetyOutlined />} autoComplete="one-time-code" size="large" maxLength={useRecoveryCode ? 32 : 6} />
              </Form.Item>
              <Space direction="vertical" style={{ width: "100%" }}>
                <Button type="primary" htmlType="submit" loading={loading} block size="large">验证并登录</Button>
                <Button type="link" block onClick={() => { setUseRecoveryCode((value) => !value); mfaForm.resetFields(); }}>
                  {useRecoveryCode ? "使用动态验证码" : "使用恢复码"}
                </Button>
                <Button type="text" block onClick={() => setChallenge(null)}>返回登录</Button>
              </Space>
            </Form>
          </>
        ) : (
          <>
            {configuration.ldap_enabled ? <Segmented block value={mode} options={[{ label: "本地账号", value: "local" }, { label: "企业目录", value: "ldap" }]} onChange={(value) => setMode(value as LoginMode)} style={{ marginBottom: 20 }} /> : null}
            {demoCredentialsLoaded && mode === "local" ? <Alert type="info" showIcon message="演示账号已自动填入" style={{ marginBottom: 24 }} /> : null}
            <Form<LoginInput> form={form} layout="vertical" requiredMark={false} onFinish={(values) => void handleSubmit(values)}>
              <Form.Item name="username" label="用户名" rules={[{ required: true, message: "请输入用户名" }]}>
                <Input prefix={<UserOutlined />} autoComplete="username" size="large" />
              </Form.Item>
              <Form.Item name="password" label="密码" rules={[{ required: true, message: "请输入密码" }]}>
                <Input.Password prefix={<LockOutlined />} autoComplete="current-password" size="large" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={loading} block size="large">{mode === "ldap" ? "使用目录账号登录" : "登录"}</Button>
            </Form>
            {configuration.oidc_enabled ? <><Divider plain>或</Divider><Button block size="large" icon={<SafetyOutlined />} onClick={() => window.location.assign("/api/v1/auth/oidc/start?return_to=/")}>使用企业单点登录</Button></> : null}
          </>
        )}
      </Card>
    </main>
  );
}
