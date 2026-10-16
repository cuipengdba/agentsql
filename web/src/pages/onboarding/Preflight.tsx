import { Alert, AutoComplete, Button, Card, Space, Tag, Typography } from "antd";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { getAgent, listAgents } from "@/api/agents";
import { getDatasource, listDatasources, listDatasourceTypes, nativePingDatasource, pingDatasource } from "@/api/datasources";
import { listPolicies } from "@/api/policies";
import type { AgentView, DatasourceTypeView, DatasourceView, PingView } from "@/api/types";

import { httpStatus } from "../config/utils";

type Status = "pass" | "warn" | "fail";
interface Check { status: Status; title: string; detail: string; path: string; action: string }

function failure(error: unknown, fallback: string): string {
  const status = httpStatus(error);
  if (status === 401) return "登录已失效，请重新登录。";
  if (status === 403) return "当前账号无权执行检查，请联系租户管理员。";
  if (status === 404) return "所选配置不存在或不属于当前租户，请核对 ID。";
  if (status === 408 || status === 504) return "检查超时，请核对网络和数据库状态后重试。";
  return status ? fallback : "服务不可达或请求超时，请检查网关状态后重试。";
}

function checkAgent(agent: AgentView): Check {
  const expiry = agent.expires_at ? Date.parse(agent.expires_at) : null;
  if (agent.status !== "active" || expiry !== null && (!Number.isFinite(expiry) || expiry <= Date.now())) {
    return { status: "fail", title: "Agent 状态", detail: agent.status !== "active" ? "Agent 尚未启用，请先启用。" : "Agent 已过期或有效期无效，请先更新。", path: "/agents", action: "管理 Agent" };
  }
  return { status: "pass", title: "Agent 状态", detail: `Agent 已启用，级别为 ${agent.level}。真实请求仍会逐次验证 API Key。`, path: "/agents", action: "查看 Agent" };
}

function checkConnection(datasource: DatasourceView, result: PingView): Check {
  if (!result.ok) {
    return { status: "fail", title: "数据库连接", detail: `连接未通过${result.error_code ? `（${result.error_code}）` : ""}。检查地址、网络、凭据和账号权限。`, path: "/datasources", action: "检查数据源" };
  }
  return { status: "pass", title: "数据库连接", detail: `${datasource.name} 连接通过，耗时 ${Math.max(0, result.latency_ms)} ms。此检查不执行 Agent SQL。`, path: "/datasources", action: "查看数据源" };
}

export function OnboardingPreflight() {
  const [agents, setAgents] = useState<AgentView[]>([]);
  const [datasources, setDatasources] = useState<DatasourceView[]>([]);
  const [types, setTypes] = useState<DatasourceTypeView[]>([]);
  const [catalogError, setCatalogError] = useState(false);
  const [agentID, setAgentID] = useState("");
  const [datasourceID, setDatasourceID] = useState("");
  const [checks, setChecks] = useState<Check[] | null>(null);
  const [checking, setChecking] = useState(false);
  const sequence = useRef(0);

  useEffect(() => {
    let active = true;
    void Promise.all([
      listAgents({ page: 1, page_size: 100 }),
      listDatasources({ page: 1, page_size: 100 }),
      listDatasourceTypes(),
    ]).then(([agentPage, datasourcePage, datasourceTypes]) => {
      if (!active) return;
      setAgents(agentPage.list || []);
      setDatasources(datasourcePage.list || []);
      setTypes(datasourceTypes);
      setCatalogError(false);
    }).catch(() => { if (active) setCatalogError(true); });
    return () => { active = false; sequence.current += 1; };
  }, []);

  const run = async () => {
    const selectedAgentID = agentID.trim();
    const selectedDatasourceID = datasourceID.trim();
    if (!selectedAgentID || !selectedDatasourceID) return;
    const current = ++sequence.current;
    setChecks(null);
    setChecking(true);
    const [agentResult, datasourceResult] = await Promise.allSettled([
      getAgent(selectedAgentID), getDatasource(selectedDatasourceID),
    ]);
    if (sequence.current !== current) return;
    const next: Check[] = [];
    if (agentResult.status === "fulfilled") next.push(checkAgent(agentResult.value));
    else next.push({ status: "fail", title: "Agent 状态", detail: failure(agentResult.reason, "无法读取 Agent，请检查权限后重试。"), path: "/agents", action: "查看 Agent" });
    if (datasourceResult.status === "rejected") {
      next.push({ status: "fail", title: "数据库连接", detail: failure(datasourceResult.reason, "无法读取数据源，请检查权限后重试。"), path: "/datasources", action: "查看数据源" });
    } else {
      const datasource = datasourceResult.value;
      const type = types.find((item) => item.key === datasource.db_type);
      const [policyResult, pingResult] = await Promise.allSettled([
        listPolicies({ agent_id: selectedAgentID, datasource_id: selectedDatasourceID, page: 1, page_size: 1 }),
        type ? type.kind === "nosql" ? nativePingDatasource(selectedDatasourceID) : pingDatasource(selectedDatasourceID)
          : Promise.reject(new Error("unknown datasource type")),
      ]);
      if (sequence.current !== current) return;
      if (policyResult.status === "fulfilled") {
        const total = policyResult.value.total;
        next.push(total === 0
          ? { status: "fail", title: "权限配置", detail: "该 Agent 与数据源之间没有策略。先授予所需对象的最小权限。", path: "/policies", action: "配置权限" }
          : { status: "warn", title: "权限配置", detail: `找到 ${total} 条策略。策略存在不表示任意 SQL 已获授权；请用真实 MCP 请求验证允许和拒绝路径。`, path: "/policies", action: "核对策略" });
      } else {
        next.push({ status: "fail", title: "权限配置", detail: failure(policyResult.reason, "无法读取权限策略，请检查权限后重试。"), path: "/policies", action: "查看权限" });
      }
      if (pingResult.status === "fulfilled") next.push(checkConnection(datasource, pingResult.value));
      else next.push({ status: "fail", title: "数据库连接", detail: type ? failure(pingResult.reason, "连接检查失败，请核对数据源配置。") : "无法识别数据库类型，请刷新页面后重试。", path: "/datasources", action: "检查数据源" });
    }
    if (sequence.current === current) { setChecks(next); setChecking(false); }
  };

  const labels: Record<Status, string> = { pass: "通过", warn: "需验证", fail: "未通过" };
  const colors: Record<Status, "success" | "warning" | "error"> = { pass: "success", warn: "warning", fail: "error" };
  return <Card title="连接与权限预检" style={{ marginTop: 20 }}>
    <Typography.Paragraph type="secondary">选择当前租户中的 Agent 和数据源，或输入完整 ID。预检读取配置并使用保存的数据库账号测试连接；不会执行 SQL。</Typography.Paragraph>
    {catalogError && <Alert type="error" showIcon message="配置目录加载失败" description="可手动输入 Agent 与数据源 ID；若数据库类型未加载，请刷新页面后重试。" style={{ marginBottom: 12 }} />}
    <Space wrap>
      <AutoComplete aria-label="Agent ID" placeholder="Agent ID" style={{ width: 240 }} value={agentID} options={agents.map((agent) => ({ value: agent.id, label: `${agent.name} (${agent.id})` }))} filterOption={(input, option) => String(option?.label || "").toLowerCase().includes(input.toLowerCase())} onChange={(value) => { sequence.current += 1; setAgentID(value); setChecks(null); setChecking(false); }} />
      <AutoComplete aria-label="数据源 ID" placeholder="数据源 ID" style={{ width: 240 }} value={datasourceID} options={datasources.map((datasource) => ({ value: datasource.id, label: `${datasource.name} (${datasource.id})` }))} filterOption={(input, option) => String(option?.label || "").toLowerCase().includes(input.toLowerCase())} onChange={(value) => { sequence.current += 1; setDatasourceID(value); setChecks(null); setChecking(false); }} />
      <Button type="primary" loading={checking} disabled={!agentID.trim() || !datasourceID.trim()} onClick={() => void run()}>运行预检</Button>
    </Space>
    {checks && <Space direction="vertical" size={10} style={{ width: "100%", marginTop: 16 }}>
      {checks.map((check) => <Alert key={check.title} type={colors[check.status]} showIcon message={<Space><span>{check.title}</span><Tag color={colors[check.status]}>{labels[check.status]}</Tag></Space>} description={<Space wrap><span>{check.detail}</span><Link to={check.path}>{check.action}</Link></Space>} />)}
      <Alert type="info" showIcon message="最后还需真实 MCP 验收" description={<span>使用该 Agent 的 API Key 从客户端发起允许和拒绝请求，再到 <Link to="/audit">审计页</Link>核对决策。预检不能代替逐对象授权与执行链路验收。</span>} />
    </Space>}
  </Card>;
}
