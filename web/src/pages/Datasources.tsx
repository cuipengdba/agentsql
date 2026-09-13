import { ApiOutlined, DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import { Alert, Button, Drawer, Form, Input, InputNumber, Modal, Pagination, Select, Space, Table, Tag, message } from "antd";
import type { TableProps } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";

import { createDatasource, deleteDatasource, listDatasources, pingDatasource, updateDatasource } from "@/api/datasources";
import type { DatasourceInput, DatasourceView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { configLabel, dbTypeMeta } from "@/constants/labels";

import { apiErrorMessage, httpStatus, isCanceled } from "./config/utils";

interface DatasourceFormValues extends DatasourceInput {
  db_type: "postgres" | "mysql";
}

const defaultDatasource: Partial<DatasourceFormValues> = {
  db_type: "postgres",
  port: 5432,
  conn_limit: 5,
  stmt_timeout_ms: 5_000,
  row_limit: 1_000,
};

export function Datasources() {
  const [form] = Form.useForm<DatasourceFormValues>();
  const [list, setList] = useState<DatasourceView[]>([]);
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
      const response = await listDatasources({ page, page_size: pageSize });
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

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue(defaultDatasource);
    setDrawerOpen(true);
  };

  const openEdit = (datasource: DatasourceView) => {
    setEditing(datasource);
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
    });
    setDrawerOpen(true);
  };

  const submit = async () => {
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
      database: values.database.trim(),
      username: values.username.trim(),
      conn_limit: values.conn_limit,
      stmt_timeout_ms: values.stmt_timeout_ms,
      row_limit: values.row_limit,
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
    setPingingID(datasource.id);
    try {
      const result = await pingDatasource(datasource.id);
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
    { title: "类型", dataIndex: "db_type", width: 110, render: (value: string) => <Tag color={dbTypeMeta[value as keyof typeof dbTypeMeta]?.color}>{configLabel(dbTypeMeta, value)}</Tag> },
    { title: "地址", key: "address", width: 190, render: (_: unknown, row: DatasourceView) => <code>{row.host}:{row.port}</code> },
    { title: "数据库", dataIndex: "database", width: 130, ellipsis: true },
    { title: "用户名", dataIndex: "username", width: 120, ellipsis: true },
    { title: "连接上限", dataIndex: "conn_limit", width: 100, align: "right" },
    { title: "超时(ms)", dataIndex: "stmt_timeout_ms", width: 105, align: "right" },
    { title: "行数上限", dataIndex: "row_limit", width: 100, align: "right" },
    { title: "密码", dataIndex: "has_password", width: 90, render: (value: boolean) => <Tag color={value ? "success" : "default"}>{value ? "已配置" : "未配置"}</Tag> },
    {
      title: "操作", key: "action", fixed: "right", width: 220,
      render: (_: unknown, row: DatasourceView) => <Space size={4}>
        <Button type="link" size="small" icon={<EditOutlined />} onClick={() => openEdit(row)}>编辑</Button>
        <Button type="link" size="small" icon={<ApiOutlined />} loading={pingingID === row.id} onClick={() => void ping(row)}>测试</Button>
        <Button type="link" danger size="small" icon={<DeleteOutlined />} onClick={() => { setDeleteTarget(row); setDeleteConfirmation(""); }}>删除</Button>
      </Space>,
    },
  ];

  return (
    <PageContainer title="数据源" subtitle="配置受控数据库连接与执行限额" extra={<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建数据源</Button>}>
      {failed ? <Alert className="cfg-inline-alert" type="error" showIcon message="数据源列表加载失败" action={<Button size="small" onClick={() => void load()}>重试</Button>} /> : null}
      <div className="cfg-table"><Table<DatasourceView> rowKey="id" columns={columns} dataSource={list} loading={loading} pagination={false} scroll={{ x: 1_520 }} /></div>
      <div className="cfg-pagination"><Pagination current={page} pageSize={pageSize} total={total} pageSizeOptions={[20, 50, 100]} showSizeChanger onChange={(next, size) => { setPageSize(size); setPage(size !== pageSize ? 1 : next); }} /></div>

      <Drawer open={drawerOpen} title={editing ? "编辑数据源" : "新建数据源"} width={560} destroyOnClose onClose={() => setDrawerOpen(false)} extra={<Button type="primary" loading={saving} onClick={() => void submit()}>保存</Button>}>
        <Form<DatasourceFormValues>
          form={form}
          layout="vertical"
          onValuesChange={(changed: Partial<DatasourceFormValues>) => {
            const dbType = changed.db_type;
            if (dbType) form.setFieldValue("port", dbType === "postgres" ? 5432 : 3306);
          }}
        >
          <Form.Item name="id" label="数据源 ID" rules={[{ required: true, whitespace: true }]}><Input disabled={Boolean(editing)} /></Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="db_type" label="数据库类型" rules={[{ required: true }]}><Select options={[{ value: "postgres", label: "PostgreSQL" }, { value: "mysql", label: "MySQL" }]} /></Form.Item>
          <div className="cfg-form-grid">
            <Form.Item name="host" label="主机" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
            <Form.Item name="port" label="端口" rules={[{ required: true }]}><InputNumber min={1} max={65535} className="cfg-full-width" /></Form.Item>
          </div>
          <Form.Item name="database" label="数据库" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="username" label="用户名" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
          <Form.Item name="password" label="密码" rules={editing ? [] : [{ required: true, message: "新建数据源必须填写密码" }]}><Input.Password placeholder={editing ? "留空表示不修改密码" : "输入数据库密码"} /></Form.Item>
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
    </PageContainer>
  );
}
