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
            message="批准后，原 Agent 可用 execute_approved 和审批单号执行一次；委托审批单还要求原申请用户的有效身份。执行前会重新检查权限、规则和数据源。旧审批单不可执行。"
          />
          <Descriptions bordered column={1} size="small">
            <Descriptions.Item label="审批单号"><code>{record.id}</code></Descriptions.Item>
            <Descriptions.Item label="审计 ID">{record.audit_id ?? "—"}</Descriptions.Item>
            <Descriptions.Item label="Agent">{record.agent_id || "—"}</Descriptions.Item>
            <Descriptions.Item label="委托申请用户">{record.requester_user_id || "—"}</Descriptions.Item>
            <Descriptions.Item label="数据源">{record.datasource_id || "—"}</Descriptions.Item>
            <Descriptions.Item label="状态">{statusTag(record.status)}</Descriptions.Item>
            <Descriptions.Item label="触发原因">{record.reason || "—"}</Descriptions.Item>
            <Descriptions.Item label="申请说明">{record.request_reason || "—"}</Descriptions.Item>
            <Descriptions.Item label="审批备注">{record.decision_comment || "—"}</Descriptions.Item>
            <Descriptions.Item label="审批人">{record.approver || "—"}</Descriptions.Item>
            <Descriptions.Item label="创建时间">{formatDateTime(record.created_at)}</Descriptions.Item>
            <Descriptions.Item label="决定时间">{formatDateTime(record.decided_at)}</Descriptions.Item>
            <Descriptions.Item label="有效期至">{formatDateTime(record.expires_at)}</Descriptions.Item>
            <Descriptions.Item label="认领时间">{formatDateTime(record.claimed_at)}</Descriptions.Item>
            <Descriptions.Item label="完成时间">{formatDateTime(record.finished_at)}</Descriptions.Item>
            <Descriptions.Item label="执行审计 ID">{record.execution_audit_id ?? "—"}</Descriptions.Item>
            <Descriptions.Item label="结果代码">{record.outcome_code || "—"}</Descriptions.Item>
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
