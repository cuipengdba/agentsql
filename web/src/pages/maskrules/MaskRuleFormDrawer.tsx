import { SaveOutlined } from "@ant-design/icons";
import { Alert, Button, Drawer, Form, Input, InputNumber, Select, Space, Switch, Typography, message } from "antd";
import { useEffect, useState } from "react";

import type { DatasourceView, MaskRuleInput, MaskRuleView } from "@/api/types";
import {
  BLOCK_ALGORITHM,
  DATE_SENSITIVE_TYPE,
  HASH_ALGORITHM,
  HASH_REDACTION_UNAVAILABLE_MESSAGE,
  MASK_ALGORITHM,
  NUMBER_SENSITIVE_TYPE,
  RANGE_ALGORITHM,
  isMaskAlgorithm,
  isSensitiveType,
  isSensitiveTypeAllowedForAlgorithm,
  maskAlgorithmMeta,
  maskAlgorithmOptions,
  rangeExample,
  sensitiveTypeOptionsForAlgorithm,
  sensitiveTypePresentationForAlgorithm,
  sensitiveTypes,
} from "@/constants/sensitiveTypes";
import type { RangeGranularity } from "@/constants/sensitiveTypes";

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
  schema_name?: string;
  table_name?: string;
  column_name: string;
  sensitive_type: string;
  algo: string;
  range_bucket_width?: number | null;
  range_bucket_offset?: number | null;
  range_granularity?: RangeGranularity | null;
  enabled: boolean;
}

function isIntegerInRange(value: unknown, min: number, max: number): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= min && value <= max;
}

function isRangeGranularity(value: unknown): value is RangeGranularity {
  return value === "year" || value === "quarter" || value === "month";
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
  const [scopeConflict, setScopeConflict] = useState<MaskRuleSubmitFailure | null>(null);
  const selectedDatasourceID = Form.useWatch("datasource_id", form);
  const selectedSchemaName = Form.useWatch("schema_name", form);
  const selectedTableName = Form.useWatch("table_name", form);
  const selectedAlgorithm = Form.useWatch("algo", form);
  const selectedSensitiveType = Form.useWatch("sensitive_type", form);
  const selectedBucketWidth = Form.useWatch("range_bucket_width", form);
  const selectedBucketOffset = Form.useWatch("range_bucket_offset", form);
  const selectedGranularity = Form.useWatch("range_granularity", form);

  useEffect(() => {
    if (!open) return;
    setScopeConflict(null);
    form.resetFields();
    form.setFieldsValue(record ? {
      id: record.id,
      datasource_id: record.datasource_id ?? undefined,
      schema_name: record.schema_name ?? "",
      table_name: record.table_name ?? "",
      column_name: record.column_name,
      sensitive_type: isSensitiveTypeAllowedForAlgorithm(record.sensitive_type, record.algo)
        ? record.sensitive_type
        : record.algo === RANGE_ALGORITHM ? NUMBER_SENSITIVE_TYPE : sensitiveTypes[0],
      algo: record.algo,
      range_bucket_width: record.algo === RANGE_ALGORITHM && record.sensitive_type === NUMBER_SENSITIVE_TYPE
        ? record.range_bucket_width ?? 10
        : undefined,
      range_bucket_offset: record.algo === RANGE_ALGORITHM && record.sensitive_type === NUMBER_SENSITIVE_TYPE
        ? record.range_bucket_offset ?? 0
        : undefined,
      range_granularity: record.algo === RANGE_ALGORITHM && record.sensitive_type === DATE_SENSITIVE_TYPE
        ? record.range_granularity ?? "year"
        : undefined,
      enabled: record.enabled,
    } : {
      id: generateMaskRuleID(),
      datasource_id: undefined,
      schema_name: "",
      table_name: "",
      column_name: "",
      sensitive_type: sensitiveTypes[0],
      algo: MASK_ALGORITHM,
      enabled: true,
    });
  }, [form, open, record]);

  const changeAlgorithm = (value: string) => {
    if (!isMaskAlgorithm(value)) return;
    const currentTypeValue: unknown = form.getFieldValue("sensitive_type");
    const currentType = isSensitiveType(currentTypeValue) ? currentTypeValue : sensitiveTypes[0];
    if (value === RANGE_ALGORITHM) {
      const nextType = isSensitiveTypeAllowedForAlgorithm(currentType, value)
        ? currentType
        : NUMBER_SENSITIVE_TYPE;
      if (nextType === NUMBER_SENSITIVE_TYPE) {
        form.setFieldsValue({
          sensitive_type: nextType,
          range_bucket_width: form.getFieldValue("range_bucket_width") ?? 10,
          range_bucket_offset: form.getFieldValue("range_bucket_offset") ?? 0,
          range_granularity: undefined,
        });
      } else {
        form.setFieldsValue({
          sensitive_type: nextType,
          range_bucket_width: undefined,
          range_bucket_offset: undefined,
          range_granularity: form.getFieldValue("range_granularity") ?? "year",
        });
      }
    } else {
      form.setFieldsValue({
        sensitive_type: value === MASK_ALGORITHM && !isSensitiveTypeAllowedForAlgorithm(currentType, value)
          ? sensitiveTypes[0]
          : currentType,
        range_bucket_width: undefined,
        range_bucket_offset: undefined,
        range_granularity: undefined,
      });
    }
    form.setFields([
      { name: "algo", errors: [] },
      { name: "sensitive_type", errors: [] },
      { name: "range_bucket_width", errors: [] },
      { name: "range_bucket_offset", errors: [] },
      { name: "range_granularity", errors: [] },
    ]);
  };

  const changeSensitiveType = (value: string) => {
    if (!isSensitiveType(value) || selectedAlgorithm !== RANGE_ALGORITHM) return;
    if (value === NUMBER_SENSITIVE_TYPE) {
      form.setFieldsValue({
        range_bucket_width: form.getFieldValue("range_bucket_width") ?? 10,
        range_bucket_offset: form.getFieldValue("range_bucket_offset") ?? 0,
        range_granularity: undefined,
      });
    } else if (value === DATE_SENSITIVE_TYPE) {
      form.setFieldsValue({
        range_bucket_width: undefined,
        range_bucket_offset: undefined,
        range_granularity: form.getFieldValue("range_granularity") ?? "year",
      });
    }
    form.setFields([
      { name: "sensitive_type", errors: [] },
      { name: "range_bucket_width", errors: [] },
      { name: "range_bucket_offset", errors: [] },
      { name: "range_granularity", errors: [] },
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
        { name: "algo", errors: ["算法与敏感类型组合不受支持"] },
        { name: "sensitive_type", errors: ["请按能力矩阵选择敏感类型"] },
      ]);
      return;
    }
    const input: MaskRuleInput = {
      id: values.id,
      datasource_id: values.datasource_id ?? null,
      schema_name: values.schema_name ?? "",
      table_name: values.table_name ?? "",
      column_name: values.column_name,
      sensitive_type: values.sensitive_type,
      algo: values.algo,
      enabled: values.enabled,
    };
    if (values.algo === RANGE_ALGORITHM && values.sensitive_type === NUMBER_SENSITIVE_TYPE) {
      if (
        !isIntegerInRange(values.range_bucket_width, 1, 1_000_000_000)
        || !isIntegerInRange(values.range_bucket_offset, -1_000_000_000, 1_000_000_000)
      ) {
        form.setFields([
          { name: "range_bucket_width", errors: ["桶宽必须是 1..1000000000 的整数"] },
          { name: "range_bucket_offset", errors: ["偏移必须是 -1000000000..1000000000 的整数"] },
        ]);
        return;
      }
      input.range_bucket_width = values.range_bucket_width;
      input.range_bucket_offset = values.range_bucket_offset;
      input.range_granularity = null;
    } else if (values.algo === RANGE_ALGORITHM && values.sensitive_type === DATE_SENSITIVE_TYPE) {
      if (!isRangeGranularity(values.range_granularity)) {
        form.setFields([{ name: "range_granularity", errors: ["请选择年、季或月"] }]);
        return;
      }
      input.range_bucket_width = null;
      input.range_bucket_offset = null;
      input.range_granularity = values.range_granularity;
    }
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
        ...(values.algo === RANGE_ALGORITHM && values.sensitive_type === NUMBER_SENSITIVE_TYPE
          ? [
            { name: "range_bucket_width" as const, errors: [backendMessage] },
            { name: "range_bucket_offset" as const, errors: [backendMessage] },
          ]
          : []),
        ...(values.algo === RANGE_ALGORITHM && values.sensitive_type === DATE_SENSITIVE_TYPE
          ? [{ name: "range_granularity" as const, errors: [backendMessage] }]
          : []),
      ]);
      void message.error(backendMessage);
      return;
    }
    if (failure.status === 409 && failure.errorCode === "MASK_RULE_SCOPE_CONFLICT") {
      setScopeConflict(failure);
      return;
    }
    void message.error(failure.message);
  };

  const algorithm = isMaskAlgorithm(selectedAlgorithm) ? selectedAlgorithm : MASK_ALGORITHM;
  const watchedType = isSensitiveType(selectedSensitiveType) ? selectedSensitiveType : sensitiveTypes[0];
  const type = isSensitiveTypeAllowedForAlgorithm(watchedType, algorithm)
    ? watchedType
    : algorithm === RANGE_ALGORITHM ? NUMBER_SENSITIVE_TYPE : sensitiveTypes[0];
  const typePresentation = sensitiveTypePresentationForAlgorithm(type, algorithm);
  const isHash = algorithm === HASH_ALGORITHM;
  const isBlock = algorithm === BLOCK_ALGORITHM;
  const isRange = algorithm === RANGE_ALGORITHM;
  const scopeInvalid = Boolean(selectedSchemaName?.trim()) && !selectedTableName?.trim();
  const selectedDatasource = datasources.find((datasource) => datasource.id === selectedDatasourceID);
  const datasourceType = selectedDatasource?.db_type.trim().toLowerCase() || "";
  const schemaHelp = datasourceType === "mysql"
    ? "可选，留空即当前库"
    : datasourceType === "postgres" || datasourceType === "postgresql"
      ? "可选，留空匹配任意模式，通常无需填写"
      : "可选；留空时按表名匹配，填写后精确限定模式";
  const rangeParamsValid = type === NUMBER_SENSITIVE_TYPE
    ? isIntegerInRange(selectedBucketWidth, 1, 1_000_000_000)
      && isIntegerInRange(selectedBucketOffset, -1_000_000_000, 1_000_000_000)
    : type === DATE_SENSITIVE_TYPE && isRangeGranularity(selectedGranularity);
  const example = isRange
    ? rangeExample(type, {
      range_bucket_width: selectedBucketWidth,
      range_bucket_offset: selectedBucketOffset,
      range_granularity: selectedGranularity,
    })
    : typePresentation.example;

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
            <Button
              type="primary"
              icon={<SaveOutlined />}
              loading={loading}
              disabled={scopeInvalid || (isRange && !rangeParamsValid)}
              onClick={() => form.submit()}
            >保存</Button>
          </Space>
        </div>
      )}
    >
      <Form
        form={form}
        layout="vertical"
        requiredMark="optional"
        onFinish={submit}
        onValuesChange={() => setScopeConflict(null)}
      >
        {scopeConflict ? (
          <Alert
            showIcon
            type="error"
            message={scopeConflict.message}
            description="该列已存在不同算法的全局/表级规则，请把两者算法改成一致，或删除其中一条"
          />
        ) : null}
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
        <Alert
          showIcon
          type="info"
          message="三档作用域"
          description="模式和表都留空为全局列规则；只填写表名为表.列规则；模式和表都填写为模式.表.列规则。模式不能脱离表名单独填写。"
        />
        <Form.Item
          name="schema_name"
          label="模式 Schema（可选）"
          dependencies={["table_name"]}
          extra={schemaHelp}
          rules={[
            {
              validator: (_rule, value: string | undefined) => {
                if (value && value.trim() !== value) return Promise.reject(new Error("模式 Schema 首尾不能包含空格"));
                const tableName: unknown = form.getFieldValue("table_name");
                if (value?.trim() && (typeof tableName !== "string" || !tableName.trim())) {
                  return Promise.reject(new Error("填写模式 Schema 时必须同时填写表名"));
                }
                return Promise.resolve();
              },
            },
          ]}
        >
          <Input placeholder={schemaHelp} />
        </Form.Item>
        <Form.Item
          name="table_name"
          label="表名（可选）"
          extra="留空时为全局列规则；填写后按表.列作用域精确保护"
          rules={[
            {
              validator: (_rule, value: string | undefined) => {
                if (value && value.trim() !== value) return Promise.reject(new Error("表名首尾不能包含空格"));
                return Promise.resolve();
              },
            },
          ]}
        >
          <Input placeholder="留空为全局列规则，如 customers" />
        </Form.Item>
        <Alert
          showIcon
          type="warning"
          message="表级规则启用安全兜底"
          description="在多表 JOIN、SELECT * 或无法确定列归属时，命中表级规则的同名列会被安全阻断（结果显示 ***）；请给列加表限定符（如 customers.phone）以精确匹配。"
        />
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
        {isRange ? (
          <Alert
            showIcon
            type="info"
            message="按粗粒度分桶或截断，无需密钥"
            description="range 会保留粗粒度分布，但不是匿名化；同一桶内的值仍可能被关联。"
          />
        ) : null}
        <Form.Item
          name="sensitive_type"
          label="敏感类型"
          extra={typePresentation.description}
          rules={[{ required: true, message: "请选择敏感类型" }]}
        >
          <Select options={sensitiveTypeOptionsForAlgorithm(algorithm)} onChange={changeSensitiveType} />
        </Form.Item>
        {isRange && type === NUMBER_SENSITIVE_TYPE ? (
          <>
            <Form.Item
              name="range_bucket_width"
              label="桶宽"
              rules={[{ required: true, message: "请输入桶宽" }]}
            >
              <InputNumber min={1} max={1_000_000_000} step={1} precision={0} style={{ width: "100%" }} />
            </Form.Item>
            <Form.Item
              name="range_bucket_offset"
              label="桶偏移"
              rules={[{ required: true, message: "请输入桶偏移" }]}
            >
              <InputNumber min={-1_000_000_000} max={1_000_000_000} step={1} precision={0} style={{ width: "100%" }} />
            </Form.Item>
          </>
        ) : null}
        {isRange && type === DATE_SENSITIVE_TYPE ? (
          <Form.Item
            name="range_granularity"
            label="截断粒度"
            rules={[{ required: true, message: "请选择截断粒度" }]}
          >
            <Select options={[
              { value: "year", label: "年（year）" },
              { value: "quarter", label: "季（quarter）" },
              { value: "month", label: "月（month）" },
            ]} />
          </Form.Item>
        ) : null}
        <Typography.Paragraph type="secondary">
          <Typography.Text strong>脱敏示例：</Typography.Text>{" "}
          <code>{example}</code>
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
