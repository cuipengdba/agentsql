import { ApiOutlined, DeleteOutlined, EditOutlined, PlusOutlined, SafetyCertificateOutlined } from "@ant-design/icons";
import { Alert, Button, Checkbox, Drawer, Form, Input, InputNumber, Modal, Pagination, Select, Space, Table, Tag, message } from "antd";
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

const defaultDatasource: Partial<DatasourceFormValues> = {
  db_type: "postgres",
  port: 5432,
  conn_limit: 5,
  stmt_timeout_ms: 5_000,
  row_limit: 1_000,
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
      value: type.key, label: `${type.display_name} · ${type.default_port} · ${type.capability}`,
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
    { title: "能力档位", dataIndex: "db_type", width: 150, render: (value: string) => typeByKey[value]?.capability || "未知" },
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
              form.setFieldValue("port", typeByKey[dbType]?.default_port);
              if (dbType === "sqlserver") {
                form.setFieldValue("tls_mode", "strict");
                form.setFieldValue("trust_server_certificate", false);
              }
            }
          }}
        >
          <Form.Item name="id" label="数据源 ID" rules={[{ required: true, whitespace: true }]}><Input disabled={Boolean(editing)} /></Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="db_type" label="数据库类型" rules={[{ required: true }]}><Select options={groupedOptions} disabled={types.length === 0} /></Form.Item>
          {selectedSpec?.kind === "nosql" ? <Alert type="info" showIcon message={`${categoryLabels[selectedSpec.category]} · 默认端口 ${selectedSpec.default_port} · 连接级/低阶支持（nosql-connect）`} /> : null}
          <div className="cfg-form-grid">
            <Form.Item name="host" label="主机" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
            <Form.Item name="port" label="端口" rules={[{ required: true }]}><InputNumber min={1} max={65535} className="cfg-full-width" /></Form.Item>
          </div>
          <Form.Item name="database" label="数据库 / 命名空间" rules={selectedSpec?.requires_database ? [{ required: true, whitespace: true }] : []}><Input /></Form.Item>
          <Form.Item name="username" label="用户名" rules={selectedSpec?.requires_username ? [{ required: true, whitespace: true }] : []}><Input /></Form.Item>
          <Form.Item name="password" label="密码 / 令牌" rules={!editing && selectedSpec?.requires_password ? [{ required: true }] : []}><Input.Password placeholder={editing ? "留空表示不修改" : "依数据源要求填写"} /></Form.Item>
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
