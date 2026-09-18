import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { Alert, Button, Empty, Pagination, Popconfirm, Select, Space, Switch, Table, Tag, Tooltip, Typography, message } from "antd";
import type { TableProps } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";

import { listDatasources } from "@/api/datasources";
import { createMaskRule, deleteMaskRule, listMaskRules, updateMaskRule } from "@/api/maskRules";
import type { DatasourceView, MaskRuleInput, MaskRuleView } from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { configLabel, maskAlgoMeta, sensitiveTypeMeta } from "@/constants/labels";
import { apiErrorMessage, formatDateTime, httpStatus, isCanceled } from "@/pages/config/utils";
import { MaskRuleFormDrawer } from "@/pages/maskrules/MaskRuleFormDrawer";

function safeTotal(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

export function MaskRules() {
  const [datasources, setDatasources] = useState<DatasourceView[]>([]);
  const [datasourceID, setDatasourceID] = useState<string | undefined>();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [list, setList] = useState<MaskRuleView[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [queryVersion, setQueryVersion] = useState(0);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<MaskRuleView | null>(null);
  const [saving, setSaving] = useState(false);
  const [deletingID, setDeletingID] = useState("");
  const [togglingID, setTogglingID] = useState("");
  const mountedRef = useRef(true);
  const controllerRef = useRef<AbortController | null>(null);
  const requestSequenceRef = useRef(0);
  const mutationSequenceRef = useRef(0);

  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    void listDatasources({ page: 1, page_size: 100 }, controller.signal)
      .then((response) => {
        if (active && mountedRef.current) setDatasources(response.list || []);
      })
      .catch((error: unknown) => {
        if (active && mountedRef.current && !isCanceled(error)) {
          void message.error(apiErrorMessage(error, "数据源列表加载失败"));
        }
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, []);

  const loadPage = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    const sequence = ++requestSequenceRef.current;
    controllerRef.current = controller;
    setLoading(true);
    setFailed(false);
    try {
      const response = await listMaskRules({
        datasource_id: datasourceID || undefined,
        page,
        page_size: pageSize,
      }, controller.signal);
      if (!mountedRef.current || controller.signal.aborted || requestSequenceRef.current !== sequence) return;
      setList((response.list || []).slice(0, pageSize));
      setTotal(safeTotal(response.total));
    } catch (error: unknown) {
      if (!controller.signal.aborted && mountedRef.current && requestSequenceRef.current === sequence && !isCanceled(error)) {
        setFailed(true);
      }
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null;
        if (mountedRef.current) setLoading(false);
      }
    }
  }, [datasourceID, page, pageSize, queryVersion]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      controllerRef.current?.abort();
      controllerRef.current = null;
      requestSequenceRef.current += 1;
      mutationSequenceRef.current += 1;
    };
  }, []);

  useEffect(() => {
    void loadPage();
    return () => controllerRef.current?.abort();
  }, [loadPage]);

  const refresh = useCallback(() => {
    setQueryVersion((value) => value + 1);
  }, []);

  const changeDatasource = (value: string | undefined) => {
    setDatasourceID(value);
    setPage(1);
  };

  const changePage = (nextPage: number, nextPageSize: number) => {
    if (nextPageSize !== pageSize) {
      setPageSize(nextPageSize);
      setPage(1);
      return;
    }
    setPage(nextPage);
  };

  const openCreate = () => {
    setEditing(null);
    setDrawerOpen(true);
  };

  const openEdit = (record: MaskRuleView) => {
    setEditing(record);
    setDrawerOpen(true);
  };

  const closeDrawer = () => {
    if (saving) return;
    setDrawerOpen(false);
    setEditing(null);
  };

  const saveRule = async (input: MaskRuleInput) => {
    if (saving) return;
    const sequence = ++mutationSequenceRef.current;
    setSaving(true);
    try {
      if (editing) {
        await updateMaskRule(editing.id, input);
      } else {
        await createMaskRule(input);
      }
      if (!mountedRef.current || mutationSequenceRef.current !== sequence) return;
      void message.success(editing ? "脱敏规则已更新" : "脱敏规则已创建");
      setDrawerOpen(false);
      setEditing(null);
      refresh();
    } catch (error: unknown) {
      if (!mountedRef.current || mutationSequenceRef.current !== sequence || isCanceled(error)) return;
      if (httpStatus(error) === 409) void message.error("该数据源/全局范围下此列名已有规则");
      else void message.error(apiErrorMessage(error, editing ? "更新脱敏规则失败" : "创建脱敏规则失败"));
    } finally {
      if (mountedRef.current && mutationSequenceRef.current === sequence) setSaving(false);
    }
  };

  const removeRule = async (record: MaskRuleView) => {
    if (deletingID) return;
    const sequence = ++mutationSequenceRef.current;
    setDeletingID(record.id);
    try {
      await deleteMaskRule(record.id);
      if (!mountedRef.current || mutationSequenceRef.current !== sequence) return;
      void message.success("脱敏规则已删除");
      refresh();
    } catch (error: unknown) {
      if (!mountedRef.current || mutationSequenceRef.current !== sequence || isCanceled(error)) return;
      void message.error(apiErrorMessage(error, "删除脱敏规则失败"));
    } finally {
      if (mountedRef.current && mutationSequenceRef.current === sequence) setDeletingID("");
    }
  };

  const toggleRule = async (record: MaskRuleView, enabled: boolean) => {
    if (togglingID) return;
    setTogglingID(record.id);
    try {
      const updated = await updateMaskRule(record.id, {
        id: record.id,
        datasource_id: record.datasource_id,
        table_name: record.table_name,
        column_name: record.column_name,
        sensitive_type: record.sensitive_type,
        algo: record.algo,
        enabled,
      });
      if (!mountedRef.current) return;
      setList((current) => current.map((item) => item.id === record.id ? updated : item));
      void message.success(enabled ? "脱敏规则已启用" : "脱敏规则已停用");
    } catch (error: unknown) {
      if (!mountedRef.current || isCanceled(error)) return;
      if (httpStatus(error) === 409) void message.warning("同名列存在规则冲突，请刷新后核对");
      else void message.error(apiErrorMessage(error, enabled ? "启用脱敏规则失败" : "停用脱敏规则失败"));
    } finally {
      if (mountedRef.current) setTogglingID("");
    }
  };

  const datasourceNames = new Map(datasources.map((datasource) => [datasource.id, datasource.name]));
  const columns: TableProps<MaskRuleView>["columns"] = [
    { title: "规则 ID", dataIndex: "id", width: 190, render: (value: string) => <code className="msk-id">{value}</code> },
    {
      title: "数据源",
      dataIndex: "datasource_id",
      width: 180,
      render: (value: string | null | undefined) => value ? (
        <Tooltip title={value}><span>{datasourceNames.get(value) || value}</span></Tooltip>
      ) : <Tag color="default">全局</Tag>,
    },
    { title: "表名（预留）", dataIndex: "table_name", width: 170, render: (value: string) => value ? <code>{value}</code> : <Typography.Text type="secondary">留空（正常）</Typography.Text> },
    { title: "列名", dataIndex: "column_name", width: 170, render: (value: string) => <code>{value}</code> },
    {
      title: "敏感类型",
      dataIndex: "sensitive_type",
      width: 120,
      render: (value: string) => {
        const meta = sensitiveTypeMeta[value as keyof typeof sensitiveTypeMeta];
        const tag = <Tag color={meta?.color || "default"}>{configLabel(sensitiveTypeMeta, value)}</Tag>;
        return meta ? <Tooltip title={`脱敏后示例：${meta.example}`}>{tag}</Tooltip> : tag;
      },
    },
    {
      title: "算法",
      dataIndex: "algo",
      width: 100,
      render: (value: string) => {
        const meta = maskAlgoMeta[value as keyof typeof maskAlgoMeta];
        return <Tag color={meta?.color || "default"}>{configLabel(maskAlgoMeta, value)}</Tag>;
      },
    },
    {
      title: "生效状态",
      dataIndex: "enabled",
      width: 190,
      render: (enabled: boolean, record) => (
        <Space size={8}>
          <Switch
            size="small"
            checked={enabled}
            loading={togglingID === record.id}
            disabled={Boolean(togglingID) || Boolean(deletingID)}
            aria-label={`${enabled ? "停用" : "启用"}规则 ${record.column_name}`}
            onChange={(checked) => void toggleRule(record, checked)}
          />
          {enabled ? <Tag color="success">已启用</Tag> : <Tag color="warning">草稿·未生效</Tag>}
        </Space>
      ),
    },
    { title: "更新时间", dataIndex: "updated_at", width: 180, render: (value: string) => formatDateTime(value) },
    {
      title: "操作",
      key: "actions",
      fixed: "right",
      width: 150,
      render: (_, record) => (
        <Space size={4}>
          <Button type="link" size="small" icon={<EditOutlined />} onClick={() => openEdit(record)}>编辑</Button>
          <Popconfirm
            title="删除后该列将不再脱敏，确认删除？"
            okText="删除"
            cancelText="取消"
            okButtonProps={{ danger: true, loading: deletingID === record.id }}
            onConfirm={() => removeRule(record)}
          >
            <Button danger type="link" size="small" icon={<DeleteOutlined />} disabled={Boolean(deletingID)}>删除</Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <PageContainer
      title="脱敏"
      subtitle="按数据源 + 规范化列名匹配；发现草稿需人工核对并启用"
      extra={<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新增脱敏规则</Button>}
    >
      <Alert
        className="msk-info-alert"
        type="info"
        showIcon
        message="脱敏按 数据源 + 列名 匹配，同名列统一生效；首版不支持 表.列 级规则"
        description="敏感发现生成的规则 table_name 留空属于正常行为。标记为“草稿·未生效”的规则不会参与运行时脱敏，请核对数据源、列名、类型与算法后再启用。"
      />
      <div className="msk-toolbar">
        <Space wrap>
          <Typography.Text>数据源</Typography.Text>
          <Select
            className="msk-datasource-filter"
            allowClear
            showSearch
            optionFilterProp="label"
            value={datasourceID}
            placeholder="全部（含全局规则）"
            options={datasources.map((datasource) => ({
              label: `${datasource.name} · ${datasource.db_type}`,
              value: datasource.id,
            }))}
            onChange={changeDatasource}
          />
          <Typography.Text>共 <strong className="mono-text">{safeTotal(total).toLocaleString("zh-CN")}</strong> 条</Typography.Text>
        </Space>
        <Tooltip title="刷新当前页">
          <Button icon={<ReloadOutlined />} loading={loading} disabled={loading} onClick={refresh}>刷新</Button>
        </Tooltip>
      </div>
      {failed ? (
        <Alert
          className="msk-inline-error"
          type="error"
          showIcon
          message={list.length > 0 ? "当前页刷新失败，已保留上次结果" : "脱敏规则加载失败"}
          action={<Button size="small" onClick={refresh}>重试</Button>}
        />
      ) : null}
      <Table<MaskRuleView>
        className="msk-table"
        rowKey="id"
        columns={columns}
        dataSource={list}
        loading={loading}
        pagination={false}
        scroll={{ x: 1450 }}
        locale={{ emptyText: <Empty description="暂无脱敏规则，点右上角新增" /> }}
      />
      <div className="msk-pagination">
        <Pagination
          current={page}
          pageSize={pageSize}
          total={safeTotal(total)}
          pageSizeOptions={[20, 50, 100]}
          showSizeChanger
          showQuickJumper
          showTotal={(value) => `共 ${safeTotal(value).toLocaleString("zh-CN")} 条`}
          onChange={changePage}
        />
      </div>
      <MaskRuleFormDrawer
        open={drawerOpen}
        record={editing}
        datasources={datasources}
        loading={saving}
        onClose={closeDrawer}
        onSubmit={(input) => void saveRule(input)}
      />
    </PageContainer>
  );
}
