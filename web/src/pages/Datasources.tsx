import { ApiOutlined, DeleteOutlined, EditOutlined, PlusOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { Alert, Button, Checkbox, Drawer, Form, Input, InputNumber, Modal, Pagination, Select, Space, Table, Tag, Tooltip, message } from "antd";
import type { TableProps } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";

import { createDatasource, deleteDatasource, listDatasources, listDatasourceTypes, nativePingDatasource, pingDatasource, updateDatasource } from "@/api/datasources";
import type { DatasourceInput, DatasourceTypeView, DatasourceView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { configLabel, dbTypeMeta } from "@/constants/labels";
import { DiscoveryDrawer } from "@/pages/datasources/DiscoveryDrawer";

import { apiErrorMessage, httpStatus, isCanceled } from "./config/utils";

interface DatasourceFormValues extends DatasourceInput {
  db_type: string;
}

const categoryLabels: Record<string, string> = {
  relational: "关系型", keyvalue: "键值", document: "文档", widecolumn: "宽列",
  graph: "图", timeseries: "时序", olap: "OLAP", vector: "向量",
};
const categoryOrder = ["relational", "keyvalue", "document", "widecolumn", "graph", "timeseries", "olap", "vector"];
const supportLabels: Record<DatasourceTypeView["support"]["level"], string> = {
  controlled_sql: "受控 SQL 子集",
  bounded_sql: "指定实例有界只读",
  controlled_read_tool: "指定工具受控读取",
  connection_preview: "连接 / 管理预览",
};

const defaultDatasource: Partial<DatasourceFormValues> = {
  db_type: "postgres",
  port: 5432,
  conn_limit: 5,
  stmt_timeout_ms: 5_000,
  row_limit: 1_000,
  read_control_json: "{}",
  trust_server_certificate: false,
};

export function Datasources() {
  const [form] = Form.useForm<DatasourceFormValues>();
  const [list, setList] = useState<DatasourceView[]>([]);
  const [types, setTypes] = useState<DatasourceTypeView[]>([]);
  const [typesFailed, setTypesFailed] = useState(false);
  const [selectedType, setSelectedType] = useState("postgres");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<DatasourceView | null>(null);
  const [saving, setSaving] = useState(false);
  const [pingingID, setPingingID] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<DatasourceView | null>(null);
  const [deleteConfirmation, setDeleteConfirmation] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [discoveryTarget, setDiscoveryTarget] = useState<DatasourceView | null>(null);
  const mountedRef = useRef(true);
  const requestRef = useRef(0);
  const controllerRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = ++requestRef.current;
    controllerRef.current = controller;
    setLoading(true);
    setFailed(false);
    try {
      const response = await listDatasources({ page, page_size: pageSize }, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || sequence !== requestRef.current) return;
      setList(response.list || []);
      setTotal(Number.isFinite(response.total) ? Math.max(0, response.total) : 0);
    } catch (error: unknown) {
      if (!controller.signal.aborted && !isCanceled(error) && mountedRef.current) setFailed(true);
    } finally {
      if (controllerRef.current === controller) controllerRef.current = null;
      if (mountedRef.current && sequence === requestRef.current) setLoading(false);
    }
  }, [page, pageSize]);

  useEffect(() => {
    mountedRef.current = true;
    void load();
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      requestRef.current += 1;
    };
  }, [load]);

  useEffect(() => {
    const controller = new AbortController();
    void listDatasourceTypes(controller.signal).then(setTypes).catch((error: unknown) => {
      if (!isCanceled(error)) setTypesFailed(true);
    });
    return () => controller.abort();
  }, []);

  const typeByKey = Object.fromEntries(types.map((type) => [type.key, type])) as Record<string, DatasourceTypeView>;
  const selectedSpec = typeByKey[selectedType];
  const groupedOptions = categoryOrder.map((category) => ({
    label: categoryLabels[category],
    options: types.filter((type) => type.category === category).map((type) => ({
      value: type.key, label: `${type.display_name} · ${type.default_port} · ${supportLabels[type.support.level]}`,
    })),
  })).filter((group) => group.options.length > 0);

  const openCreate = () => {
    setEditing(null);
    setSelectedType("postgres");
    form.resetFields();
    form.setFieldsValue(defaultDatasource);
    setDrawerOpen(true);
  };

  const openEdit = (datasource: DatasourceView) => {
    setEditing(datasource);
    setSelectedType(datasource.db_type);
    form.setFieldsValue({
      id: datasource.id,
      name: datasource.name,
      db_type: datasource.db_type as DatasourceFormValues["db_type"],
      host: datasource.host,
      port: datasource.port,
      database: datasource.database,
      username: datasource.username,
      password: "",
      conn_limit: datasource.conn_limit,
      stmt_timeout_ms: datasource.stmt_timeout_ms,
      row_limit: datasource.row_limit,
      tls_mode: datasource.tls_mode,
      tls_server_name: datasource.tls_server_name,
      tls_ca_file: datasource.tls_ca_file,
      read_control_json: datasource.read_control_json || "{}",
      trust_server_certificate: datasource.trust_server_certificate,
    });
    setDrawerOpen(true);
  };

  const submit = async () => {
    if (!selectedSpec) {
      void message.error("数据源类型尚未加载，请稍后重试");
      return;
    }
    let values: DatasourceFormValues;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    const input: DatasourceInput = {
      id: values.id,
      name: values.name.trim(),
      db_type: values.db_type,
      host: values.host.trim(),
      port: values.port,
      database: (values.database || "").trim(),
      username: (values.username || "").trim(),
      conn_limit: values.conn_limit,
      stmt_timeout_ms: values.stmt_timeout_ms,
      row_limit: values.row_limit,
      tls_mode: values.tls_mode,
      tls_server_name: values.tls_server_name?.trim(),
      tls_ca_file: values.tls_ca_file?.trim(),
      read_control_json: values.read_control_json?.trim() || "{}",
      trust_server_certificate: Boolean(values.trust_server_certificate),
    };
    if (values.password) input.password = values.password;
    setSaving(true);
    try {
      if (editing) await updateDatasource(editing.id, input);
      else await createDatasource(input);
      if (!mountedRef.current) return;
      setDrawerOpen(false);
      void message.success(editing ? "数据源已更新" : "数据源已创建");
      void load();
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, editing ? "更新数据源失败" : "创建数据源失败"));
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  const ping = async (datasource: DatasourceView) => {
    if (!typeByKey[datasource.db_type]) {
      void message.error("数据源类型尚未加载，无法选择连接测试路径");
      return;
    }
    setPingingID(datasource.id);
    try {
      const result = typeByKey[datasource.db_type]?.kind === "nosql"
        ? await nativePingDatasource(datasource.id) : await pingDatasource(datasource.id);
      if (!mountedRef.current) return;
      if (result.ok) void message.success(`连接成功，延迟 ${Number.isFinite(result.latency_ms) ? result.latency_ms : 0} ms`);
      else void message.error("连接测试未通过");
    } catch (error: unknown) {
      void message.error(apiErrorMessage(error, "连接失败，请检查网络与凭证"));
    } finally {
      if (mountedRef.current) setPingingID("");
    }
  };

  const remove = async () => {
    if (!deleteTarget || deleteConfirmation !== deleteTarget.name) return;
    setDeleting(true);
    try {
      await deleteDatasource(deleteTarget.id);
      if (!mountedRef.current) return;
      setDeleteTarget(null);
      setDeleteConfirmation("");
      void message.success("数据源已删除");
      if (list.length === 1 && page > 1) setPage((value) => value - 1);
      else void load();
    } catch (error: unknown) {
      if (httpStatus(error) === 409) void message.error("该数据源仍被权限策略引用，请先清理策略后再删除");
      else void message.error(apiErrorMessage(error, "删除数据源失败"));
    } finally {
      if (mountedRef.current) setDeleting(false);
    }
  };

  const columns: TableProps<DatasourceView>["columns"] = [
    { title: "ID", dataIndex: "id", width: 140, ellipsis: true, render: (value: string) => <code>{value}</code> },
    { title: "名称", dataIndex: "name", width: 140, ellipsis: true },
    { title: "类型", dataIndex: "db_type", width: 190, render: (value: string) => { const spec = typeByKey[value]; return <Space size={4}><Tag color={spec?.kind === "nosql" ? "cyan" : "blue"}>{spec?.display_name || configLabel(dbTypeMeta, value)}</Tag>{spec?.kind === "nosql" ? <Tag>{categoryLabels[spec.category]}</Tag> : null}</Space>; } },
    { title: "能力档位", dataIndex: "db_type", width: 175, render: (value: string) => { const support = typeByKey[value]?.support; return support ? <Tooltip title={support.scope}><Tag color={support.level === "connection_preview" ? "default" : "blue"}>{supportLabels[support.level]}</Tag></Tooltip> : "未知"; } },
    { title: "地址", key: "address", width: 190, render: (_: unknown, row: DatasourceView) => <code>{row.host}:{row.port}</code> },
    { title: "数据库", dataIndex: "database", width: 130, ellipsis: true },
    { title: "用户名", dataIndex: "username", width: 120, ellipsis: true },
    { title: "连接上限", dataIndex: "conn_limit", width: 100, align: "right" },
    { title: "超时(ms)", dataIndex: "stmt_timeout_ms", width: 105, align: "right" },
    { title: "行数上限", dataIndex: "row_limit", width: 100, align: "right" },
    { title: "密码", dataIndex: "has_password", width: 90, render: (value: boolean) => <Tag color={value ? "success" : "default"}>{value ? "已配置" : "未配置"}</Tag> },
    {
      title: "操作", key: "action", fixed: "right", width: 300,
      render: (_: unknown, row: DatasourceView) => <Space size={4}>
        <Button type="link" size="small" icon={<EditOutlined />} onClick={() => openEdit(row)}>编辑</Button>
        <Button type="link" size="small" icon={<ApiOutlined />} loading={pingingID === row.id} onClick={() => void ping(row)}>测试</Button>
        {typeByKey[row.db_type]?.kind === "relational" ? <Button type="link" size="small" icon={<SafetyCertificateOutlined />} onClick={() => setDiscoveryTarget(row)}>敏感发现</Button> : null}
        <Button type="link" danger size="small" icon={<DeleteOutlined />} onClick={() => { setDeleteTarget(row); setDeleteConfirmation(""); }}>删除</Button>
      </Space>,
    },
  ];

  return (
    <PageContainer title="数据源" subtitle="配置受控数据库连接与执行限额" extra={<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建数据源</Button>}>
      {failed ? <Alert className="cfg-inline-alert" type="error" showIcon message="数据源列表加载失败" action={<Button size="small" onClick={() => void load()}>重试</Button>} /> : null}
      {typesFailed ? <Alert type="error" showIcon message="数据源类型加载失败，请刷新页面后重试" /> : null}
      <div className="cfg-table"><Table<DatasourceView> rowKey="id" columns={columns} dataSource={list} loading={loading} pagination={false} scroll={{ x: 1_800 }} /></div>
      <div className="cfg-pagination"><Pagination current={page} pageSize={pageSize} total={total} pageSizeOptions={[20, 50, 100]} showSizeChanger onChange={(next, size) => { setPageSize(size); setPage(size !== pageSize ? 1 : next); }} /></div>

      <Drawer open={drawerOpen} title={editing ? "编辑数据源" : "新建数据源"} width={560} destroyOnClose onClose={() => setDrawerOpen(false)} extra={<Button type="primary" loading={saving} onClick={() => void submit()}>保存</Button>}>
        <Form<DatasourceFormValues>
          form={form}
          layout="vertical"
          onValuesChange={(changed: Partial<DatasourceFormValues>) => {
            const dbType = changed.db_type;
            if (dbType) {
              setSelectedType(dbType);
              form.setFieldValue("port", dbType === "clickhouse" ? 9440 : typeByKey[dbType]?.default_port);
              form.setFieldsValue({ tls_server_name: undefined, tls_ca_file: undefined, read_control_json: "{}" });
              if (dbType === "clickhouse") {
                form.setFieldValue("tls_mode", "strict");
                form.setFieldValue("trust_server_certificate", false);
              } else if (dbType === "sqlserver") {
                form.setFieldValue("tls_mode", "strict");
                form.setFieldValue("trust_server_certificate", false);
              } else if (dbType === "mongodb" || dbType === "redis" || dbType === "valkey") {
                form.setFieldValue("tls_mode", "strict");
                form.setFieldValue("trust_server_certificate", false);
              } else if (dbType === "postgres" || dbType === "vastbase" || dbType === "opengauss" || dbType === "dm") {
                form.setFieldValue("tls_mode", "verify-full");
                form.setFieldValue("trust_server_certificate", false);
              } else {
                form.setFieldsValue({ tls_mode: undefined, tls_server_name: undefined, tls_ca_file: undefined, trust_server_certificate: false });
              }
            }
            if (changed.tls_mode === "disable") {
              form.setFieldsValue({ tls_server_name: undefined, tls_ca_file: undefined });
            }
          }}
        >
          <Form.Item name="id" label="数据源 ID" rules={[{ required: true, whitespace: true }]}><Input disabled={Boolean(editing)} /></Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="db_type" label="数据库类型" rules={[{ required: true }]}><Select options={groupedOptions} disabled={types.length === 0} /></Form.Item>
          {selectedSpec?.support ? <Alert type="info" showIcon message={supportLabels[selectedSpec.support.level]} description={<>
            <div>{selectedSpec.support.scope}</div>
            <div>Agent 工具入口：{selectedSpec.support.agent_tools.length ? selectedSpec.support.agent_tools.join("、") : "无"}。每次调用仍需通过身份、权限与审计检查。</div>
            <a href={`https://github.com/cuipengdba/agentsql/blob/main/${selectedSpec.support.evidence_doc}`} target="_blank" rel="noopener noreferrer">查看验收证据</a>
          </>} /> : null}
          {selectedType === "vastbase" ? <Alert type="warning" showIcon message="Vastbase G100 Build 9 的 PG 模式仅支持指定实例的受限只读路径：明确 schema、带 WHERE 和 LIMIT 的单表 SELECT。启用 TLS 时请配置 CA 与主机名校验，并在服务端为只读账号拒绝明文连接；主备切换与完整生产支持尚未验收。" /> : null}
          {selectedType === "highgo" ? <Alert type="warning" showIcon message="瀚高 V9.0.10 企业版 PG 模式仅支持指定单实例的受限只读查询：明确 schema、带 WHERE 和 LIMIT 的单表 SELECT。请使用最小权限只读账号；其他版本、模式和完整生产支持尚未验收。" /> : null}
          {selectedType === "opengauss" ? <Alert type="warning" showIcon message="openGauss 7.0.0 LTS 仅开放指定实例的受限只读路径：明确 schema、FROM ONLY、带 WHERE 和整数 LIMIT 的单表 SELECT。必须配置 verify-full、可信 CA 和最小权限账号；GaussDB 商业版、主备和完整生产支持尚未验收。" /> : null}
          {selectedType === "clickhouse" ? <Alert type="info" showIcon message="ClickHouse 管理端仍提供连接级预览；Agent 查询请使用独立的 MCP query_analytics 受控入口。" /> : selectedType === "milvus" ? <Alert type="warning" showIcon message="Milvus 受控检索仅限 2.6.25 单机 default 数据库、TLS 与最小权限账号。Agent 使用 MCP search_vectors；管理端原有连接级预览不支持此 TLS 路径。" /> : null}
          <div className="cfg-form-grid">
            <Form.Item name="host" label="主机" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
            <Form.Item name="port" label="端口" rules={[{ required: true }]}><InputNumber min={1} max={65535} className="cfg-full-width" /></Form.Item>
          </div>
          <Form.Item name="database" label="数据库 / 命名空间" rules={selectedSpec?.requires_database ? [{ required: true, whitespace: true }] : []}><Input /></Form.Item>
          <Form.Item name="username" label="用户名" rules={selectedSpec?.requires_username ? [{ required: true, whitespace: true }] : []}><Input /></Form.Item>
          <Form.Item name="password" label="密码 / 令牌" rules={!editing && selectedSpec?.requires_password ? [{ required: true }] : []}><Input.Password placeholder={editing ? "留空表示不修改" : "依数据源要求填写"} /></Form.Item>
          {selectedType === "mongodb" || selectedType === "redis" || selectedType === "valkey" || selectedType === "clickhouse" || selectedType === "milvus" ? <>
            <Alert type="info" showIcon
              message={selectedType === "mongodb" ? "MongoDB 副本集受控读取" : selectedType === "redis" || selectedType === "valkey" ? "Redis / Valkey Sentinel 受控读取" : selectedType === "clickhouse" ? "ClickHouse 查询资源预算" : "Milvus 租户隔离与标量过滤"}
              description={selectedType === "mongodb" ? "填写三个受信成员、replica_set 和允许的集合索引；成员须使用可验证 TLS 证书。{} 保留原有单实例路径。JSON 中不得包含密码。" : selectedType === "redis" || selectedType === "valkey" ? "填写三个受信 Sentinel、允许的主节点及单独的 Sentinel ACL 用户；密码由运维放入与数据源绑定的网关环境变量。{} 保留原有单实例路径。保存后编辑可查看变量名。" : selectedType === "clickhouse" ? "配置只会收紧已有查询资源上限。{} 保留默认预算。" : "配置租户字段后，网关从当前 Agent 绑定租户生成过滤条件；可另批准最多八个标量过滤字段。{} 保留旧版无行级租户过滤的路径。"} />
            {editing?.sentinel_password_env && selectedType !== "mongodb" ? <code>{editing.sentinel_password_env}</code> : null}
            <Form.Item name="read_control_json" label="拓扑与读取约束 JSON" rules={[{ required: true, whitespace: true }]}>
              <Input.TextArea rows={7} spellCheck={false} placeholder={selectedType === "mongodb" ? '{"mongo":{"replica_set":"rs0","members":["mongo1.example:27017","mongo2.example:27017","mongo3.example:27017"],"indexes":[{"collection":"customers","name":"customer_id_1","fields":["customer_id"]}]}}' : selectedType === "redis" || selectedType === "valkey" ? '{"redis_sentinel":{"master_name":"mymaster","sentinels":["sentinel1.example:26379","sentinel2.example:26379","sentinel3.example:26379"],"allowed_masters":["redis1.example:6379","redis2.example:6379"],"sentinel_username":"sentinel_reader"}}' : selectedType === "clickhouse" ? '{"clickhouse":{"max_rows_to_read":1000,"max_bytes_to_read":262144,"max_memory_usage":33554432}}' : '{"milvus":{"tenant_field":"tenant_id","filter_fields":["status","region"]}}'} />
            </Form.Item>
          </> : null}
          <Form.Item noStyle shouldUpdate={(previous, current) => previous.db_type !== current.db_type || previous.tls_mode !== current.tls_mode}>
            {({ getFieldValue }) => getFieldValue("db_type") === "sqlserver" ? <>
              <Alert className="cfg-inline-alert" type="info" showIcon message="SQL Server 2025 仅开放受控只读 SELECT；TLS 不可关闭，默认使用 TDS 8.0 strict。" />
              <Form.Item name="tls_mode" label="TLS 模式" rules={[{ required: true }]}>
                <Select options={[{ value: "strict", label: "strict（推荐，TDS 8.0）" }, { value: "verify-full", label: "verify-full（兼容模式）" }]} />
              </Form.Item>
              <Form.Item name="tls_server_name" label="证书主机名（可选）"><Input placeholder="db.example.com" /></Form.Item>
              <Form.Item name="tls_ca_file" label="CA / 服务器证书文件（服务端路径，可选）"><Input /></Form.Item>
              {getFieldValue("tls_mode") === "verify-full" ?
                <Form.Item name="trust_server_certificate" valuePropName="checked"><Checkbox>信任未验证证书（仅限受控开发环境）</Checkbox></Form.Item> : null}
            </> : getFieldValue("db_type") === "clickhouse" ? <>
              <Alert className="cfg-inline-alert" type="warning" showIcon message="ClickHouse 受控查询仅限已验收的 26.8.31.2 单实例；需配置原生 TLS 端口 9440、数据库/账号密码、可验证 CA 和 readonly=1 的最小权限角色。原有明文连接仅保留管理预览。" />
              <Form.Item name="tls_mode" label="TLS 模式"><Select options={[{ value: "strict", label: "strict（受控查询，校验 CA 和主机名）" }, { value: "verify-full", label: "verify-full（受控查询）" }, { value: "disable", label: "disable（仅管理预览）" }]} /></Form.Item>
              {getFieldValue("tls_mode") === "strict" || getFieldValue("tls_mode") === "verify-full" ?
                <Form.Item name="tls_ca_file" label="CA 证书文件（网关服务端绝对路径）" rules={[{ required: true, whitespace: true }]}><Input placeholder="/etc/agentsql/certs/clickhouse-ca.crt" /></Form.Item> : null}
              <Form.Item name="tls_server_name" label="证书主机名（可选，默认连接主机）"><Input placeholder="clickhouse.example.com" /></Form.Item>
            </> : getFieldValue("db_type") === "milvus" ? <>
              <Form.Item name="tls_mode" label="TLS 模式"><Select options={[{ value: "strict", label: "strict（受控检索，校验 CA 和主机名）" }, { value: "verify-full", label: "verify-full（受控检索）" }]} /></Form.Item>
              <Form.Item name="tls_ca_file" label="CA 证书文件（网关服务端绝对路径）" rules={[{ required: true, whitespace: true }]}><Input placeholder="/etc/agentsql/certs/milvus-ca.pem" /></Form.Item>
              <Form.Item name="tls_server_name" label="证书主机名（可选，默认连接主机）"><Input placeholder="milvus.example.com" /></Form.Item>
            </> : getFieldValue("db_type") === "mongodb" || getFieldValue("db_type") === "redis" || getFieldValue("db_type") === "valkey" ? <>
              <Form.Item name="tls_mode" label="TLS 模式" rules={[{ required: true }]}><Select options={[{ value: "strict", label: "strict（校验 CA 和成员主机名）" }, { value: "verify-full", label: "verify-full（校验 CA 和成员主机名）" }]} /></Form.Item>
              <Form.Item name="tls_ca_file" label="CA 证书文件（网关服务端绝对路径）" rules={[{ required: true, whitespace: true }]}><Input placeholder="/etc/agentsql/certs/native-ca.pem" /></Form.Item>
              <Form.Item name="tls_server_name" label="固定证书主机名（副本集或 Sentinel 模式必须留空）"><Input /></Form.Item>
            </> : getFieldValue("db_type") === "dm" ? <>
              <Alert className="cfg-inline-alert" type="warning" showIcon message="达梦 DM8 Pack3 指定实例的受限只读路径。新建连接须在服务器启用加密并配置可验证证书；旧版未设置 TLS 的数据源只保留兼容行为，不代表安全连接已验证。" />
              <Form.Item name="tls_mode" label="TLS 模式" rules={[{ required: true }]}>
                <Select options={[{ value: "verify-full", label: "verify-full（校验 CA 和主机名）" }]} />
              </Form.Item>
              <Form.Item name="tls_ca_file" label="CA 证书文件（网关服务端绝对路径）" rules={[{ required: true, whitespace: true }]}><Input placeholder="/etc/agentsql/certs/dm-ca.pem" /></Form.Item>
              <Form.Item name="tls_server_name" label="证书主机名" rules={[{ required: true, whitespace: true }]}><Input placeholder="dm.example.com" /></Form.Item>
            </> : getFieldValue("db_type") === "postgres" || getFieldValue("db_type") === "vastbase" || getFieldValue("db_type") === "opengauss" ? <>
              <Alert className="cfg-inline-alert" type="info" showIcon message={getFieldValue("db_type") === "opengauss" ? "openGauss 7.0.0 LTS 必须使用 verify-full、可信 CA 和匹配的证书主机名。" : getFieldValue("db_type") === "vastbase" ? "Vastbase G100 Build 9 PG 模式可在指定隔离实例使用 verify-full。该实例须启用 ssl 并通过账号访问规则拒绝明文；require_ssl=on 会拒绝当前 PG 驱动协议，不能用于这条适配路径。" : "PostgreSQL 协议路径（含已验证的金仓 PG 模式）可显式启用证书和主机名校验；金仓其他模式与完整生产支持仍需单独验收。"} />
              <Form.Item name="tls_mode" label="TLS 模式" rules={[{ required: true }]}>
                <Select options={getFieldValue("db_type") === "opengauss" ? [{ value: "verify-full", label: "verify-full（必选，校验 CA 和主机名）" }] : [{ value: "verify-full", label: "verify-full（推荐，校验 CA 和主机名）" }, { value: "disable", label: "disable（仅限隔离测试）" }]} />
              </Form.Item>
              {getFieldValue("tls_mode") === "verify-full" ? <>
                <Form.Item name="tls_ca_file" label="CA 证书文件（网关服务端绝对路径）" rules={[{ required: true, whitespace: true }]}><Input placeholder="/etc/agentsql/certs/root.crt" /></Form.Item>
                <Form.Item name="tls_server_name" label="证书主机名（可选，默认连接主机）"><Input placeholder="db.example.com" /></Form.Item>
              </> : null}
            </> : null}
          </Form.Item>
          <div className="cfg-form-grid cfg-form-grid-three">
            <Form.Item name="conn_limit" label="连接上限" rules={[{ required: true }]}><InputNumber min={1} className="cfg-full-width" /></Form.Item>
            <Form.Item name="stmt_timeout_ms" label="语句超时(ms)" rules={[{ required: true }]}><InputNumber min={1} className="cfg-full-width" /></Form.Item>
            <Form.Item name="row_limit" label="行数上限" rules={[{ required: true }]}><InputNumber min={1} className="cfg-full-width" /></Form.Item>
          </div>
        </Form>
      </Drawer>

      <Modal open={deleteTarget !== null} title="删除数据源" okText="确认删除" okButtonProps={{ danger: true, disabled: deleteConfirmation !== deleteTarget?.name }} confirmLoading={deleting} onOk={() => void remove()} onCancel={() => setDeleteTarget(null)}>
        <Alert type="warning" showIcon message="删除前请输入数据源名称，已关联策略的数据源无法删除" />
        <Input className="cfg-confirm-input" value={deleteConfirmation} onChange={(event) => setDeleteConfirmation(event.target.value)} placeholder={deleteTarget?.name} />
      </Modal>

      <DiscoveryDrawer open={discoveryTarget !== null} datasource={discoveryTarget} onClose={() => setDiscoveryTarget(null)} />
    </PageContainer>
  );
}
