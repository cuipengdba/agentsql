import { Alert, Card, Descriptions, Skeleton, Space, Typography } from "antd";
import { useEffect, useState } from "react";

import { getSystemRelease, type SystemRelease } from "@/api/systemRelease";
import { PageContainer } from "@/components/PageContainer";
import { useLocaleStore } from "@/i18n/locale";

export function Upgrade() {
  const locale = useLocaleStore((state) => state.locale);
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
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
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
        <Card title="离线发行包验签">
          <Typography.Paragraph>
            将完整的 15 项发行资产放入同一目录。公钥须从独立可信渠道获取，保存在该目录之外；明确指定要安装的版本，验签通过后再执行离线安装。
          </Typography.Paragraph>
          <Typography.Paragraph code copyable>
            {`agentsqlctl release verify --directory ./release-assets --public-key ./trusted-release-key.json --version ${/^v\d+\.\d+\.\d+$/.test(release.version) ? release.version : "v4.0.0"}`}
          </Typography.Paragraph>
          <Typography.Paragraph type="secondary">
            验签会核对发行清单、三份 Ed25519 签名及源码来源，不下载、解包或运行文件。校验期间及安装前请保持目录内容不变。
          </Typography.Paragraph>
          <Typography.Link href={`https://github.com/cuipengdba/agentsql/blob/main/docs/${locale === "en-US" ? "en/" : ""}v4.0.0-delivery.md`} target="_blank" rel="noreferrer">
            离线交付与跨版本升级指南
          </Typography.Link>
        </Card>
        <Card title="导出诊断包">
          <Typography.Paragraph>
            在服务器上导出只读诊断，便于排查元数据库和审计库的版本及可用性。输出只含固定检查结果和产品能力证据，不包含连接串、凭据、SQL、日志或业务数据。
          </Typography.Paragraph>
          <Typography.Paragraph code copyable>
            agentsqlctl support-bundle --config ./config.yaml --output ./support-new.json
          </Typography.Paragraph>
          <Typography.Paragraph type="secondary">
            输出路径必须是新文件；已有文件和符号链接会被拒绝。返回码 0 表示就绪，2 表示待迁移，1 表示检查受阻；检查受阻时也会保存诊断结果。
          </Typography.Paragraph>
        </Card>
        </Space>
      ) : null}
    </PageContainer>
  );
}
