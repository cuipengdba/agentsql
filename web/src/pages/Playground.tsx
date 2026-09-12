import { RedoOutlined } from "@ant-design/icons";
import { Alert, Button, Segmented, Space, Tag } from "antd";
import { useMemo, useState } from "react";

import { PageContainer } from "@/components/PageContainer";
import { StageFlow } from "@/components/stageflow/StageFlow";
import { stageSampleList, stageSamples, type StageSampleKey } from "@/components/stageflow/stageSamples";
import { getDecisionMeta } from "@/constants/labels";

export function Playground() {
  const [sampleKey, setSampleKey] = useState<StageSampleKey>("allow");
  const [replayKey, setReplayKey] = useState(0);
  const sample = stageSamples[sampleKey];
  const decision = getDecisionMeta(sample.data.decision);
  const encodedData = useMemo(() => JSON.stringify(sample.data, null, 2), [sample.data]);

  const changeSample = (next: StageSampleKey) => {
    setSampleKey(next);
    setReplayKey((value) => value + 1);
  };

  return (
    <PageContainer title="演示台" subtitle="一条 SQL 的六段安全检查">
      <Alert className="stage-preview-alert" type="info" showIcon message="组件预览·T21 将接入真实模拟 AI 请求" />
      <div className="stage-preview-toolbar">
        <Segmented<StageSampleKey>
          value={sampleKey}
          options={stageSampleList.map((item) => ({ label: item.label, value: item.key }))}
          onChange={changeSample}
        />
        <Button icon={<RedoOutlined />} onClick={() => setReplayKey((value) => value + 1)}>重播</Button>
      </div>
      <section className="stage-preview-sql" aria-label="样例 SQL">
        <Space size={10} wrap>
          <Tag color={decision.tagColor}>{decision.label}</Tag>
          <code>{sample.sql}</code>
        </Space>
      </section>
      <StageFlow data={sample.data} autoPlay replayKey={replayKey} />
      <details className="stage-preview-json">
        <summary>查看 StageFlowData</summary>
        <pre>{encodedData}</pre>
      </details>
    </PageContainer>
  );
}
