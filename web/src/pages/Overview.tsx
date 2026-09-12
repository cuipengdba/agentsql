import { SafetyCertificateFilled } from "@ant-design/icons";
import { Card, Space, Typography } from "antd";

import { PageContainer } from "@/components/PageContainer";
import { useAuthStore } from "@/store/authStore";

export function Overview() {
  const username = useAuthStore((state) => state.username);

  return (
    <PageContainer title="总览" subtitle="数据库访问安全态势">
      <Card className="welcome-card" bordered>
        <Space size={16} align="start">
          <div className="welcome-mark" aria-hidden="true">
            <SafetyCertificateFilled />
          </div>
          <div>
            <Typography.Title level={3}>AgentSQL 智盾控制台</Typography.Title>
            <Typography.Paragraph type="secondary">
              当前管理员：<span className="mono-text">{username || "-"}</span>
            </Typography.Paragraph>
          </div>
        </Space>
      </Card>
    </PageContainer>
  );
}
