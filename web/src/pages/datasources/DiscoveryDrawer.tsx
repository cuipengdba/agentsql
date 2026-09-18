import {
  DeleteOutlined,
  PlusOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
} from "@ant-design/icons";
import {
  Alert,
  Button,
  Drawer,
  Input,
  InputNumber,
  Space,
  Statistic,
  Switch,
  Typography,
  message,
} from "antd";
import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";

import {
  applyDiscoveryDrafts,
  discoverSensitiveColumns,
  discoveryApplyResponseFromError,
  type DiscoveryApplyResponse,
  type DiscoveryApplyStatus,
  type DiscoveryResponse,
  type DiscoveryTableRef,
} from "@/api/discovery";
import type { DatasourceView } from "@/api/types";
import { discoveryApplyStatusMeta } from "@/constants/labels";
import { sensitiveTypes } from "@/constants/sensitiveTypes";
import { httpStatus, isCanceled } from "@/pages/config/utils";

import { DiscoveryResults, discoveryFindingKey } from "./DiscoveryResults";

const MAX_TABLES = 20;

interface EditableTableRef extends DiscoveryTableRef {
  key: number;
}

interface DiscoveryDrawerProps {
  open: boolean;
  datasource: DatasourceView | null;
  onClose: () => void;
}

function requestTimedOut(error: unknown): boolean {
  return typeof error === "object" && error !== null && (error as { code?: unknown }).code === "ECONNABORTED";
}

function friendlyDiscoveryError(error: unknown, phase: "discover" | "apply"): string {
  const status = httpStatus(error);
  if (status === 403) return "数据源账号无权读取部分对象，请授权后重试";
  if (status === 429) return "敏感发现请求已限流，请稍后重试";
  if (status === 413) return "请求范围超限，请减少表数量后重试";
  if (status === 422) return phase === "apply"
    ? "所选列暂不支持一键生成脱敏规则，请按提示选择可应用的发现项"
    : "发现范围或参数超限，请检查表范围与采样行数";
  if (status === 504 || status === 408 || requestTimedOut(error)) return "敏感发现超时，建议缩小表范围或关闭采样后重试";
  if (status === 503) return "受控读服务未就绪，请稍后重试";
  if (status === 409) return "同名列已有不同脱敏规则，请到脱敏规则页核对后再处理";
  return phase === "apply" ? "生成脱敏草稿失败，请稍后重试" : "敏感发现失败，请稍后重试";
}

function statusMapFromOutcome(outcome: DiscoveryApplyResponse): Map<string, DiscoveryApplyStatus> {
  const statuses = new Map<string, DiscoveryApplyStatus>();
  const add = (status: DiscoveryApplyStatus, items: DiscoveryApplyResponse[keyof Pick<DiscoveryApplyResponse,
    "created" | "existing" | "covered_by_global" | "conflicts" | "ambiguous">]) => {
    items.forEach((item) => statuses.set(item.column.trim().toLowerCase(), status));
  };
  add("created", outcome.created);
  add("existing", outcome.existing);
  add("covered_by_global", outcome.covered_by_global);
  add("conflict", outcome.conflicts);
  add("ambiguous", outcome.ambiguous);
  return statuses;
}

function applySummary(outcome: DiscoveryApplyResponse): string {
  const entries: Array<[DiscoveryApplyStatus, number]> = [
    ["created", outcome.counts.created],
    ["existing", outcome.counts.existing],
    ["covered_by_global", outcome.counts.covered_by_global],
    ["conflict", outcome.counts.conflicts],
    ["ambiguous", outcome.counts.ambiguous],
  ];
  return entries
    .filter(([, count]) => count > 0)
    .map(([status, count]) => `${discoveryApplyStatusMeta[status].label} ${count} 项`)
    .join("；");
}

export function DiscoveryDrawer({ open, datasource, onClose }: DiscoveryDrawerProps) {
  const navigate = useNavigate();
  const nextKeyRef = useRef(1);
  const discoverControllerRef = useRef<AbortController | null>(null);
  const applyControllerRef = useRef<AbortController | null>(null);
  const [tables, setTables] = useState<EditableTableRef[]>([]);
  const [sampling, setSampling] = useState(true);
  const [sampleRows, setSampleRows] = useState(10);
  const [discovering, setDiscovering] = useState(false);
  const [applying, setApplying] = useState(false);
  const [result, setResult] = useState<DiscoveryResponse | null>(null);
  const [selectedKeys, setSelectedKeys] = useState<string[]>([]);
  const [outcome, setOutcome] = useState<DiscoveryApplyResponse | null>(null);
  const [errorText, setErrorText] = useState("");

  useEffect(() => {
    if (!open || !datasource) return;
    nextKeyRef.current = 2;
    setTables([{
      key: 1,
      schema: datasource.db_type === "postgres" ? "public" : datasource.database,
      table: "",
    }]);
    setSampling(true);
    setSampleRows(10);
    setResult(null);
    setSelectedKeys([]);
    setOutcome(null);
    setErrorText("");
  }, [datasource, open]);

  useEffect(() => () => {
    discoverControllerRef.current?.abort();
    applyControllerRef.current?.abort();
  }, []);

  const selectedFindings = useMemo(() => {
    if (!result) return [];
    const selected = new Set(selectedKeys);
    return result.findings.filter((finding) => selected.has(discoveryFindingKey(finding)) && finding.applicable && finding.recommended_rule);
  }, [result, selectedKeys]);

  const updateTable = (key: number, field: "schema" | "table", value: string) => {
    setTables((current) => current.map((table) => table.key === key ? { ...table, [field]: value } : table));
  };

  const addTable = () => {
    if (tables.length >= MAX_TABLES) return;
    const key = nextKeyRef.current++;
    setTables((current) => [...current, {
      key,
      schema: datasource?.db_type === "postgres" ? "public" : (datasource?.database || ""),
      table: "",
    }]);
  };

  const removeTable = (key: number) => {
    setTables((current) => current.filter((table) => table.key !== key));
  };

  const scan = async () => {
    if (!datasource || discovering) return;
    const normalized = tables.map(({ schema, table }) => ({ schema: schema.trim(), table: table.trim() }));
    if (normalized.length < 1 || normalized.some((table) => !table.schema || !table.table)) {
      setErrorText("请至少填写一个完整的 schema 与表名");
      return;
    }
    if (new Set(normalized.map((table) => `${table.schema}\u0000${table.table}`)).size !== normalized.length) {
      setErrorText("表范围存在重复项，请合并后重试");
      return;
    }
    discoverControllerRef.current?.abort();
    applyControllerRef.current?.abort();
    const controller = new AbortController();
    discoverControllerRef.current = controller;
    setDiscovering(true);
    setErrorText("");
    setOutcome(null);
    try {
      const response = await discoverSensitiveColumns(datasource.id, {
        tables: normalized,
        sampling,
        ...(sampling ? { sample_rows: sampleRows } : {}),
        categories: [...sensitiveTypes],
      }, controller.signal);
      if (controller.signal.aborted) return;
      setResult(response);
      setSelectedKeys(response.findings.filter((finding) => finding.applicable).map(discoveryFindingKey));
    } catch (error: unknown) {
      if (!controller.signal.aborted && !isCanceled(error)) setErrorText(friendlyDiscoveryError(error, "discover"));
    } finally {
      if (discoverControllerRef.current === controller) discoverControllerRef.current = null;
      if (!controller.signal.aborted) setDiscovering(false);
    }
  };

  const acceptOutcome = (response: DiscoveryApplyResponse) => {
    setOutcome(response);
    setErrorText("");
    const hasAttention = response.counts.conflicts > 0 || response.counts.ambiguous > 0;
    if (hasAttention) void message.warning("草稿处理完成，部分同名列需要人工确认");
    else void message.success("脱敏草稿处理完成");
  };

  const apply = async () => {
    if (!datasource || selectedFindings.length === 0 || applying) return;
    applyControllerRef.current?.abort();
    const controller = new AbortController();
    applyControllerRef.current = controller;
    setApplying(true);
    setErrorText("");
    try {
      const response = await applyDiscoveryDrafts(datasource.id, {
        items: selectedFindings.map((finding) => ({
          schema: finding.schema,
          table: finding.table,
          column: finding.column,
          category: finding.category,
          sensitive_type: finding.recommended_rule!.sensitive_type,
          algo: finding.recommended_rule!.algo,
        })),
      }, controller.signal);
      if (!controller.signal.aborted) acceptOutcome(response);
    } catch (error: unknown) {
      if (controller.signal.aborted || isCanceled(error)) return;
      const conflictOutcome = httpStatus(error) === 409 ? discoveryApplyResponseFromError(error) : undefined;
      if (conflictOutcome) acceptOutcome(conflictOutcome);
      else setErrorText(friendlyDiscoveryError(error, "apply"));
    } finally {
      if (applyControllerRef.current === controller) applyControllerRef.current = null;
      if (!controller.signal.aborted) setApplying(false);
    }
  };

  const close = () => {
    discoverControllerRef.current?.abort();
    applyControllerRef.current?.abort();
    setDiscovering(false);
    setApplying(false);
    onClose();
  };

  const statusMap = outcome ? statusMapFromOutcome(outcome) : new Map<string, DiscoveryApplyStatus>();

  return (
    <Drawer
      className="dsc-drawer"
      open={open}
      width="min(980px, 100vw)"
      destroyOnClose
      title={(
        <span className="dsc-title">
          <SafetyCertificateOutlined />
          敏感列发现 · {datasource?.name || "数据源"}
        </span>
      )}
      onClose={close}
      footer={(
        <div className="dsc-footer">
          <Button onClick={close}>关闭</Button>
          <Space wrap>
            <Button icon={<SearchOutlined />} loading={discovering} disabled={applying} onClick={() => void scan()}>
              {result ? "重新发现" : "开始发现"}
            </Button>
            <Button type="primary" loading={applying} disabled={!result || selectedFindings.length === 0 || discovering} onClick={() => void apply()}>
              生成脱敏草稿{selectedFindings.length > 0 ? `（${selectedFindings.length}）` : ""}
            </Button>
          </Space>
        </div>
      )}
    >
      <Alert
        className="dsc-boundary-alert"
        type="info"
        showIcon
        message="受控同步扫描"
        description="当前后端契约要求显式指定 1–20 个表，不执行未界定的全库扫描；元数据总列数最多 500，超限时请拆分范围。样本只用于本次分类，不会返回到页面。"
      />

      <section className="dsc-scope">
        <div className="dsc-section-heading">
          <div>
            <h3>扫描范围</h3>
            <p>填写数据库账号可见的 schema 与基础表。已添加 {tables.length} 个，还可添加 {MAX_TABLES - tables.length} 个。</p>
          </div>
          <Button icon={<PlusOutlined />} disabled={tables.length >= MAX_TABLES} onClick={addTable}>添加表</Button>
        </div>
        <div className="dsc-table-inputs">
          {tables.map((table, index) => (
            <div className="dsc-table-input" key={table.key}>
              <span>{index + 1}</span>
              <Input value={table.schema} placeholder="schema" aria-label={`第 ${index + 1} 项 schema`} onChange={(event) => updateTable(table.key, "schema", event.target.value)} />
              <Input value={table.table} placeholder="表名" aria-label={`第 ${index + 1} 项表名`} onChange={(event) => updateTable(table.key, "table", event.target.value)} />
              <Button type="text" danger icon={<DeleteOutlined />} aria-label={`删除第 ${index + 1} 项`} disabled={tables.length === 1} onClick={() => removeTable(table.key)} />
            </div>
          ))}
        </div>
        <div className="dsc-sampling">
          <div>
            <Typography.Text strong>样本校验</Typography.Text>
            <Typography.Text type="secondary">开启后提升识别置信度；关闭采样时最高为中置信度。</Typography.Text>
          </div>
          <Space>
            <Switch checked={sampling} checkedChildren="开启" unCheckedChildren="关闭" onChange={setSampling} />
            <InputNumber value={sampleRows} min={1} max={20} disabled={!sampling} addonAfter="行" onChange={(value) => setSampleRows(typeof value === "number" ? value : 10)} />
          </Space>
        </div>
      </section>

      {errorText ? <Alert className="dsc-error-alert" type="warning" showIcon message={errorText} /> : null}

      {result ? (
        <>
          <div className="dsc-stats">
            <Statistic title="扫描表" value={result.stats.tables_scanned} suffix={`/ ${result.stats.tables_requested}`} />
            <Statistic title="读取列" value={result.stats.columns_seen} />
            <Statistic title="候选列" value={result.stats.candidate_columns} />
            <Statistic title="采样列" value={result.stats.sampled_columns} />
            <Statistic title="采样值" value={result.stats.sampled_values_count} />
            <Statistic title="发现项" value={result.stats.findings_count} />
          </div>
          <DiscoveryResults
            result={result}
            selectedKeys={selectedKeys}
            statuses={statusMap}
            onSelectionChange={(keys) => {
              setSelectedKeys(keys);
              setOutcome(null);
            }}
          />
        </>
      ) : null}

      {outcome ? (
        <Alert
          className="dsc-outcome-alert"
          type={outcome.counts.conflicts > 0 || outcome.counts.ambiguous > 0 ? "warning" : "success"}
          showIcon
          message={applySummary(outcome) || "未生成新草稿"}
          description={(
            <div className="dsc-outcome-copy">
              <p>新草稿默认 enabled=false，不会生效。作用域为“数据源 + 列名”，table_name 留空，同名列会统一生效；请到“脱敏规则”页核对后手动启用。</p>
              {outcome.counts.conflicts > 0 ? <p>冲突项：同名列已存在不同规则，请先在脱敏规则页比较类型与算法。</p> : null}
              {outcome.counts.ambiguous > 0 ? <p>待确认项：多个表的同名列识别为不同类别，请缩小到单表重新发现并人工确认。</p> : null}
              <Button size="small" type="primary" onClick={() => navigate("/mask-rules")}>前往脱敏规则</Button>
            </div>
          )}
        />
      ) : null}
    </Drawer>
  );
}
