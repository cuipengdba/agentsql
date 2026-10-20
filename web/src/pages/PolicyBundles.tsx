import { Alert, Button, Card, Descriptions, Input, Modal, Space, Table, Tag, Typography, message } from "antd";
import { useCallback, useEffect, useState } from "react";

import {
  applyPolicyBundleProposal, currentPolicyBundle, getPolicyBundleProposal, planPolicyBundle,
  policyBundleStatus, proposePolicyBundle, reviewPolicyBundleProposal,
  type PolicyBundle, type PolicyBundlePlan, type PolicyBundleProposal,
  type PolicyBundleSnapshot, type PolicyBundleStatus,
} from "@/api/policyBundles";
import { PageContainer } from "@/components/PageContainer";

const { Text } = Typography;

function parseBundle(text: string): PolicyBundle {
  const value: unknown = JSON.parse(text);
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("策略包必须是 JSON 对象");
  const bundle = value as Record<string, unknown>;
  if (bundle.schema_version !== "agentsql.policy-bundle/v1" || typeof bundle.tenant_id !== "string" || !Array.isArray(bundle.policies)) {
    throw new Error("策略包格式不正确");
  }
  return bundle as unknown as PolicyBundle;
}

export function PolicyBundles() {
  const [snapshot, setSnapshot] = useState<PolicyBundleSnapshot | null>(null);
  const [status, setStatus] = useState<PolicyBundleStatus | null>(null);
  const [jsonText, setJsonText] = useState("");
  const [replayText, setReplayText] = useState("[]");
  const [plan, setPlan] = useState<PolicyBundlePlan | null>(null);
  const [proposalID, setProposalID] = useState("");
  const [proposal, setProposal] = useState<PolicyBundleProposal | null>(null);
  const [proposalPlan, setProposalPlan] = useState<PolicyBundlePlan | null>(null);
  const [proposalStale, setProposalStale] = useState(false);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    const [nextSnapshot, nextStatus] = await Promise.all([currentPolicyBundle(), policyBundleStatus()]);
    setSnapshot(nextSnapshot);
    setStatus(nextStatus);
    return nextSnapshot;
  }, []);

  useEffect(() => {
    let active = true;
    void Promise.all([currentPolicyBundle(), policyBundleStatus()]).then(([nextSnapshot, nextStatus]) => {
      if (!active) return;
      setSnapshot(nextSnapshot);
      setStatus(nextStatus);
      setJsonText(JSON.stringify(nextSnapshot.bundle, null, 2));
    });
    return () => { active = false; };
  }, []);

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    try { await action(); } catch (error) {
      // The HTTP client displays API errors; local integrity failures need a
      // visible message as well.
      if (error instanceof Error && error.message === "提案目标摘要不一致") void message.error(error.message);
    } finally { setBusy(false); }
  };

  const download = () => {
    if (!snapshot) return;
    const url = URL.createObjectURL(new Blob([JSON.stringify(snapshot.bundle, null, 2) + "\n"], { type: "application/json" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `agentsql-policy-bundle-${snapshot.bundle.tenant_id}.json`;
    link.click();
    URL.revokeObjectURL(url);
  };

  const inspect = async () => {
    if (!proposalID.trim()) { void message.warning("请输入提案 ID"); return; }
    await run(async () => {
      const [nextProposal, current] = await Promise.all([getPolicyBundleProposal(proposalID.trim()), currentPolicyBundle()]);
      setProposal(nextProposal);
      setProposalPlan(null);
      const stale = nextProposal.base_digest !== current.digest;
      setProposalStale(stale);
      if (!stale) {
        const checked = await planPolicyBundle(nextProposal.base_digest, nextProposal.desired);
        if (checked.desired_digest !== nextProposal.desired_digest) throw new Error("提案目标摘要不一致");
        setProposalPlan(checked);
      }
    });
  };

  const review = async (approve: boolean) => {
    if (!proposal) return;
    await run(async () => {
      setProposal(await reviewPolicyBundleProposal(proposal.id, approve));
      void message.success(approve ? "提案已批准" : "提案已驳回");
    });
  };

  const apply = () => {
    if (!proposal) return;
    Modal.confirm({
      title: "执行已批准的策略变更？",
      content: "服务端将重新检查租户、源摘要、审批人和策略约束；任何冲突都会整笔回滚。",
      onOk: async () => {
        setBusy(true);
        try {
          await applyPolicyBundleProposal(proposal.id);
          setProposal(await getPolicyBundleProposal(proposal.id));
          const next = await refresh();
          setJsonText(JSON.stringify(next.bundle, null, 2));
          setPlan(null);
          void message.success("策略变更已应用");
        } finally { setBusy(false); }
      },
    });
  };

  return (
    <PageContainer title="策略包治理" subtitle="导出、评审并原子应用同一租户的表级对象策略" extra={<Space><Button onClick={download} disabled={!snapshot}>导出当前策略包</Button><Button onClick={() => void run(async () => { await refresh(); setPlan(null); })}>刷新状态</Button></Space>}>
      <Space direction="vertical" size="large" style={{ width: "100%" }}>
        <Alert type="info" showIcon message="当前支持范围" description="单租户最多 128 条策略、每次最多 32 项变更；仅可变更无 B2 绑定、列权限、行过滤或旧版异常标记的表级 allow/deny 策略。计划只比较元数据，不代表业务 SQL 已通过网关校验。" />
        {status?.state === "drifted" ? <Alert type="warning" showIcon message="策略偏离最近批准的基线" description="有人在批准后的策略包之外修改了策略。请重新导出、评审并应用新提案。" /> : null}
        <Card title="当前快照与基线">
          <Descriptions column={1} size="small">
            <Descriptions.Item label="租户">{snapshot?.bundle.tenant_id || "加载中"}</Descriptions.Item>
            <Descriptions.Item label="当前摘要"><Text copyable>{snapshot?.digest || "—"}</Text></Descriptions.Item>
            <Descriptions.Item label="批准基线"><Text copyable>{status?.approved_digest || "尚未建立"}</Text></Descriptions.Item>
            <Descriptions.Item label="状态"><Tag color={status?.state === "approved" ? "green" : status?.state === "drifted" ? "orange" : "default"}>{status?.state || "加载中"}</Tag></Descriptions.Item>
          </Descriptions>
        </Card>
        <Card title="编排与变更计划" extra={<Space><Button loading={busy} onClick={() => void run(async () => { const base = await refresh(); setJsonText(JSON.stringify(base.bundle, null, 2)); setPlan(null); })}>重置为当前快照</Button><Button type="primary" loading={busy} disabled={!snapshot} onClick={() => void run(async () => {
          try {
            const desired = parseBundle(jsonText);
            const parsedCases: unknown = JSON.parse(replayText);
            if (!Array.isArray(parsedCases)) throw new Error("回放案例必须是 JSON 数组");
            const next = await planPolicyBundle(snapshot!.digest, desired, parsedCases);
            setPlan(next);
          } catch (error) { if (error instanceof SyntaxError || error instanceof Error && (error.message.includes("策略包") || error.message.includes("回放案例"))) void message.error(error.message); else throw error; }
        })}>生成计划</Button></Space>}>
          <Input.TextArea aria-label="策略包 JSON" rows={14} value={jsonText} onChange={(event) => { setJsonText(event.target.value); setPlan(null); }} spellCheck={false} style={{ fontFamily: "monospace" }} />
          <Text>可选离线回放：填写 JSON 数组，每项含 agent_id、datasource_id、object_name（精确表名）。最多 32 项。</Text>
          <Input.TextArea aria-label="离线回放案例 JSON" rows={3} value={replayText} onChange={(event) => { setReplayText(event.target.value); setPlan(null); }} spellCheck={false} style={{ fontFamily: "monospace", marginTop: 8 }} />
          {plan ? <Space direction="vertical" style={{ width: "100%", marginTop: 16 }}>
            <Text>目标摘要：<Text copyable>{plan.desired_digest}</Text></Text>
            <Table size="small" rowKey="id" pagination={false} dataSource={plan.changes} columns={[
              { title: "策略 ID", dataIndex: "id" }, { title: "操作", dataIndex: "operation" },
              { title: "放宽权限", dataIndex: "broadens_grant", render: (value: boolean) => value ? <Tag color="orange">需重点复核</Tag> : "否" },
            ]} />
            {plan.replay?.length ? <Table size="small" rowKey={(row) => `${row.agent_id}/${row.datasource_id}/${row.object_name}`} pagination={false} dataSource={plan.replay} columns={[
              { title: "Agent", dataIndex: "agent_id" }, { title: "数据源", dataIndex: "datasource_id" },
              { title: "精确表名", dataIndex: "object_name" }, { title: "变更前", dataIndex: "before" }, { title: "变更后", dataIndex: "after" },
            ]} /> : null}
            <Button type="primary" loading={busy} onClick={() => void run(async () => {
              const created = await proposePolicyBundle(plan.base_digest, parseBundle(jsonText));
              setProposal(created); setProposalPlan(plan); setProposalStale(false); setProposalID(created.id); void message.success("提案已创建，需其他管理员审批");
            })}>提交评审提案</Button>
          </Space> : null}
        </Card>
        <Card title="异人评审与应用">
          <Space.Compact style={{ width: "100%" }}><Input aria-label="提案 ID" placeholder="输入提案 ID" value={proposalID} onChange={(event) => setProposalID(event.target.value)} /><Button loading={busy} onClick={() => void inspect()}>查询提案</Button></Space.Compact>
          {proposal ? <Space direction="vertical" style={{ width: "100%", marginTop: 16 }}>
            {proposalStale ? <Alert type="warning" showIcon message="提案来源已过期" description="当前策略摘要与提案来源不符，不能应用；请重新导出并提交。" /> : null}
            <Descriptions column={1} size="small">
              <Descriptions.Item label="提案 ID"><Text copyable>{proposal.id}</Text></Descriptions.Item>
              <Descriptions.Item label="状态">{proposal.status}</Descriptions.Item>
              <Descriptions.Item label="发起人">{proposal.author_user_id}</Descriptions.Item>
              <Descriptions.Item label="审批人">{proposal.reviewer_user_id || "待审批"}</Descriptions.Item>
              <Descriptions.Item label="源摘要"><Text copyable>{proposal.base_digest}</Text></Descriptions.Item>
              <Descriptions.Item label="目标摘要"><Text copyable>{proposal.desired_digest}</Text></Descriptions.Item>
            </Descriptions>
            {proposalPlan ? <Table size="small" rowKey="id" pagination={false} dataSource={proposalPlan.changes} columns={[
              { title: "策略 ID", dataIndex: "id" }, { title: "操作", dataIndex: "operation" },
              { title: "目标", render: (_, change) => {
                const row = proposal.desired.policies.find((item) => item.id === change.id);
                return row ? `${String(row.action)} ${String(row.object_type)} ${String(row.object_name)}` : "删除";
              } },
              { title: "放宽权限", dataIndex: "broadens_grant", render: (value: boolean) => value ? <Tag color="orange">需重点复核</Tag> : "否" },
            ]} /> : null}
            <Space>
              <Button disabled={proposal.status !== "pending" || !proposalPlan || proposalStale} loading={busy} onClick={() => void review(true)}>批准</Button>
              <Button danger disabled={proposal.status !== "pending"} loading={busy} onClick={() => void review(false)}>驳回</Button>
              <Button type="primary" disabled={proposal.status !== "approved" || !proposalPlan || proposalStale} loading={busy} onClick={apply}>应用</Button>
            </Space>
          </Space> : null}
        </Card>
      </Space>
    </PageContainer>
  );
}
