import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { requestMock } = vi.hoisted(() => ({ requestMock: vi.fn() }));

vi.mock("@/api/client", () => ({ request: requestMock }));

import {
  getAuditChainStatus,
  type AuditChainStatus,
  type AuditChainVerifyResult,
} from "@/api/auditChain";
import {
  AuditChainNotices,
  AuditChainPanels,
  auditChainFailureFeedback,
  verifyAndRefreshAuditChain,
} from "./AuditChain";

const activeStatus: AuditChainStatus = {
  chain_id: "management",
  status: "ACTIVE",
  mode: "hmac-sha256",
  head_seq: 42,
  head_id: 1042,
  protected_since: 1,
  genesis_at: "2026-09-24T08:00:00Z",
  observed_instance: "instance-management-0123456789abcdef",
  verification: {
    result: "VALID_AT_OBSERVED_HEAD",
    last_verified_at: "2026-09-24T09:00:00Z",
    last_verified_head_seq: 42,
  },
};

function axiosError(status: number, errorCode: string, retryAfterSeconds?: number): unknown {
  return {
    response: {
      status,
      data: {
        code: status,
        msg: "request failed",
        data: { error_code: errorCode, retry_after_seconds: retryAfterSeconds },
      },
    },
  };
}

describe("AuditChain", () => {
  beforeEach(() => requestMock.mockReset());

  it("renders the management ACTIVE status and latest valid verification", () => {
    const html = renderToStaticMarkup(<AuditChainPanels data={activeStatus} />);
    expect(html).toContain("链状态");
    expect(html).toContain("ACTIVE");
    expect(html).toContain("hmac-sha256");
    expect(html).toContain("42");
    expect(html).toContain("VALID_AT_OBSERVED_HEAD");
  });

  it("renders DISABLED with a default tag and no verification record", () => {
    const html = renderToStaticMarkup(
      <AuditChainPanels data={{ ...activeStatus, status: "DISABLED", verification: {} }} />,
    );
    expect(html).toContain("DISABLED");
    expect(html).toContain("ant-tag-default");
    expect(html).toContain("尚未校验");
  });

  it("verifies successfully, publishes the result, and refreshes the same domain", async () => {
    const result: AuditChainVerifyResult = {
      result: "VALID_AT_OBSERVED_HEAD",
      head_seq: 42,
      total: 42,
      unchained: 0,
    };
    requestMock.mockResolvedValueOnce(result);
    const onResult = vi.fn();
    const refresh = vi.fn(async () => undefined);

    await expect(verifyAndRefreshAuditChain("management", onResult, refresh)).resolves.toEqual(result);
    expect(requestMock).toHaveBeenCalledWith(expect.objectContaining({
      method: "POST",
      url: "/audit-chain/verify",
      params: { domain: "management" },
    }));
    expect(onResult).toHaveBeenCalledWith(result);
    expect(refresh).toHaveBeenCalledWith("management");
  });

  it("highlights a broken chain and its self_mismatch location", () => {
    const broken: AuditChainVerifyResult = {
      result: "INVALID",
      head_seq: 8,
      total: 10,
      unchained: 0,
      break: { seq: 7, id: 107, reason: "self_mismatch" },
    };
    const html = renderToStaticMarkup(<AuditChainPanels data={activeStatus} verifyResult={broken} />);
    expect(html).toContain("检测到链异常");
    expect(html).toContain("self_mismatch");
    expect(html).toContain("seq 7");
    expect(html).toContain("id 107");
  });

  it("shows the retry interval from a 429 Axios response", () => {
    const feedback = auditChainFailureFeedback(axiosError(429, "CHAIN_VERIFY_RATE_LIMITED", 120));
    expect(feedback.kind).toBe("rate-limited");
    expect(feedback.message).toContain("120 秒");
  });

  it("maps 409 and 503 to their dedicated UI feedback", () => {
    expect(auditChainFailureFeedback(axiosError(409, "CHAIN_VERIFY_IN_PROGRESS")).message)
      .toContain("已有校验在进行");
    const unavailable = auditChainFailureFeedback(axiosError(503, "CHAIN_VERIFIER_UNAVAILABLE"));
    expect(unavailable).toEqual({ kind: "unavailable", message: "链校验器不可用" });
    expect(renderToStaticMarkup(
      <AuditChainNotices verifierUnavailable trafficUnavailable={false} />,
    )).toContain("链校验器不可用");
  });

  it("renders the combined-deployment explanation for traffic 404", () => {
    const html = renderToStaticMarkup(
      <AuditChainNotices verifierUnavailable={false} trafficUnavailable />,
    );
    expect(html).toContain("该域未启用独立审计链");
  });

  it("requests status with domain=traffic after the domain changes", async () => {
    requestMock.mockResolvedValueOnce({ ...activeStatus, chain_id: "traffic" });
    await getAuditChainStatus("traffic");
    expect(requestMock).toHaveBeenCalledWith(expect.objectContaining({
      method: "GET",
      url: "/audit-chain/status",
      params: { domain: "traffic" },
    }));
  });
});
