import { LockOutlined, SafetyCertificateFilled, UserOutlined } from "@ant-design/icons";
import { Button, Card, Form, Input, Typography } from "antd";
import { useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";

import { login, me } from "@/api/auth";
import type { LoginInput } from "@/api/types";
import { useAuthStore } from "@/store/authStore";

export function Login() {
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const storeLogin = useAuthStore((state) => state.login);
  const setUsername = useAuthStore((state) => state.setUsername);
  const clear = useAuthStore((state) => state.clear);

  if (isAuthenticated()) {
    return <Navigate to="/" replace />;
  }

  const handleSubmit = async (input: LoginInput) => {
    setLoading(true);
    try {
      const session = await login(input);
      storeLogin({ token: session.token, expiresAt: session.expires_at, username: input.username });
      const profile = await me();
      setUsername(profile.username);
      navigate("/", { replace: true });
    } catch {
      clear();
    } finally {
      setLoading(false);
    }
  };

  return (
    <main className="login-page">
      <Card className="login-card" bordered>
        <div className="login-brand">
          <span className="login-brand-mark" aria-hidden="true">
            <SafetyCertificateFilled />
          </span>
          <Typography.Title level={2}>AgentSQL 智盾</Typography.Title>
          <Typography.Paragraph>
            AI 原生数据库安全网关 · 让每一次模型访问都可控、可审计
          </Typography.Paragraph>
        </div>
        <Form<LoginInput> layout="vertical" requiredMark={false} onFinish={(values) => void handleSubmit(values)}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: "请输入用户名" }]}>
            <Input prefix={<UserOutlined />} autoComplete="username" size="large" />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: "请输入密码" }]}>
            <Input.Password prefix={<LockOutlined />} autoComplete="current-password" size="large" />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={loading} block size="large">
            登录
          </Button>
        </Form>
      </Card>
    </main>
  );
}
