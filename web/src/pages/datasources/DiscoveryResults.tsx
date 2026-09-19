import { Checkbox, Empty, Table, Tag, Tooltip, Typography } from "antd";
import type { TableProps } from "antd";

import type {
  DiscoveryApplyStatus,
  DiscoveryFinding,
  DiscoveryResponse,
} from "@/api/discovery";
import {
  configLabel,
  discoveryApplyStatusMeta,
  discoveryCategoryMeta,
  discoveryConfidenceMeta,
} from "@/constants/labels";

export function discoveryFindingKey(finding: DiscoveryFinding): string {
  return [finding.schema, finding.table, finding.column, finding.category].join("\u0000");
}

function evidenceText(finding: DiscoveryFinding): string {
  const nameSignal = finding.signals.find((signal) => signal.name.startsWith("column_name_"));
  const nameEvidence = nameSignal?.name.includes("_strong_") ? "列名强信号" : "列名中等信号";
  if (!finding.sampled) return `${nameEvidence}；未采样`;
  if (finding.eligible_samples === 0) return `${nameEvidence}；无有效样本`;
  const support = Math.round((finding.matched_samples / finding.eligible_samples) * 100);
  return `${nameEvidence}；样本支持 ${finding.matched_samples}/${finding.eligible_samples}（${support}%）`;
}

interface DiscoveryResultsProps {
  result: DiscoveryResponse;
  selectedKeys: string[];
  statuses: ReadonlyMap<string, DiscoveryApplyStatus>;
  onSelectionChange: (keys: string[]) => void;
}

export function DiscoveryResults({ result, selectedKeys, statuses, onSelectionChange }: DiscoveryResultsProps) {
  const statusFor = (finding: DiscoveryFinding) => statuses.get(
    `${finding.table.trim()}\u0000${finding.column.trim().toLowerCase()}`,
  );
  const columns: TableProps<DiscoveryFinding>["columns"] = [
    {
      title: "表",
      key: "table",
      render: (_, finding) => <code className="dsc-source">{finding.schema}.{finding.table}</code>,
    },
    {
      title: "列",
      key: "column",
      render: (_, finding) => (
        <span className="dsc-column">
          <code>{finding.column}</code>
          <Typography.Text type="secondary">{finding.data_type}</Typography.Text>
        </span>
      ),
    },
    {
      title: "类别",
      dataIndex: "category",
      render: (value: DiscoveryFinding["category"]) => {
        const meta = discoveryCategoryMeta[value];
        return <Tag color={meta.color}>{meta.label}</Tag>;
      },
    },
    {
      title: "置信度",
      dataIndex: "confidence",
      render: (value: DiscoveryFinding["confidence"]) => {
        const meta = discoveryConfidenceMeta[value];
        return <Tag color={meta.color}>{meta.label}</Tag>;
      },
    },
    {
      title: "证据",
      key: "evidence",
      render: (_, finding) => <span className="dsc-evidence">{evidenceText(finding)}</span>,
    },
    {
      title: "一键脱敏",
      key: "applicable",
      render: (_, finding) => {
        const status = statusFor(finding);
        if (status) {
          const meta = discoveryApplyStatusMeta[status];
          return <Tag color={meta.color}>{meta.label}</Tag>;
        }
        if (finding.applicable) return finding.existing_rule ? <Tag color="blue">可选 · 已有规则</Tag> : <Tag color="success">可选</Tag>;
        return <Tooltip title={finding.reason}><Tag>仅发现</Tag></Tooltip>;
      },
    },
  ];

  return (
    <section className="dsc-results" aria-label="敏感列发现结果">
      <div className="dsc-results-heading">
        <div>
          <h3>发现结果</h3>
          <p>启发式识别可能存在误报或漏报；列表保留实际 schema.table 来源，生成草稿前请核对列用途。</p>
        </div>
        <Typography.Text type="secondary">已选 {selectedKeys.length} 项</Typography.Text>
      </div>

      <div className="dsc-result-table">
        <Table<DiscoveryFinding>
          rowKey={discoveryFindingKey}
          columns={columns}
          dataSource={result.findings}
          pagination={false}
          tableLayout="fixed"
          rowSelection={{
            selectedRowKeys: selectedKeys,
            onChange: (keys) => onSelectionChange(keys.map(String)),
            getCheckboxProps: (finding) => ({
              disabled: !finding.applicable,
              title: finding.applicable ? undefined : finding.reason,
            }),
          }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="未发现候选敏感列" /> }}
        />
      </div>

      <div className="dsc-result-cards">
        {result.findings.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="未发现候选敏感列" /> : null}
        {result.findings.map((finding) => {
          const key = discoveryFindingKey(finding);
          const status = statusFor(finding);
          return (
            <article className="dsc-result-card" key={key}>
              <div className="dsc-card-heading">
                <Checkbox
                  checked={selectedKeys.includes(key)}
                  disabled={!finding.applicable}
                  title={finding.applicable ? undefined : finding.reason}
                  onChange={(event) => onSelectionChange(event.target.checked
                    ? [...selectedKeys, key]
                    : selectedKeys.filter((selected) => selected !== key))}
                />
                <div>
                  <code>{finding.column}</code>
                  <small>{finding.schema}.{finding.table}</small>
                </div>
              </div>
              <div className="dsc-card-tags">
                <Tag color={discoveryCategoryMeta[finding.category].color}>{configLabel(discoveryCategoryMeta, finding.category)}</Tag>
                <Tag color={discoveryConfidenceMeta[finding.confidence].color}>置信度 {configLabel(discoveryConfidenceMeta, finding.confidence)}</Tag>
                {status ? <Tag color={discoveryApplyStatusMeta[status].color}>{configLabel(discoveryApplyStatusMeta, status)}</Tag> : null}
              </div>
              <p>{evidenceText(finding)}</p>
              {!finding.applicable && finding.reason ? <small className="dsc-unavailable">{finding.reason}</small> : null}
            </article>
          );
        })}
      </div>
    </section>
  );
}
