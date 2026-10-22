import { LockOutlined, SafetyCertificateFilled, SafetyOutlined, UserOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Divider, Form, Input, Segmented, Space, Typography } from "antd";
import { useEffect, useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";

import { authConfig, ldapLogin, login, me, verifyMFA } from "@/api/auth";
import type { HumanAuthConfig, LoginInput, LoginView } from "@/api/types";
import { localeText, useLocaleStore } from "@/i18n/locale";
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
  const locale = useLocaleStore((state) => state.locale);
  const setLocale = useLocaleStore((state) => state.setLocale);
  const tr = (zh: string, en: string) => localeText(zh, en, locale);

  useEffect(() => {
    let active = true;
    void authConfig().then((value) => {
      if (!active) return;
      setConfiguration(value);
      if (value.demo_admin_username && value.demo_admin_password) {
        form.setFieldsValue({ username: value.demo_admin_username, password: value.demo_admin_password });
        setDemoCredentialsLoaded(true);
      }
    }).catch(() => undefined);
    return () => { active = false; };
  }, [form]);

  if (isAuthenticated()) return <Navigate to="/" replace />;

  const finishSession = async (session: LoginView, username: string) => {
    if (!session.token || !session.expires_at) throw new Error(tr("登录响应缺少会话令牌", "Login response is missing a session token"));
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
      <button className="login-locale-button" data-no-translate type="button" onClick={() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN")} aria-label={tr("切换为英文", "Switch to Chinese")}>
        {locale === "zh-CN" ? "EN" : "中文"}
      </button>
      <Card className="login-card" bordered>
        <div className="login-brand">
          <span className="login-brand-mark" aria-hidden="true"><SafetyCertificateFilled /></span>
          <Typography.Title level={2}>{tr("AgentSQL 智盾", "AgentSQL")}</Typography.Title>
          <Typography.Paragraph>{tr("控制平面身份验证 · 最小权限 · 全程审计", "Control plane authentication · Least privilege · Complete audit trail")}</Typography.Paragraph>
        </div>
        {challenge ? (
          <>
            <Alert type="info" showIcon message={tr("需要多因素认证", "Multi-factor authentication required")} description={tr("输入认证器中的 6 位动态码，或使用一枚未使用的恢复码。每次失败后需重新验证密码。", "Enter the six-digit code from your authenticator or an unused recovery code. After each failure, verify your password again.")} style={{ marginBottom: 24 }} />
            <Form form={mfaForm} layout="vertical" requiredMark={false} onFinish={(values) => void handleMFA(values)}>
              <Form.Item name="code" label={useRecoveryCode ? tr("恢复码", "Recovery code") : tr("动态验证码", "Authentication code")} rules={[{ required: true, message: tr("请输入验证码", "Enter the code") }]}>
                <Input prefix={<SafetyOutlined />} autoComplete="one-time-code" size="large" maxLength={useRecoveryCode ? 32 : 6} />
              </Form.Item>
              <Space direction="vertical" style={{ width: "100%" }}>
                <Button type="primary" htmlType="submit" loading={loading} block size="large">{tr("验证并登录", "Verify and sign in")}</Button>
                <Button type="link" block onClick={() => { setUseRecoveryCode((value) => !value); mfaForm.resetFields(); }}>
                  {useRecoveryCode ? tr("使用动态验证码", "Use authentication code") : tr("使用恢复码", "Use recovery code")}
                </Button>
                <Button type="text" block onClick={() => setChallenge(null)}>{tr("返回登录", "Back to sign in")}</Button>
              </Space>
            </Form>
          </>
        ) : (
          <>
            {configuration.ldap_enabled ? <Segmented block value={mode} options={[{ label: tr("本地账号", "Local account"), value: "local" }, { label: tr("企业目录", "Enterprise directory"), value: "ldap" }]} onChange={(value) => setMode(value as LoginMode)} style={{ marginBottom: 20 }} /> : null}
            {demoCredentialsLoaded && mode === "local" ? <Alert type="info" showIcon message={tr("演示账号已自动填入", "Demo credentials filled in automatically")} style={{ marginBottom: 24 }} /> : null}
            <Form<LoginInput> form={form} layout="vertical" requiredMark={false} onFinish={(values) => void handleSubmit(values)}>
              <Form.Item name="username" label={tr("用户名", "Username")} rules={[{ required: true, message: tr("请输入用户名", "Enter your username") }]}>
                <Input prefix={<UserOutlined />} autoComplete="username" size="large" />
              </Form.Item>
              <Form.Item name="password" label={tr("密码", "Password")} rules={[{ required: true, message: tr("请输入密码", "Enter your password") }]}>
                <Input.Password prefix={<LockOutlined />} autoComplete="current-password" size="large" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={loading} block size="large">{mode === "ldap" ? tr("使用目录账号登录", "Sign in with directory account") : tr("登录", "Sign in")}</Button>
            </Form>
            {configuration.oidc_enabled ? <><Divider plain>{tr("或", "or")}</Divider><Button block size="large" icon={<SafetyOutlined />} onClick={() => window.location.assign("/api/v1/auth/oidc/start?return_to=/")}>{tr("使用企业单点登录", "Use enterprise single sign-on")}</Button></> : null}
          </>
        )}
      </Card>
    </main>
  );
}
