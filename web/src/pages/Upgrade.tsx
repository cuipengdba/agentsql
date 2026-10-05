import { Alert, Card, Descriptions, Skeleton, Typography } from "antd";
import { useEffect, useState } from "react";

import { getSystemRelease, type SystemRelease } from "@/api/systemRelease";
import { PageContainer } from "@/components/PageContainer";

export function Upgrade() {
  const [release, setRelease] = useState<SystemRelease | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void getSystemRelease(controller.signal).then(setRelease).catch(() => {
      if (!controller.signal.aborted) setFailed(true);
    });
    return () => controller.abort();
  }, []);

  return (
    <PageContainer title="版本与升级" subtitle="查看当前运行版本和只读升级预检方式">
      {failed ? <Alert type="error" showIcon message="无法读取当前运行版本" /> : null}
      {!release && !failed ? <Skeleton active /> : null}
      {release ? (
        <Card title="当前实例">
          <Descriptions column={1} bordered size="small">
            <Descriptions.Item label="运行版本">{release.version}</Descriptions.Item>
            <Descriptions.Item label="升级能力">清单检查与只读预演</Descriptions.Item>
          </Descriptions>
          <Alert
            style={{ marginTop: 16 }}
            type="info"
            showIcon
            message="升级预演不会替换程序或创建备份"
            description="在服务器终端使用 agentsqlctl upgrade check --manifest-url <HTTPS 清单 URL>，再用 agentsqlctl upgrade apply --manifest-url <HTTPS 清单 URL> --backup-dir <新目录> --config <配置文件> --dry-run。实际安装仍需独立、经过审核的运维流程。"
          />
          <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>
            SHA-256 只校验下载内容与清单一致，不证明发布者身份。请从可信渠道核对清单和发布来源。
          </Typography.Paragraph>
        </Card>
      ) : null}
    </PageContainer>
  );
}
