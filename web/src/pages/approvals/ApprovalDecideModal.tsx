import { Alert, Descriptions, Input, Modal, Tag } from "antd";
import { useEffect, useState } from "react";

import type { ApprovalDecisionInput, ApprovalView } from "@/api/types";
import { approvalStatusMeta, configLabel } from "@/constants/labels";

interface ApprovalDecideModalProps {
  record: ApprovalView | null;
  decision: ApprovalDecisionInput["decision"];
  open: boolean;
  loading: boolean;
  onCancel: () => void;
  onSubmit: (comment?: string) => void;
}

export function ApprovalDecideModal({
  record,
  decision,
  open,
  loading,
  onCancel,
  onSubmit,
}: ApprovalDecideModalProps) {
  const [comment, setComment] = useState("");

  useEffect(() => {
    if (open) setComment("");
  }, [open, record?.id, decision]);

  const status = record?.status || "";
  const statusMeta = approvalStatusMeta[status as keyof typeof approvalStatusMeta];
  const approving = decision === "approve";

  return (
    <Modal
      destroyOnClose
      open={open}
      title={approving ? "通过审批" : "拒绝审批"}
      okText={approving ? "确认通过" : "确认拒绝"}
      okButtonProps={{ danger: !approving, disabled: record?.status !== "pending" }}
      cancelText="取消"
      confirmLoading={loading}
      onCancel={onCancel}
      onOk={() => onSubmit(comment.trim() || undefined)}
    >
      {record ? (
        <div className="apv-decide-content">
          <Alert
            type="info"
            showIcon
            message="本次操作只记录审批结论与留痕，不会自动重放 SQL。"
          />
          <Descriptions column={1} size="small">
            <Descriptions.Item label="审批单号"><code>{record.id}</code></Descriptions.Item>
            <Descriptions.Item label="Agent">{record.agent_id || "—"}</Descriptions.Item>
            <Descriptions.Item label="状态">
              <Tag color={statusMeta?.color || "default"}>{configLabel(approvalStatusMeta, status)}</Tag>
            </Descriptions.Item>
          </Descriptions>
          <pre className="apv-sql-block apv-sql-block-compact">{record.sql_raw || "—"}</pre>
          <label className="apv-comment-label" htmlFor="approval-comment">审批意见（可选）</label>
          <Input.TextArea
            id="approval-comment"
            value={comment}
            maxLength={1000}
            autoSize={{ minRows: 3, maxRows: 6 }}
            placeholder="补充通过或拒绝原因"
            onChange={(event) => setComment(event.target.value)}
          />
        </div>
      ) : null}
    </Modal>
  );
}
