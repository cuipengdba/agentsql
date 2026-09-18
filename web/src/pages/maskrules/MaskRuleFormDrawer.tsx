import { SaveOutlined } from "@ant-design/icons";
import { Button, Drawer, Form, Input, Select, Space, Typography } from "antd";
import { useEffect } from "react";

import type { DatasourceView, MaskRuleInput, MaskRuleView } from "@/api/types";
import {
  MASK_ALGORITHM,
  isMaskAlgorithm,
  isSensitiveType,
  maskAlgorithmOptions,
  sensitiveTypeOptions,
  sensitiveTypes,
} from "@/constants/sensitiveTypes";

interface MaskRuleFormDrawerProps {
  open: boolean;
  record: MaskRuleView | null;
  datasources: DatasourceView[];
  loading: boolean;
  onClose: () => void;
  onSubmit: (input: MaskRuleInput) => void;
}

interface MaskRuleFormValues {
  id: string;
  datasource_id?: string;
  column_name: string;
  sensitive_type: string;
  algo: string;
}

function generateMaskRuleID(): string {
  const timestamp = Date.now().toString(36);
  const random = Math.random().toString(36).slice(2, 8).padEnd(6, "0");
  return `msk_${timestamp}${random}`;
}

function trimmedRule(label: string) {
  return {
    validator: (_rule: unknown, value: string | undefined) => {
      if (!value) return Promise.reject(new Error(`${label}不能为空`));
      if (value.trim() !== value) return Promise.reject(new Error(`${label}首尾不能包含空格`));
      return Promise.resolve();
    },
  };
}

export function MaskRuleFormDrawer({
  open,
  record,
  datasources,
  loading,
  onClose,
  onSubmit,
}: MaskRuleFormDrawerProps) {
  const [form] = Form.useForm<MaskRuleFormValues>();

  useEffect(() => {
    if (!open) return;
    form.setFieldsValue(record ? {
      id: record.id,
      datasource_id: record.datasource_id || undefined,
      column_name: record.column_name,
      sensitive_type: record.sensitive_type,
      algo: record.algo,
    } : {
      id: generateMaskRuleID(),
      datasource_id: undefined,
      column_name: "",
      sensitive_type: sensitiveTypes[0],
      algo: MASK_ALGORITHM,
    });
  }, [form, open, record]);

  const submit = (values: MaskRuleFormValues) => {
    if (!isSensitiveType(values.sensitive_type)) {
      form.setFields([{ name: "sensitive_type", errors: ["请选择支持的敏感类型"] }]);
      return;
    }
    if (!isMaskAlgorithm(values.algo)) {
      form.setFields([{ name: "algo", errors: ["仅支持打码（mask）算法"] }]);
      return;
    }
    const input: MaskRuleInput = {
      id: values.id,
      datasource_id: values.datasource_id || null,
      table_name: record ? record.table_name : "",
      column_name: values.column_name,
      sensitive_type: values.sensitive_type,
      algo: values.algo,
    };
    onSubmit(input);
  };

  return (
    <Drawer
      className="msk-form-drawer"
      destroyOnClose
      open={open}
      title={record ? "编辑脱敏规则" : "新增脱敏规则"}
      width={520}
      onClose={onClose}
      footer={(
        <div className="msk-drawer-footer">
          <Space>
            <Button onClick={onClose}>取消</Button>
            <Button type="primary" icon={<SaveOutlined />} loading={loading} onClick={() => form.submit()}>保存</Button>
          </Space>
        </div>
      )}
    >
      <Form form={form} layout="vertical" requiredMark="optional" onFinish={submit}>
        <Form.Item name="datasource_id" label="数据源">
          <Select
            allowClear
            showSearch
            optionFilterProp="label"
            placeholder="留空表示全局规则"
            options={datasources.map((datasource) => ({
              label: `${datasource.name} · ${datasource.db_type}`,
              value: datasource.id,
            }))}
          />
        </Form.Item>
        <Form.Item
          name="id"
          label="规则 ID"
          extra="使用小写字母、数字、下划线或连字符"
          rules={[
            trimmedRule("规则 ID"),
            { pattern: /^[a-z0-9_-]+$/, message: "仅允许小写字母、数字、下划线或连字符" },
          ]}
        >
          <Input readOnly={record !== null} />
        </Form.Item>
        <Typography.Paragraph type="secondary">
          表名为预留字段，v0.1 按列名匹配、不参与表+列匹配；编辑时会保留历史表名。
        </Typography.Paragraph>
        <Form.Item
          name="column_name"
          label="列名"
          extra="按结果列名精确匹配，不做模糊匹配"
          rules={[trimmedRule("列名")]}
        >
          <Input placeholder="命中列名，如 phone" />
        </Form.Item>
        <Form.Item name="sensitive_type" label="敏感类型" rules={[{ required: true, message: "请选择敏感类型" }]}>
          <Select options={sensitiveTypeOptions} />
        </Form.Item>
        <Form.Item name="algo" label="算法" rules={[{ required: true, message: "请选择算法" }]}>
          <Select options={maskAlgorithmOptions} />
        </Form.Item>
        <Typography.Text type="secondary">哈希、区间等算法将在后续版本提供。</Typography.Text>
      </Form>
    </Drawer>
  );
}
