import { DownloadOutlined, FilePdfOutlined, ReloadOutlined } from "@ant-design/icons";
import { Alert, Button, Pagination, Space, Tooltip, Typography, message } from "antd";
import axios from "axios";
import { useCallback, useEffect, useRef, useState } from "react";

import { exportAudit, listAudit } from "@/api/audit";
import type { AuditView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";

import { AuditDetailDrawer } from "./audit/AuditDetailDrawer";
import { AuditFilters, type AppliedAuditQuery } from "./audit/AuditFilters";
import { AuditTable } from "./audit/AuditTable";

function safeTotal(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

function exportFilename(now = new Date()): string {
  const part = (value: number) => String(value).padStart(2, "0");
  return `agentsql-audit-${now.getFullYear()}${part(now.getMonth() + 1)}${part(now.getDate())}-${part(now.getHours())}${part(now.getMinutes())}.jsonl`;
}

function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    document.body.removeChild(anchor);
    URL.revokeObjectURL(url);
  }
}

export function Audit() {
  const [appliedFilters, setAppliedFilters] = useState<AppliedAuditQuery>({});
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [list, setList] = useState<AuditView[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [queryVersion, setQueryVersion] = useState(0);
  const [exporting, setExporting] = useState(false);
  const [selected, setSelected] = useState<AuditView | null>(null);
  const mountedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const requestSequenceRef = useRef(0);

  const loadPage = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = requestSequenceRef.current + 1;
    requestSequenceRef.current = sequence;
    controllerRef.current = controller;
    setLoading(true);
    setFailed(false);
    try {
      const response = await listAudit({ ...appliedFilters, page, page_size: pageSize }, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || requestSequenceRef.current !== sequence) return;
      setList((response.list || []).slice(0, pageSize));
      setTotal(safeTotal(response.total));
    } catch {
      if (!controller.signal.aborted && mountedRef.current && requestSequenceRef.current === sequence) setFailed(true);
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null;
        if (mountedRef.current) setLoading(false);
      }
    }
  }, [appliedFilters, page, pageSize, queryVersion]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      controllerRef.current = null;
      requestSequenceRef.current += 1;
    };
  }, []);

  useEffect(() => {
    void loadPage();
    return () => controllerRef.current?.abort();
  }, [loadPage]);

  const applyFilters = useCallback((query: AppliedAuditQuery) => {
    setAppliedFilters(query);
    setPage(1);
    setQueryVersion((value) => value + 1);
  }, []);

  const refresh = useCallback(() => {
    setQueryVersion((value) => value + 1);
  }, []);

  const changePage = (nextPage: number, nextPageSize: number) => {
    if (nextPageSize !== pageSize) {
      setPageSize(nextPageSize);
      setPage(1);
      return;
    }
    setPage(nextPage);
  };

  const handleExport = async () => {
    if (exporting) return;
    setExporting(true);
    try {
      const blob = await exportAudit(appliedFilters);
      if (!mountedRef.current) return;
      downloadBlob(blob, exportFilename());
      void message.success("审计 JSONL 已开始下载");
    } catch (error: unknown) {
      if (!mountedRef.current) return;
      if (axios.isAxiosError(error) && error.response?.status === 422) {
        void message.warning("导出上限 1 万行，请缩小时间范围或增加筛选");
      } else {
        void message.error("审计导出失败，请稍后重试");
      }
    } finally {
      if (mountedRef.current) setExporting(false);
    }
  };

  const toolbar = (
    <div className="audit-toolbar">
      <Space size={12}>
        <Typography.Text>共 <strong className="mono-text">{safeTotal(total).toLocaleString("zh-CN")}</strong> 条</Typography.Text>
        <Tooltip title="按当前筛选刷新">
          <Button icon={<ReloadOutlined />} loading={loading} onClick={refresh}>刷新</Button>
        </Tooltip>
      </Space>
      <Space>
        <Button icon={<DownloadOutlined />} loading={exporting} onClick={() => void handleExport()}>导出 JSONL</Button>
        <Button icon={<FilePdfOutlined />} onClick={() => void message.info("合规 PDF 报告将在后续版本提供")}>导出 PDF</Button>
      </Space>
    </div>
  );

  return (
    <PageContainer title="审计" subtitle="追溯每一次模型数据库访问的完整证据链">
      <AuditFilters onApply={applyFilters} />
      {toolbar}
      {failed && list.length > 0 ? (
        <Alert className="audit-inline-error" type="error" showIcon message="当前页刷新失败，已保留上次结果" action={<Button size="small" onClick={refresh}>重试</Button>} />
      ) : null}
      <AuditTable list={list} loading={loading} failed={failed} onView={setSelected} onRetry={refresh} />
      <div className="audit-pagination">
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
      <AuditDetailDrawer record={selected} open={selected !== null} onClose={() => setSelected(null)} />
    </PageContainer>
  );
}
