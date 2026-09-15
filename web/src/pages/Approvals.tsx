import { CheckOutlined, CloseOutlined, CopyOutlined, ReloadOutlined } from "@ant-design/icons";
import { Alert, Button, Empty, Pagination, Select, Space, Table, Tag, Tooltip, Typography, message } from "antd";
import type { TableProps } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";

import { decideApproval, listApprovals } from "@/api/approvals";
import type { ApprovalDecisionInput, ApprovalView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { approvalStatusMeta, configLabel } from "@/constants/labels";
import { apiErrorMessage, copyText, formatDateTime, httpStatus, isCanceled } from "@/pages/config/utils";

import { ApprovalDecideModal } from "@/pages/approvals/ApprovalDecideModal";
import { ApprovalDetailDrawer } from "@/pages/approvals/ApprovalDetailDrawer";

interface PendingDecision {
  record: ApprovalView;
  decision: ApprovalDecisionInput["decision"];
}

function safeTotal(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

export function Approvals() {
  const [status, setStatus] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [list, setList] = useState<ApprovalView[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [queryVersion, setQueryVersion] = useState(0);
  const [selected, setSelected] = useState<ApprovalView | null>(null);
  const [pendingDecision, setPendingDecision] = useState<PendingDecision | null>(null);
  const [deciding, setDeciding] = useState(false);
  const mountedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const requestSequenceRef = useRef(0);
  const mutationSequenceRef = useRef(0);

  const loadPage = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = ++requestSequenceRef.current;
    controllerRef.current = controller;
    setLoading(true);
    setFailed(false);
    try {
      const response = await listApprovals({
        status: status || undefined,
        page,
        page_size: pageSize,
      });
      if (!mountedRef.current || controller.signal.aborted || requestSequenceRef.current !== sequence) return;
      setList((response.list || []).slice(0, pageSize));
      setTotal(safeTotal(response.total));
    } catch (error: unknown) {
      if (!controller.signal.aborted && mountedRef.current && requestSequenceRef.current === sequence && !isCanceled(error)) {
        setFailed(true);
      }
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null;
        if (mountedRef.current) setLoading(false);
      }
    }
  }, [page, pageSize, queryVersion, status]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      controllerRef.current = null;
      requestSequenceRef.current += 1;
      mutationSequenceRef.current += 1;
    };
  }, []);

  useEffect(() => {
    void loadPage();
    return () => controllerRef.current?.abort();
  }, [loadPage]);

  const refresh = useCallback(() => {
    setQueryVersion((value) => value + 1);
  }, []);

  const changeStatus = (nextStatus: string) => {
    setStatus(nextStatus);
    setPage(1);
  };

  const changePage = (nextPage: number, nextPageSize: number) => {
    if (nextPageSize !== pageSize) {
      setPageSize(nextPageSize);
      setPage(1);
      return;
    }
    setPage(nextPage);
  };

  const openDecision = (record: ApprovalView, decision: ApprovalDecisionInput["decision"]) => {
    if (record.status !== "pending") return;
    setPendingDecision({ record, decision });
  };

  const submitDecision = async (comment?: string) => {
    if (!pendingDecision || pendingDecision.record.status !== "pending" || deciding) return;
    const sequence = ++mutationSequenceRef.current;
    setDeciding(true);
    try {
      const input: ApprovalDecisionInput = {
        decision: pendingDecision.decision,
        ...(comment ? { comment } : {}),
      };
      await decideApproval(pendingDecision.record.id, input);
      if (!mountedRef.current || mutationSequenceRef.current !== sequence) return;
      void message.success(pendingDecision.decision === "approve" ? "审批已通过" : "审批已拒绝");
      setPendingDecision(null);
      refresh();
    } catch (error: unknown) {
      if (!mountedRef.current || mutationSequenceRef.current !== sequence || isCanceled(error)) return;
      const content = apiErrorMessage(error, "审批提交失败，请稍后重试");
      if (httpStatus(error) === 409) {
        void message.warning(content);
        setPendingDecision(null);
        refresh();
      } else {
        void message.error(content);
      }
    } finally {
      if (mountedRef.current && mutationSequenceRef.current === sequence) setDeciding(false);
    }
  };

  const copyApprovalID = async (id: string) => {
    try {
      await copyText(id);
      if (mountedRef.current) void message.success("审批单号已复制");
    } catch {
      if (mountedRef.current) void message.error("复制失败，请手动复制");
    }
  };

  const columns: TableProps<ApprovalView>["columns"] = [
    {
      title: "审批单号",
      dataIndex: "id",
      width: 190,
      render: (value: string) => (
        <Space size={4}>
          <code className="apv-id">{value}</code>
          <Button
            type="text"
            size="small"
            icon={<CopyOutlined />}
            title="复制审批单号"
            onClick={(event) => {
              event.stopPropagation();
              void copyApprovalID(value);
            }}
          />
        </Space>
      ),
    },
    { title: "Agent", dataIndex: "agent_id", width: 150, render: (value: string | null | undefined) => value || "—" },
    {
      title: "状态",
      dataIndex: "status",
      width: 100,
      render: (value: string) => {
        const meta = approvalStatusMeta[value as keyof typeof approvalStatusMeta];
        return <Tag color={meta?.color || "default"}>{configLabel(approvalStatusMeta, value)}</Tag>;
      },
    },
    {
      title: "待审 SQL",
      dataIndex: "sql_raw",
      width: 280,
      ellipsis: true,
      render: (value: string | null | undefined) => value ? (
        <Tooltip title={<pre className="apv-tooltip-sql">{value}</pre>}>
          <code className="apv-table-sql">{value}</code>
        </Tooltip>
      ) : "—",
    },
    { title: "原因/备注", dataIndex: "reason", width: 190, ellipsis: true, render: (value: string | null | undefined) => value || "—" },
    { title: "审批人", dataIndex: "approver", width: 120, render: (value: string | null | undefined) => value || "—" },
    { title: "创建时间", dataIndex: "created_at", width: 170, render: (value: string) => formatDateTime(value) },
    { title: "决定时间", dataIndex: "decided_at", width: 170, render: (value: string | null | undefined) => formatDateTime(value) },
    {
      title: "操作",
      key: "actions",
      fixed: "right",
      width: 130,
      render: (_, record) => record.status === "pending" ? (
        <Space size={4}>
          <Button
            type="link"
            size="small"
            icon={<CheckOutlined />}
            onClick={(event) => {
              event.stopPropagation();
              openDecision(record, "approve");
            }}
          >通过</Button>
          <Button
            danger
            type="link"
            size="small"
            icon={<CloseOutlined />}
            onClick={(event) => {
              event.stopPropagation();
              openDecision(record, "reject");
            }}
          >拒绝</Button>
        </Space>
      ) : "—",
    },
  ];

  return (
    <PageContainer
      title="审批"
      subtitle="模型触发的转人工审批单；v0.1 记录审批结论与留痕，通过后不会自动重放 SQL（完整审批流在后续版本）"
    >
      <div className="apv-toolbar">
        <Space wrap>
          <Typography.Text>状态</Typography.Text>
          <Select
            className="apv-status-filter"
            value={status}
            options={[
              { label: "全部", value: "" },
              ...Object.entries(approvalStatusMeta).map(([value, meta]) => ({ label: meta.label, value })),
            ]}
            onChange={changeStatus}
          />
          <Typography.Text>共 <strong className="mono-text">{safeTotal(total).toLocaleString("zh-CN")}</strong> 条</Typography.Text>
        </Space>
        <Tooltip title="刷新当前页">
          <Button icon={<ReloadOutlined />} loading={loading} disabled={loading} onClick={refresh}>刷新</Button>
        </Tooltip>
      </div>
      {failed ? (
        <Alert
          className="apv-inline-error"
          type="error"
          showIcon
          message={list.length > 0 ? "当前页刷新失败，已保留上次结果" : "审批单加载失败"}
          action={<Button size="small" onClick={refresh}>重试</Button>}
        />
      ) : null}
      <Table<ApprovalView>
        className="apv-table"
        rowKey="id"
        columns={columns}
        dataSource={list}
        loading={loading}
        pagination={false}
        scroll={{ x: 1650 }}
        locale={{ emptyText: <Empty description="暂无审批单" /> }}
        onRow={(record) => ({
          className: "apv-clickable-row",
          onClick: () => setSelected(record),
        })}
      />
      <div className="apv-pagination">
        <Pagination
          current={page}
          pageSize={pageSize}
          total={safeTotal(total)}
          pageSizeOptions={[20, 50, 100]}
          showSizeChanger
          showQuickJumper
          showTotal={(value) => `共 ${safeTotal(value).toLocaleString("zh-CN")} 条`}
          onChange={changePage}
        />
      </div>
      <ApprovalDetailDrawer record={selected} open={selected !== null} onClose={() => setSelected(null)} />
      <ApprovalDecideModal
        record={pendingDecision?.record || null}
        decision={pendingDecision?.decision || "approve"}
        open={pendingDecision !== null}
        loading={deciding}
        onCancel={() => { if (!deciding) setPendingDecision(null); }}
        onSubmit={(comment) => void submitDecision(comment)}
      />
    </PageContainer>
  );
}
