import { Alert, Button, Card, Input, Space, Typography } from "antd";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { preflightPolicy } from "@/api/policies";
import type { PolicyPreflightView } from "@/api/types";

import { httpStatus } from "../config/utils";

interface PolicyExplainProps {
  agentID: string;
  datasourceID: string;
}

function errorDescription(error: unknown): string {
  const status = httpStatus(error);
  if (status === 401) return "登录已失效，请重新登录后再试。";
  if (status === 403) return "当前账号无权查看该 Agent 或数据源的策略。";
  if (status === 404) return "Agent 或数据源不存在，或不属于当前租户。";
  if (status === 422) return "SQL 未通过输入校验；请使用一条明确、受支持的语句。";
  return "离线解释未完成，请检查网关和控制面状态后重试。";
}

export function PolicyExplain({ agentID, datasourceID }: PolicyExplainProps) {
  const [sql, setSQL] = useState("");
  const [result, setResult] = useState<PolicyPreflightView | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const sequence = useRef(0);

  useEffect(() => {
    sequence.current += 1;
    setResult(null);
    setError("");
    setLoading(false);
  }, [agentID, datasourceID, sql]);
  useEffect(() => () => { sequence.current += 1; }, []);

  const run = async () => {
    if (!agentID.trim() || !datasourceID.trim() || !sql.trim()) return;
    const current = ++sequence.current;
    setResult(null);
    setError("");
    setLoading(true);
    try {
      const response = await preflightPolicy({ agent_id: agentID.trim(), datasource_id: datasourceID.trim(), sql: sql.trim() });
      if (sequence.current === current) setResult(response);
    } catch (cause) {
      if (sequence.current === current) setError(errorDescription(cause));
    } finally {
      if (sequence.current === current) setLoading(false);
    }
  };

  return <Card title="解释目标 SQL 的策略" style={{ marginTop: 20 }}>
    <Typography.Paragraph type="secondary">沿用上方选定的 Agent 和数据源，读取已保存的策略并解析一条 SQL。此步骤不连接业务库、不执行 SQL，也不会给出“允许执行”的结论。</Typography.Paragraph>
    {!agentID.trim() || !datasourceID.trim() ? <Alert type="info" showIcon message="请先在连接与权限预检中选择 Agent 和数据源" style={{ marginBottom: 12 }} /> : null}
    <Input.TextArea aria-label="待解释 SQL" rows={3} maxLength={16_384} placeholder="SELECT id FROM public.customers WHERE id = 1 LIMIT 10" value={sql} onChange={(event) => setSQL(event.target.value)} />
    <Button style={{ marginTop: 10 }} loading={loading} disabled={!agentID.trim() || !datasourceID.trim() || !sql.trim()} onClick={() => void run()}>运行离线解释</Button>
    {error && <Alert style={{ marginTop: 14 }} type="error" showIcon message="离线解释未完成" description={error} />}
    {result && <Alert style={{ marginTop: 14 }} showIcon type={result.decision === "deny" ? "error" : "warning"}
      message={result.decision === "deny" ? "明确拒绝" : "尚不能确认允许"}
      description={<Space direction="vertical" size={8}>
        <span>{result.reason}{result.static_rule ? `（静态规则 ${result.static_rule}）` : ""}</span>
        {result.objects.map((object, index) => <span key={`${object.schema}.${object.table}.${index}`}>
          {object.schema ? `${object.schema}.` : ""}{object.table}：{object.state === "deny" ? "表策略拒绝或缺少允许" : "表策略候选，仍需真实验证"}；允许策略 {object.allow_policy_ids.join("、") || "无"}；拒绝策略 {object.deny_policy_ids.join("、") || "无"}
        </span>)}
        <span>下一步：在真实 MCP 客户端分别运行允许与拒绝请求，到 <Link to="/audit">审计记录</Link>核对结果；需调整策略时打开 <Link to="/policies">权限策略</Link>。</span>
      </Space>} />}
  </Card>;
}
