import { CopyOutlined, PlayCircleOutlined, ThunderboltOutlined, UndoOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Empty, Input, Segmented, Select, Spin, Tag, Tooltip, message } from "antd";
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  assessPlayground,
  isPlaygroundRequestCanceled,
  playgroundRunErrorMessage,
  runPlayground,
} from "@/api/playground";
import type {
  PlaygroundAssessRequest,
  PlaygroundAssessView,
  PlaygroundDemoAgentProfile,
  PlaygroundDemoDatasourceID,
  PlaygroundRunRequest,
  PlaygroundRunResponse,
} from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { StageFlow } from "@/components/stageflow/StageFlow";
import { adaptAssessment } from "@/components/stageflow/adaptAssessment";
import { decisionMeta, getDecisionMeta, statementLabel } from "@/constants/labels";
import { selectIsDemo, useDemoStore } from "@/store/demoStore";
import { palette } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";

import { DemoScenarioCards, type DemoRunScenario } from "./playground/DemoScenarioCards";
import { LiveResultTable } from "./playground/LiveResultTable";
import { playgroundScenarios, type PlaygroundScenario } from "./playground/scenarios";

const { TextArea } = Input;
type Dialect = PlaygroundAssessRequest["db_type"];
type AgentLevel = NonNullable<PlaygroundAssessRequest["agent_level"]>;
type PlaygroundMode = "static" | "live";

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
  const isDemo = useDemoStore(selectIsDemo);
  const [playgroundMode, setPlaygroundMode] = useState<PlaygroundMode>("static");
  const [dialect, setDialect] = useState<Dialect>("postgres");
  const [agentLevel, setAgentLevel] = useState<AgentLevel>("readonly");
  const [sql, setSQL] = useState("");
  const [result, setResult] = useState<PlaygroundAssessView | null>(null);
  const [loading, setLoading] = useState(false);
  const [replayKey, setReplayKey] = useState(0);
  const mountedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const requestSequenceRef = useRef(0);
  const [liveDatasourceID, setLiveDatasourceID] = useState<PlaygroundDemoDatasourceID>("ds-demo-pg");
  const [liveAgentProfile, setLiveAgentProfile] = useState<PlaygroundDemoAgentProfile>("ro");
  const [liveSQL, setLiveSQL] = useState("");
  const [liveResult, setLiveResult] = useState<PlaygroundRunResponse | null>(null);
  const [liveLoading, setLiveLoading] = useState(false);
  const [liveError, setLiveError] = useState<string | null>(null);
  const [activeDemoScenario, setActiveDemoScenario] = useState<string | null>(null);
  const [latestAuditID, setLatestAuditID] = useState<number | null>(null);
  const liveControllerRef = useRef<AbortController | null>(null);
  const liveRequestSequenceRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      controllerRef.current = null;
      requestSequenceRef.current += 1;
      liveControllerRef.current?.abort();
      liveControllerRef.current = null;
      liveRequestSequenceRef.current += 1;
    };
  }, []);

  useEffect(() => {
    if (isDemo) return;
    liveControllerRef.current?.abort();
    liveControllerRef.current = null;
    liveRequestSequenceRef.current += 1;
    setLiveLoading(false);
    setLiveResult(null);
    setLiveError(null);
    setPlaygroundMode("static");
  }, [isDemo]);

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

  const executeLiveRun = useCallback(async (request: PlaygroundRunRequest) => {
    // The store is checked again at call time so a non-demo projection can
    // never trigger the demo-only endpoint, even through a stale callback.
    if (!useDemoStore.getState().enabled) return;

    liveControllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = liveRequestSequenceRef.current + 1;
    liveRequestSequenceRef.current = sequence;
    liveControllerRef.current = controller;
    setLiveLoading(true);
    setLiveError(null);
    setLiveResult(null);
    try {
      const response = await runPlayground(request, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || liveRequestSequenceRef.current !== sequence) return;
      setLiveResult(response);
      if (Number.isSafeInteger(response.audit_id) && response.audit_id > 0) setLatestAuditID(response.audit_id);
    } catch (error: unknown) {
      if (!isPlaygroundRequestCanceled(error) && mountedRef.current && liveRequestSequenceRef.current === sequence) {
        setLiveError(playgroundRunErrorMessage(error));
      }
    } finally {
      if (liveControllerRef.current === controller) {
        liveControllerRef.current = null;
        if (mountedRef.current) setLiveLoading(false);
      }
    }
  }, []);

  const submitLiveRun = useCallback(() => {
    const statement = liveSQL.trim();
    if (!statement) {
      void message.warning("请输入要真实试运行的 SQL");
      return;
    }
    setActiveDemoScenario(null);
    void executeLiveRun({ sql: statement, datasource_id: liveDatasourceID, agent_profile: liveAgentProfile });
  }, [executeLiveRun, liveAgentProfile, liveDatasourceID, liveSQL]);

  const runDemoScenario = useCallback((scenario: DemoRunScenario) => {
    setLiveDatasourceID(scenario.datasourceID);
    setLiveAgentProfile(scenario.agentProfile);
    setLiveSQL(scenario.sql);
    setActiveDemoScenario(scenario.key);
    void executeLiveRun({
      sql: scenario.sql,
      datasource_id: scenario.datasourceID,
      agent_profile: scenario.agentProfile,
    });
  }, [executeLiveRun]);

  const resetLiveRun = useCallback(() => {
    liveControllerRef.current?.abort();
    liveControllerRef.current = null;
    liveRequestSequenceRef.current += 1;
    setLiveDatasourceID("ds-demo-pg");
    setLiveAgentProfile("ro");
    setLiveSQL("");
    setLiveResult(null);
    setLiveError(null);
    setLiveLoading(false);
    setActiveDemoScenario(null);
  }, []);

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
    <PageContainer
      title="拦截演示台"
      subtitle="输入 SQL，看一次 AI 请求如何被六段安全网关逐段判定"
      extra={isDemo ? (
        <Segmented<PlaygroundMode>
          value={playgroundMode}
          options={[
            { label: "静态评估", value: "static" },
            { label: "真实试运行（Live）", value: "live" },
          ]}
          onChange={setPlaygroundMode}
        />
      ) : undefined}
    >
      {!isDemo || playgroundMode === "static" ? (
        <>
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
        </>
      ) : null}

      {isDemo && playgroundMode === "live" ? (
        <section className="playground-live" aria-label="真实试运行 Live Demo">
          <div className="live-demo-heading">
            <div>
              <Tag color="processing">Live Demo</Tag>
              <h2>真实试运行</h2>
              <p>通过受限演示身份连接演示数据库，执行、脱敏与审计均走真实网关链路。</p>
            </div>
            <Tag color="warning">仅允许固定演示数据源</Tag>
          </div>

          <Card className="playground-live-control" bordered>
            <div className="playground-control-row">
              <div className="playground-control-field live-control-field">
                <span>演示数据源</span>
                <Select<PlaygroundDemoDatasourceID>
                  value={liveDatasourceID}
                  options={[
                    { label: "PostgreSQL（ds-demo-pg）", value: "ds-demo-pg" },
                    { label: "MySQL（ds-demo-mysql）", value: "ds-demo-mysql" },
                  ]}
                  onChange={(value) => {
                    setLiveDatasourceID(value);
                    setActiveDemoScenario(null);
                  }}
                />
              </div>
              <div className="playground-control-field live-control-field">
                <span>固定演示身份</span>
                <Select<PlaygroundDemoAgentProfile>
                  value={liveAgentProfile}
                  options={[
                    { label: "只读演示（ro）", value: "ro" },
                    { label: "DML 拦截演示（dml）", value: "dml" },
                  ]}
                  onChange={(value) => {
                    setLiveAgentProfile(value);
                    setActiveDemoScenario(null);
                  }}
                />
              </div>
            </div>
            <label className="playground-sql-label" htmlFor="playground-live-sql">SQL</label>
            <TextArea
              id="playground-live-sql"
              className="playground-sql-input mono-text"
              value={liveSQL}
              onChange={(event) => {
                setLiveSQL(event.target.value);
                setActiveDemoScenario(null);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
                  event.preventDefault();
                  submitLiveRun();
                }
              }}
              autoSize={{ minRows: 3, maxRows: 10 }}
              spellCheck={false}
              placeholder="输入要在受限演示数据库中试运行的 SQL"
            />
            <div className="playground-actions">
              <Button icon={<UndoOutlined />} onClick={resetLiveRun}>重置</Button>
              <Tooltip title="Ctrl/⌘ + Enter">
                <Button type="primary" icon={<PlayCircleOutlined />} loading={liveLoading} onClick={submitLiveRun}>真实试运行</Button>
              </Tooltip>
            </div>
          </Card>

          <DemoScenarioCards
            activeKey={activeDemoScenario}
            loading={liveLoading}
            latestAuditID={latestAuditID}
            onRun={runDemoScenario}
          />

          <section className="playground-live-result" aria-live="polite">
            <div className="playground-section-heading"><strong>试运行结果</strong><span>真实执行 · 脱敏 · 审计</span></div>
            {liveLoading ? (
              <div className="playground-loading"><Spin size="large" tip="正在通过真实安全链路试运行" /></div>
            ) : liveError ? (
              <Alert
                type="error"
                showIcon
                message="真实试运行失败"
                description={liveError}
                action={<Button size="small" onClick={submitLiveRun}>重试</Button>}
              />
            ) : liveResult ? (
              <LiveResultTable result={liveResult} />
            ) : (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="输入 SQL，或点击上方剧本卡开始真实试运行" />
            )}
          </section>
        </section>
      ) : null}
    </PageContainer>
  );
}
