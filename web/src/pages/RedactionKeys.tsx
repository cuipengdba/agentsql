import { Alert, Card, Empty, Skeleton, Space, Table, Tag, Typography } from "antd";
import type { TableProps } from "antd";
import { useEffect, useMemo, useState } from "react";

import { getRedactionKeys, type RedactionKeyVersion, type RedactionKeysResponse } from "@/api/redactionKeys";
import { PageContainer } from "@/components/PageContainer";
import { formatDateTime, isCanceled } from "@/pages/config/utils";

const stateLabels: Record<string, string> = { active: "当前登记 active", standby: "预备 standby", legacy: "历史 legacy", retired: "已退役 retired" };

function Commitment({ value }: { value: string }) {
  return <Typography.Text className="redaction-commitment" code copyable={{ text: value }}>{value || "—"}</Typography.Text>;
}

function driftCommand(kind: string, version?: number): string {
  if (kind === "active_not_registered") return `agentsqlctl redaction-key registry-mark-active --id ${version || "<N>"} --config <yaml>`;
  return "agentsqlctl redaction-key reconcile --config <yaml>";
}

export function RedactionKeys() {
  const [data, setData] = useState<RedactionKeysResponse | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void getRedactionKeys(controller.signal).then(setData).catch((error: unknown) => { if (!isCanceled(error)) setFailed(true); });
    return () => controller.abort();
  }, []);
  return (
    <PageContainer title="脱敏密钥" subtitle="只读查看当前进程装配与登记状态；密钥材料不会进入控制台">
      <Alert type="info" showIcon message="登记为预备（standby）不代表运行时已切换" description="当前版本采用重启式计划切换；登记操作只更新控制面状态，运行进程仍使用启动时装配的 active_version。" />
      {failed ? <Alert type="error" showIcon message="脱敏密钥状态加载失败" /> : null}
      {!data && !failed ? <Skeleton active /> : null}
      {data ? <RedactionKeysView data={data} /> : null}
    </PageContainer>
  );
}

export function RedactionKeysView({ data }: { data: RedactionKeysResponse }) {
  const drift = useMemo(() => [...(data.observed.drift?.warnings || []), ...(data.observed.drift?.information || [])], [data]);
  const columns: TableProps<RedactionKeyVersion>["columns"] = [
    { title: "版本", dataIndex: "id", width: 90, render: (id: string) => <code>{id}</code> },
    { title: "状态", dataIndex: "state", width: 170, render: (state: string) => <Tag color={state === "active" ? "success" : state === "retired" ? "default" : "processing"}>{stateLabels[state] || state}</Tag> },
    { title: "Commitment", dataIndex: "commitment", render: (value: string) => <Commitment value={value} /> },
    { title: "配置 revision", dataIndex: "config_revision", width: 180, render: (value: string) => value || "—" },
    { title: "更新时间", dataIndex: "updated_at", width: 190, render: (value: string) => formatDateTime(value) },
    { title: "激活时间", dataIndex: "activated_at", width: 190, render: (value?: string | null) => value ? formatDateTime(value) : "—" },
    { title: "退役时间", dataIndex: "retired_at", width: 190, render: (value?: string | null) => value ? formatDateTime(value) : "—" },
  ];
  return <>
        {drift.length ? <Alert type={data.observed.ready ? "warning" : "error"} showIcon message="检测到脱敏密钥状态漂移" description={<Space direction="vertical" size={6}>{drift.map((item) => <div key={`${item.number}-${item.kind}-${item.version || 0}`}><strong>#{item.number} {item.kind}</strong>：{item.message}<br /><Typography.Text code copyable>{driftCommand(item.kind, item.version)}</Typography.Text></div>)}</Space>} /> : null}
        <div className="redaction-key-grid">
          <Card title="进程观察" className="redaction-key-card">
            <dl className="redaction-observed"><div><dt>active_version</dt><dd><Tag color="success">v{data.observed.active_version || "—"}</Tag></dd></div><div><dt>密钥模式</dt><dd>{data.observed.mode || data.observed.status}</dd></div><div><dt>revision</dt><dd><Commitment value={data.observed.revision || ""} /></dd></div></dl>
            <Table size="small" rowKey="id" pagination={false} dataSource={data.observed.keys || []} columns={[{ title: "id", dataIndex: "id", width: 70 }, { title: "commitment", dataIndex: "commitment", render: (value: string) => <Commitment value={value} /> }]} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="进程未观察到 manifest 密钥" /> }} />
          </Card>
          <Card title="登记状态" className="redaction-key-card redaction-registry-card"><Table rowKey="id" size="small" pagination={false} scroll={{ x: 1200 }} columns={columns} dataSource={data.registered || []} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无登记版本，请运行 reconcile" /> }} /></Card>
        </div>
      </>;
}
