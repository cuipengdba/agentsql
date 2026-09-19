import { SaveOutlined } from "@ant-design/icons";
import { Alert, Button, Drawer, Form, Input, Select, Space, Switch, Typography, message } from "antd";
import { useEffect } from "react";

import type { DatasourceView, MaskRuleInput, MaskRuleView } from "@/api/types";
import {
  BLOCK_ALGORITHM,
  GENERIC_SENSITIVE_TYPE,
  HASH_ALGORITHM,
  HASH_REDACTION_UNAVAILABLE_MESSAGE,
  MASK_ALGORITHM,
  isMaskAlgorithm,
  isSensitiveType,
  isSensitiveTypeAllowedForAlgorithm,
  maskAlgorithmMeta,
  maskAlgorithmOptions,
  sensitiveTypeOptionsForAlgorithm,
  sensitiveTypePresentationForAlgorithm,
  sensitiveTypes,
} from "@/constants/sensitiveTypes";

export interface MaskRuleSubmitFailure {
  status?: number;
  errorCode?: string;
  message: string;
}

interface MaskRuleFormDrawerProps {
  open: boolean;
  record: MaskRuleView | null;
  datasources: DatasourceView[];
  loading: boolean;
  onClose: () => void;
  onSubmit: (input: MaskRuleInput) => Promise<MaskRuleSubmitFailure | null>;
}

interface MaskRuleFormValues {
  id: string;
  datasource_id?: string;
  column_name: string;
  sensitive_type: string;
  algo: string;
  enabled: boolean;
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
  const selectedAlgorithm = Form.useWatch("algo", form);
  const selectedSensitiveType = Form.useWatch("sensitive_type", form);

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue(record ? {
      id: record.id,
      datasource_id: record.datasource_id || undefined,
      column_name: record.column_name,
      sensitive_type: record.algo === MASK_ALGORITHM && record.sensitive_type === GENERIC_SENSITIVE_TYPE
        ? sensitiveTypes[0]
        : record.sensitive_type,
      algo: record.algo,
      enabled: record.enabled,
    } : {
      id: generateMaskRuleID(),
      datasource_id: undefined,
      column_name: "",
      sensitive_type: sensitiveTypes[0],
      algo: MASK_ALGORITHM,
      enabled: true,
    });
  }, [form, open, record]);

  const changeAlgorithm = (value: string) => {
    if (value === MASK_ALGORITHM && form.getFieldValue("sensitive_type") === GENERIC_SENSITIVE_TYPE) {
      form.setFieldValue("sensitive_type", sensitiveTypes[0]);
    }
    form.setFields([
      { name: "algo", errors: [] },
      { name: "sensitive_type", errors: [] },
    ]);
  };

  const submit = async (values: MaskRuleFormValues) => {
    if (!isSensitiveType(values.sensitive_type)) {
      form.setFields([{ name: "sensitive_type", errors: ["请选择支持的敏感类型"] }]);
      return;
    }
    if (!isMaskAlgorithm(values.algo)) {
      form.setFields([{ name: "algo", errors: ["请选择支持的脱敏算法"] }]);
      return;
    }
    if (!isSensitiveTypeAllowedForAlgorithm(values.sensitive_type, values.algo)) {
      form.setFields([
        { name: "algo", errors: ["打码（mask）不支持通用敏感值"] },
        { name: "sensitive_type", errors: ["通用敏感值仅支持 hash/block"] },
      ]);
      return;
    }
    const input: MaskRuleInput = {
      id: values.id,
      datasource_id: values.datasource_id || null,
      table_name: record ? record.table_name : "",
      column_name: values.column_name,
      sensitive_type: values.sensitive_type,
      algo: values.algo,
      enabled: values.enabled,
    };
    const failure = await onSubmit(input);
    if (!failure) return;
    if (failure.status === 503 && failure.errorCode === "HASH_REDACTION_UNAVAILABLE") {
      void message.error(values.algo === HASH_ALGORITHM
        ? HASH_REDACTION_UNAVAILABLE_MESSAGE
        : "服务端拒绝当前脱敏规则操作，请刷新后重试");
      return;
    }
    if (failure.status === 422 && failure.errorCode === "INVALID_MASK_RULE") {
      const backendMessage = failure.message || "脱敏算法与敏感类型组合无效";
      form.setFields([
        { name: "algo", errors: [backendMessage] },
        { name: "sensitive_type", errors: [backendMessage] },
      ]);
      void message.error(backendMessage);
      return;
    }
    void message.error(failure.message);
  };

  const algorithm = isMaskAlgorithm(selectedAlgorithm) ? selectedAlgorithm : MASK_ALGORITHM;
  const type = isSensitiveType(selectedSensitiveType) ? selectedSensitiveType : sensitiveTypes[0];
  const typePresentation = sensitiveTypePresentationForAlgorithm(type, algorithm);
  const isHash = algorithm === HASH_ALGORITHM;
  const isBlock = algorithm === BLOCK_ALGORITHM;

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
        <Form.Item
          name="algo"
          label="算法"
          extra={maskAlgorithmMeta[algorithm].description}
          rules={[{ required: true, message: "请选择算法" }]}
        >
          <Select options={maskAlgorithmOptions} onChange={changeAlgorithm} />
        </Form.Item>
        {isHash ? (
          <Alert
            showIcon
            type="warning"
            message="哈希指纹不可逆且具有确定性"
            description="相同原值会得到相同指纹，可用于跨表等值关联和去重，也会暴露相等关系；指纹无法还原原文。启用前必须在服务端配置 AGENTSQL_REDACTION_HASH_KEY。"
          />
        ) : null}
        {isBlock ? (
          <Alert
            showIcon
            type="info"
            message="整值阻断为固定 ***"
            description="命中列的每个非空值都会统一替换为固定 ***，无需配置密钥，不可还原也不可关联。"
          />
        ) : null}
        <Form.Item
          name="sensitive_type"
          label="敏感类型"
          extra={typePresentation.description}
          rules={[{ required: true, message: "请选择敏感类型" }]}
        >
          <Select options={sensitiveTypeOptionsForAlgorithm(algorithm)} />
        </Form.Item>
        <Typography.Paragraph type="secondary">
          <Typography.Text strong>脱敏示例：</Typography.Text>{" "}
          <code>{typePresentation.example}</code>
          {isHash ? "（定长、不可逆）" : null}
        </Typography.Paragraph>
        <Form.Item
          name="enabled"
          label="生效状态"
          valuePropName="checked"
          extra="停用规则可作为草稿保存，不参与运行时脱敏。"
        >
          <Switch checkedChildren="启用" unCheckedChildren="停用" />
        </Form.Item>
      </Form>
    </Drawer>
  );
}
