import { CopyOutlined, ThunderboltOutlined, UndoOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Empty, Input, Segmented, Spin, Tag, Tooltip, message } from "antd";
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { assessPlayground } from "@/api/playground";
import type { PlaygroundAssessRequest, PlaygroundAssessView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { StageFlow } from "@/components/stageflow/StageFlow";
import { adaptAssessment } from "@/components/stageflow/adaptAssessment";
import { decisionMeta, getDecisionMeta, statementLabel } from "@/constants/labels";
import { palette } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";

import { playgroundScenarios, type PlaygroundScenario } from "./playground/scenarios";

const { TextArea } = Input;
type Dialect = PlaygroundAssessRequest["db_type"];
type AgentLevel = NonNullable<PlaygroundAssessRequest["agent_level"]>;

interface ScenarioStyle extends CSSProperties {
  "--playground-scenario-color": string;
}

function knownDecision(value: string): boolean {
  return Object.prototype.hasOwnProperty.call(decisionMeta, value.trim().toLowerCase());
}

function scenarioColor(scenario: PlaygroundScenario, mode: "dark" | "light"): string {
  if (scenario.expect === "error") return palette[mode].textSecondary;
  if (scenario.expect === "allow") return decisionMeta.allow.color;
  if (scenario.expect === "approve") return decisionMeta.approve.color;
  return decisionMeta.deny.color;
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  let copied = false;
  try {
    textarea.select();
    copied = document.execCommand("copy");
  } finally {
    document.body.removeChild(textarea);
  }
  if (!copied) throw new Error("copy command failed");
}

export function Playground() {
  const mode = useThemeStore((state) => state.mode);
  const [dialect, setDialect] = useState<Dialect>("postgres");
  const [agentLevel, setAgentLevel] = useState<AgentLevel>("readonly");
  const [sql, setSQL] = useState("");
  const [result, setResult] = useState<PlaygroundAssessView | null>(null);
  const [loading, setLoading] = useState(false);
  const [replayKey, setReplayKey] = useState(0);
  const mountedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const requestSequenceRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      controllerRef.current = null;
      requestSequenceRef.current += 1;
    };
  }, []);

  const runAssessment = useCallback(async (request: PlaygroundAssessRequest) => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = requestSequenceRef.current + 1;
    requestSequenceRef.current = sequence;
    controllerRef.current = controller;
    setLoading(true);
    setResult(null);
    try {
      const response = await assessPlayground(request, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || requestSequenceRef.current !== sequence) return;
      setResult(response);
      setReplayKey((value) => value + 1);
    } catch {
      // Shared API handling reports non-cancellation failures; stale and
      // cancelled responses must remain silent and cannot update this view.
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null;
        if (mountedRef.current) setLoading(false);
      }
    }
  }, []);

  const submit = useCallback(() => {
    const statement = sql.trim();
    if (!statement) {
      void message.warning("请输入要评估的 SQL");
      return;
    }
    void runAssessment({ sql: statement, db_type: dialect, agent_level: agentLevel });
  }, [agentLevel, dialect, runAssessment, sql]);

  const selectScenario = (scenario: PlaygroundScenario) => {
    setDialect(scenario.dbType);
    setAgentLevel(scenario.agentLevel);
    setSQL(scenario.sql);
    void runAssessment({ sql: scenario.sql, db_type: scenario.dbType, agent_level: scenario.agentLevel });
  };

  const reset = () => {
    controllerRef.current?.abort();
    controllerRef.current = null;
    requestSequenceRef.current += 1;
    setDialect("postgres");
    setAgentLevel("readonly");
    setSQL("");
    setResult(null);
    setLoading(false);
  };

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      submit();
    }
  };

  const flowData = useMemo(() => result ? adaptAssessment(result) : null, [result]);
  const rawJSON = useMemo(() => result ? JSON.stringify(result, null, 2) : "", [result]);
  const resultDecision = result ? getDecisionMeta(result.Decision) : null;

  const copyResultSQL = async () => {
    if (!result?.SQL) return;
    try {
      await copyText(result.SQL);
      void message.success("SQL 已复制");
    } catch {
      void message.error("复制失败，请手动选择文本");
    }
  };

  return (
    <PageContainer title="拦截演示台" subtitle="输入 SQL，看一次 AI 请求如何被六段安全网关逐段判定">
      <Alert
        className="playground-notice"
        type="info"
        showIcon
        message="零风险演示：仅做 SQL 解析与静态规则安检，不连接真实数据库、不执行 SQL、不写审计；依赖执行计划、索引、事务态的动态规则与表/列级授权，在真实网关连库并配置策略（T22）后生效。"
      />

      <Card className="playground-control" bordered>
        <div className="playground-control-row">
          <div className="playground-control-field">
            <span>数据库方言</span>
            <Segmented<Dialect>
              value={dialect}
              options={[{ label: "PostgreSQL", value: "postgres" }, { label: "MySQL", value: "mysql" }]}
              onChange={setDialect}
            />
          </div>
          <div className="playground-control-field">
            <span>Agent 级别</span>
            <Tooltip title="readonly 级别的非 SELECT 请求会由 R003 拦截">
              <Segmented<AgentLevel>
                value={agentLevel}
                options={[{ label: "只读", value: "readonly" }, { label: "读写", value: "dml" }, { label: "DDL", value: "ddl" }]}
                onChange={setAgentLevel}
              />
            </Tooltip>
          </div>
        </div>
        <label className="playground-sql-label" htmlFor="playground-sql">SQL</label>
        <TextArea
          id="playground-sql"
          className="playground-sql-input mono-text"
          value={sql}
          onChange={(event) => setSQL(event.target.value)}
          onKeyDown={handleKeyDown}
          autoSize={{ minRows: 3, maxRows: 10 }}
          spellCheck={false}
          placeholder="输入一条 PostgreSQL 或 MySQL 语句"
        />
        <div className="playground-actions">
          <Button icon={<UndoOutlined />} onClick={reset}>重置</Button>
          <Tooltip title="Ctrl/⌘ + Enter">
            <Button type="primary" icon={<ThunderboltOutlined />} loading={loading} onClick={submit}>模拟 AI 请求</Button>
          </Tooltip>
        </div>
      </Card>

      <section className="playground-scenarios" aria-label="安全演示剧本">
        <div className="playground-section-heading"><strong>安全剧本</strong><span>点击后立即评估</span></div>
        <div className="playground-scenario-list">
          {playgroundScenarios.map((scenario) => {
            const style: ScenarioStyle = { "--playground-scenario-color": scenarioColor(scenario, mode) };
            return (
              <Button
                className={`playground-scenario playground-scenario-${scenario.expect}`}
                style={style}
                key={scenario.key}
                onClick={() => selectScenario(scenario)}
              >
                {scenario.label}
              </Button>
            );
          })}
        </div>
        <p className="playground-try-more">可自行尝试：DROP TABLE x（R101）、KILL 12（R203）、COPY ... PROGRAM（R104）、带注释 SQL（R006）。纯静态层不为展示效果伪造 warn。</p>
      </section>

      <section className="playground-result" aria-live="polite">
        <div className="playground-section-heading"><strong>评估结果</strong><span>静态规则门</span></div>
        {loading ? (
          <div className="playground-loading"><Spin size="large" tip="正在解析并执行静态规则安检" /></div>
        ) : !result || !flowData || !resultDecision ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="输入 SQL 或选择一个安全剧本开始演示" />
        ) : (
          <>
            {result.ParseError || result.Decision === "error" ? <Alert className="playground-parse-error" type="error" showIcon message="SQL 解析失败" description={result.ParseError || result.Reason || "解析器无法生成可信 AST"} /> : null}
            <div className="playground-result-meta">
              {knownDecision(result.Decision) ? <Tag color={resultDecision.tagColor}>{resultDecision.label}</Tag> : <Tag className="playground-decision-error">{result.Decision === "error" ? "错误" : result.Decision || "未知"}</Tag>}
              <Tag>{statementLabel(result.StmtType)}</Tag>
              <span>{result.DBType || dialect}</span>
              <code>{result.SQL || sql}</code>
              <Tooltip title="复制 SQL"><Button type="text" size="small" icon={<CopyOutlined />} onClick={() => void copyResultSQL()} aria-label="复制 SQL" /></Tooltip>
            </div>
            <StageFlow data={flowData} autoPlay replayKey={replayKey} />
            <details className="playground-raw-json">
              <summary>查看评估原始 JSON</summary>
              <pre>{rawJSON}</pre>
            </details>
          </>
        )}
      </section>
    </PageContainer>
  );
}
