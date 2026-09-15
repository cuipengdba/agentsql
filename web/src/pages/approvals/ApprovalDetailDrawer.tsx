import { Alert, Descriptions, Drawer, Tag } from "antd";

import type { ApprovalView } from "@/api/types";
import { approvalStatusMeta, configLabel } from "@/constants/labels";
import { formatDateTime } from "@/pages/config/utils";

interface ApprovalDetailDrawerProps {
  record: ApprovalView | null;
  open: boolean;
  onClose: () => void;
}

function statusTag(status: string) {
  const meta = approvalStatusMeta[status as keyof typeof approvalStatusMeta];
  return <Tag color={meta?.color || "default"}>{configLabel(approvalStatusMeta, status)}</Tag>;
}

export function ApprovalDetailDrawer({ record, open, onClose }: ApprovalDetailDrawerProps) {
  return (
    <Drawer
      className="apv-detail-drawer"
      destroyOnClose
      open={open}
      title="审批单详情"
      width={680}
      onClose={onClose}
    >
      {record ? (
        <>
          <Alert
            className="apv-boundary-alert"
            type="info"
            showIcon
            message="v0.1 通过或拒绝只记录审批结论与留痕，不会自动重放该 SQL。"
          />
          <Descriptions bordered column={1} size="small">
            <Descriptions.Item label="审批单号"><code>{record.id}</code></Descriptions.Item>
            <Descriptions.Item label="审计 ID">{record.audit_id ?? "—"}</Descriptions.Item>
            <Descriptions.Item label="Agent">{record.agent_id || "—"}</Descriptions.Item>
            <Descriptions.Item label="状态">{statusTag(record.status)}</Descriptions.Item>
            <Descriptions.Item label="原因/备注">{record.reason || "—"}</Descriptions.Item>
            <Descriptions.Item label="审批人">{record.approver || "—"}</Descriptions.Item>
            <Descriptions.Item label="创建时间">{formatDateTime(record.created_at)}</Descriptions.Item>
            <Descriptions.Item label="决定时间">{formatDateTime(record.decided_at)}</Descriptions.Item>
            <Descriptions.Item label="更新时间">{formatDateTime(record.updated_at)}</Descriptions.Item>
          </Descriptions>
          <section className="apv-detail-section">
            <h3>待审 SQL</h3>
            <pre className="apv-sql-block">{record.sql_raw || "—"}</pre>
          </section>
        </>
      ) : null}
    </Drawer>
  );
}
