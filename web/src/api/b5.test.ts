import { beforeEach, describe, expect, it, vi } from "vitest";

const { requestMock } = vi.hoisted(() => ({ requestMock: vi.fn() }));
vi.mock("./client", () => ({ request: requestMock }));

import { confirmB5Discard, getB5Sessions, getB5Status, reconcileB5 } from "./b5";

describe("B5 admin API", () => {
  beforeEach(() => requestMock.mockReset());

  it("uses the capability endpoint for flag-off navigation", async () => {
    requestMock.mockResolvedValue({ enabled: false, state: "FEATURE_OFF", reason: "B5_SESSIONS_FEATURE_OFF", ready: true });
    await expect(getB5Status()).resolves.toMatchObject({ enabled: false });
    expect(requestMock).toHaveBeenCalledWith(expect.objectContaining({ method: "GET", url: "/b5/status", suppressErrorMessage: true }));
  });

  it("forwards pagination and filtering", async () => {
    requestMock.mockResolvedValue({ total: 0, page: 2, page_size: 20, list: [] });
    await getB5Sessions({ page: 2, page_size: 20, status: "ACTIVE", owner: "node-a" });
    expect(requestMock).toHaveBeenCalledWith(expect.objectContaining({ params: expect.objectContaining({ page: 2, status: "ACTIVE", owner: "node-a" }) }));
  });

  it("requires an explicit confirmation payload for operations", async () => {
    requestMock.mockResolvedValue({});
    await confirmB5Discard("lease/a");
    await reconcileB5("tx-1", "ds-1");
    expect(requestMock).toHaveBeenNthCalledWith(1, expect.objectContaining({ data: { confirm: true }, url: "/b5/quarantine/lease%2Fa/confirm-discard" }));
    expect(requestMock).toHaveBeenNthCalledWith(2, expect.objectContaining({ data: { transaction_id: "tx-1", datasource_id: "ds-1", confirm: true } }));
  });
});
