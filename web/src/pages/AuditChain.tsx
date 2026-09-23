import { ReloadOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Descriptions, Empty, Segmented, Skeleton, Space, Tag, Typography, message } from "antd";
import type { ReactNode } from "react";
import { useCallback, useEffect, useRef, useState } from "react";

import {
  getAuditChainStatus,
  verifyAuditChain,
  type AuditChainDomain,
  type AuditChainStatus,
  type AuditChainVerificationStatus,
  type AuditChainVerifyResult,
} from "@/api/auditChain";
import { PageContainer } from "@/components/PageContainer";
import { apiErrorMessage, formatDateTime, isCanceled } from "@/pages/config/utils";

const validResult = "VALID_AT_OBSERVED_HEAD";

interface AuditChainFailure {
  status?: number;
  errorCode?: string;
  retryAfterSeconds?: number;
}

function objectValue(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null ? value as Record<string, unknown> : null;
}

export function auditChainFailure(error: unknown): AuditChainFailure {
  const candidate = objectValue(error);
  const response = objectValue(candidate?.response);
  const envelope = objectValue(response?.data);
  const detail = objectValue(envelope?.data) || envelope;
  const status = typeof response?.status === "number" ? response.status : undefined;
  const errorCode = typeof detail?.error_code === "string" ? detail.error_code : undefined;
  const retryAfterSeconds = typeof detail?.retry_after_seconds === "number" ? detail.retry_after_seconds : undefined;
  return { status, errorCode, retryAfterSeconds };
}

export type AuditChainFailureFeedback =
  | { kind: "in-progress"; message: string }
  | { kind: "rate-limited"; message: string }
  | { kind: "unavailable"; message: string }
  | { kind: "error"; message: string };

export function auditChainFailureFeedback(error: unknown): AuditChainFailureFeedback {
  const failure = auditChainFailure(error);
  if (failure.status === 409 || failure.errorCode === "CHAIN_VERIFY_IN_PROGRESS") {
    return { kind: "in-progress", message: "已有校验在进行，请稍候" };
  }
  if (failure.status === 429 || failure.errorCode === "CHAIN_VERIFY_RATE_LIMITED") {
    return {
      kind: "rate-limited",
      message: failure.retryAfterSeconds === undefined
        ? "校验过于频繁，请稍后重试"
        : `校验过于频繁，请 ${failure.retryAfterSeconds} 秒后重试`,
    };
  }
  if (failure.status === 503 || failure.errorCode === "CHAIN_VERIFIER_UNAVAILABLE") {
    return { kind: "unavailable", message: "链校验器不可用" };
  }
  return { kind: "error", message: apiErrorMessage(error, "审计链校验失败，请稍后重试") };
}

function statusColor(status: string): "success" | "processing" | "default" | "error" {
  if (status === "ACTIVE") return "success";
  if (status === "BUILDING") return "processing";
  if (status === "FAILED") return "error";
  return "default";
}

function value(value: string | number | null | undefined): ReactNode {
  return value === null || value === undefined || value === "" ? "—" : value;
}

function BreakDetails({ verification }: { verification: AuditChainVerificationStatus }) {
  const location = [
    verification.break_seq !== null && verification.break_seq !== undefined ? `seq ${verification.break_seq}` : "",
    verification.break_id !== null && verification.break_id !== undefined ? `id ${verification.break_id}` : "",
  ].filter(Boolean).join(" / ");
  return (
    <Tag color="error" data-testid="verification-result">
      {verification.break_reason || verification.result}
      {location ? `（${location}）` : ""}
    </Tag>
  );
}

function VerificationResult({ verification }: { verification: AuditChainVerificationStatus }) {
  if (!verification.result) return <>尚未校验</>;
  if (verification.result === validResult) {
    return <Tag color="success" data-testid="verification-result">{verification.result}</Tag>;
  }
  return <BreakDetails verification={verification} />;
}

function AbnormalAlert({ result }: { result: AuditChainVerifyResult | null }) {
  if (!result || result.result === validResult) return null;
  const detail = result.break
    ? `${result.break.reason}（seq ${result.break.seq} / id ${result.break.id}）`
    : result.result;
  return <Alert type="error" showIcon message="检测到链异常" description={detail} />;
}

export function AuditChainPanels({ data, verifyResult = null }: { data: AuditChainStatus; verifyResult?: AuditChainVerifyResult | null }) {
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <AbnormalAlert result={verifyResult} />
      {!verifyResult && data.verification.result && data.verification.result !== validResult ? (
        <Alert
          type="error"
          showIcon
          message="检测到链异常"
          description={data.verification.break_reason || data.verification.result}
        />
      ) : null}
      <Card title="链状态">
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2 }}>
          <Descriptions.Item label="链状态">
            <Tag color={statusColor(data.status)} data-testid="chain-status">{data.status}</Tag>
          </Descriptions.Item>
          <Descriptions.Item label="模式">{value(data.mode)}</Descriptions.Item>
          <Descriptions.Item label="head_seq">{data.head_seq}</Descriptions.Item>
          <Descriptions.Item label="head_id">{value(data.head_id)}</Descriptions.Item>
          <Descriptions.Item label="protected_since">{value(data.protected_since)}</Descriptions.Item>
          <Descriptions.Item label="genesis_at">{formatDateTime(data.genesis_at)}</Descriptions.Item>
          <Descriptions.Item label="observed_instance" span={2}>
            {data.observed_instance ? (
              <Typography.Text
                copyable={{ text: data.observed_instance }}
                ellipsis={{ tooltip: data.observed_instance }}
                style={{ display: "inline-block", maxWidth: "min(100%, 520px)", verticalAlign: "bottom" }}
              >
                {data.observed_instance}
              </Typography.Text>
            ) : "—"}
          </Descriptions.Item>
        </Descriptions>
      </Card>
      <Card title="最近校验">
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2 }}>
          <Descriptions.Item label="校验结果" span={2}>
            <VerificationResult verification={data.verification} />
          </Descriptions.Item>
          <Descriptions.Item label="last_verified_at">{formatDateTime(data.verification.last_verified_at)}</Descriptions.Item>
          <Descriptions.Item label="last_verified_head_seq">{value(data.verification.last_verified_head_seq)}</Descriptions.Item>
        </Descriptions>
      </Card>
    </Space>
  );
}

export function AuditChainNotices({ verifierUnavailable, trafficUnavailable }: {
  verifierUnavailable: boolean;
  trafficUnavailable: boolean;
}) {
  return (
    <>
      {verifierUnavailable ? <Alert type="error" showIcon message="链校验器不可用" /> : null}
      {trafficUnavailable ? (
        <Card><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="该域未启用独立审计链" /></Card>
      ) : null}
    </>
  );
}

export async function verifyAndRefreshAuditChain(
  domain: AuditChainDomain,
  onResult: (result: AuditChainVerifyResult) => void,
  refresh: (domain: AuditChainDomain) => Promise<void>,
): Promise<AuditChainVerifyResult> {
  const result = await verifyAuditChain(domain);
  onResult(result);
  await refresh(domain);
  return result;
}

export function AuditChain() {
  const [domain, setDomain] = useState<AuditChainDomain>("management");
  const [data, setData] = useState<AuditChainStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [verifying, setVerifying] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [trafficUnavailable, setTrafficUnavailable] = useState(false);
  const [verifierUnavailable, setVerifierUnavailable] = useState(false);
  const [verifyResult, setVerifyResult] = useState<AuditChainVerifyResult | null>(null);
  const mountedRef = useRef(false);
  const domainRef = useRef<AuditChainDomain>(domain);
  const loadControllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      loadControllerRef.current?.abort();
    };
  }, []);

  const loadStatus = useCallback(async (targetDomain: AuditChainDomain) => {
    loadControllerRef.current?.abort();
    const controller = new AbortController();
    loadControllerRef.current = controller;
    setLoading(true);
    setLoadFailed(false);
    setTrafficUnavailable(false);
    try {
      const status = await getAuditChainStatus(targetDomain, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || loadControllerRef.current !== controller) return;
      setData(status);
    } catch (error: unknown) {
      if (!mountedRef.current || controller.signal.aborted || isCanceled(error) || loadControllerRef.current !== controller) return;
      setData(null);
      if (targetDomain === "traffic" && auditChainFailure(error).status === 404) setTrafficUnavailable(true);
      else setLoadFailed(true);
    } finally {
      if (mountedRef.current && loadControllerRef.current === controller) {
        setLoading(false);
        loadControllerRef.current = null;
      }
    }
  }, []);

  useEffect(() => {
    void loadStatus(domain);
    return () => loadControllerRef.current?.abort();
  }, [domain, loadStatus]);

  const changeDomain = (nextDomain: AuditChainDomain) => {
    domainRef.current = nextDomain;
    setData(null);
    setVerifyResult(null);
    setVerifierUnavailable(false);
    setDomain(nextDomain);
  };

  const verify = async () => {
    const targetDomain = domain;
    setVerifying(true);
    setVerifierUnavailable(false);
    try {
      const result = await verifyAndRefreshAuditChain(targetDomain, (nextResult) => {
        if (mountedRef.current && domainRef.current === targetDomain) setVerifyResult(nextResult);
      }, async (targetDomain) => {
        if (mountedRef.current && domainRef.current === targetDomain) await loadStatus(targetDomain);
      });
      if (!mountedRef.current || domainRef.current !== targetDomain) return;
      if (result.result === validResult) void message.success("审计链校验通过");
      else void message.warning("检测到链异常");
    } catch (error: unknown) {
      if (!mountedRef.current || domainRef.current !== targetDomain || isCanceled(error)) return;
      const feedback = auditChainFailureFeedback(error);
      if (feedback.kind === "in-progress" || feedback.kind === "rate-limited") {
        void message.warning(feedback.message);
      } else if (feedback.kind === "unavailable") {
        setVerifierUnavailable(true);
      } else {
        void message.error(feedback.message);
      }
    } finally {
      if (mountedRef.current) setVerifying(false);
    }
  };

  const extra = (
    <Space wrap>
      <Segmented<AuditChainDomain>
        aria-label="审计链域"
        value={domain}
        options={["management", "traffic"]}
        onChange={changeDomain}
      />
      <Button icon={<ReloadOutlined />} loading={loading} onClick={() => {
        setVerifyResult(null);
        void loadStatus(domain);
      }}>刷新</Button>
      <Button
        type="primary"
        icon={<SafetyCertificateOutlined />}
        loading={verifying}
        disabled={trafficUnavailable || !data}
        onClick={() => void verify()}
      >
        立即校验
      </Button>
    </Space>
  );

  return (
    <PageContainer title="审计完整性链" subtitle="只读查看链状态并按需执行完整性校验" extra={extra}>
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <AuditChainNotices verifierUnavailable={verifierUnavailable} trafficUnavailable={trafficUnavailable} />
        {loadFailed ? <Alert type="error" showIcon message="审计链状态加载失败" description="请稍后刷新重试" /> : null}
        {loading && !data ? <Skeleton active /> : null}
        {data ? <AuditChainPanels data={data} verifyResult={verifyResult} /> : null}
      </Space>
    </PageContainer>
  );
}
