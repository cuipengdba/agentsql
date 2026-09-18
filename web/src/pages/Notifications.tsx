import {
  BellOutlined,
  DeleteOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
  SendOutlined,
} from "@ant-design/icons";
import {
  Alert,
  Button,
  Card,
  Empty,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import type { TableProps } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";

import {
  getNotificationConfig,
  getNotificationHealth,
  putNotificationConfig,
  testNotificationChannel,
} from "@/api/notifications";
import type {
  NotificationChannel,
  NotificationChannelKind,
  NotificationConfig,
  NotificationDecision,
  NotificationHealthChannel,
  NotificationSyslog,
  NotificationWebhook,
} from "@/api/types";
import { PageContainer } from "@/components/PageContainer";
import { apiErrorMessage, formatDateTime, httpStatus, isCanceled } from "@/pages/config/utils";

interface HeaderDraft {
  key: string;
  name: string;
  value: string;
  configured: boolean;
}

interface WebhookDraft extends Omit<NotificationWebhook, "headers"> {
  headers: HeaderDraft[];
}

interface ChannelDraft extends Omit<NotificationChannel, "webhook"> {
  webhook?: WebhookDraft;
}

interface ConfigDraft extends Omit<NotificationConfig, "channels"> {
  channels: ChannelDraft[];
}

const SECRET_MASK = "********";
const DEFAULT_QUEUE_SIZE = 64;
const MAX_QUEUE_SIZE = 65_535;

const decisionOptions: { value: NotificationDecision; label: string }[] = [
  { value: "deny", label: "拒绝（deny）" },
  { value: "error", label: "错误（error）" },
  { value: "warn", label: "警告（warn）" },
  { value: "allow", label: "放行（allow）" },
];

const acceptedDecisionValues = new Set<NotificationDecision>([
  ...decisionOptions.map((option) => option.value),
  "approve",
]);

const templateOptions = [
  { value: "generic", label: "通用（generic）" },
  { value: "feishu", label: "飞书（feishu）" },
  { value: "dingtalk", label: "钉钉（dingtalk）" },
  { value: "wecom", label: "企业微信（wecom）" },
  { value: "slack", label: "Slack" },
] as const;

const facilityNames = [
  "kernel", "user", "mail", "daemon", "auth", "syslog", "lpr", "news",
  "uucp", "clock", "authpriv", "ftp", "ntp", "audit", "alert", "clock2",
  "local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7",
];

const facilityOptions = facilityNames.map((name, value) => ({ value, label: `${value} · ${name}` }));

const categoryLabels: Record<string, string> = {
  sent: "已发送",
  connect_error: "连接失败",
  timeout: "发送超时",
  http_status: "下游返回异常状态",
  send_error: "发送失败",
};

let draftSequence = 0;

function nextDraftKey(prefix: string): string {
  draftSequence += 1;
  return `${prefix}-${Date.now()}-${draftSequence}`;
}

function cleanSecret(value: string | undefined): string {
  return value === SECRET_MASK ? "" : value || "";
}

function sanitizeConfig(config: NotificationConfig): ConfigDraft {
  return {
    enabled: Boolean(config.enabled),
    queue_size: Number.isInteger(config.queue_size) && config.queue_size > 0
      ? config.queue_size
      : DEFAULT_QUEUE_SIZE,
    channels: (config.channels || []).map((channel) => ({
      id: channel.id || "",
      enabled: Boolean(channel.enabled),
      kind: channel.kind,
      decisions: channel.decisions?.filter((item): item is NotificationDecision =>
        acceptedDecisionValues.has(item as NotificationDecision)) || [],
      include_sql: Boolean(channel.include_sql),
      allow_private_endpoints: Boolean(channel.allow_private_endpoints),
      webhook: channel.webhook ? {
        template: channel.webhook.template || "generic",
        url: "",
        url_configured: Boolean(channel.webhook.url_configured),
        bearer_token: "",
        bearer_token_configured: Boolean(channel.webhook.bearer_token_configured),
        headers: Object.keys(channel.webhook.headers || {}).map((name) => ({
          key: nextDraftKey("header"),
          name,
          value: "",
          configured: true,
        })),
        headers_configured: Boolean(channel.webhook.headers_configured),
        secret: "",
        secret_configured: Boolean(channel.webhook.secret_configured),
      } : undefined,
      syslog: channel.syslog ? { ...channel.syslog } : undefined,
    })),
  };
}

function toChannelDTO(channel: ChannelDraft): NotificationChannel {
  const base: NotificationChannel = {
    id: channel.id.trim(),
    enabled: channel.enabled,
    kind: channel.kind,
    decisions: channel.decisions,
    include_sql: channel.include_sql,
    allow_private_endpoints: channel.allow_private_endpoints,
  };
  if (channel.kind === "webhook" && channel.webhook) {
    const webhook = channel.webhook;
    base.webhook = {
      template: webhook.template,
      url: cleanSecret(webhook.url).trim(),
      url_configured: webhook.url_configured,
      bearer_token: cleanSecret(webhook.bearer_token),
      bearer_token_configured: webhook.bearer_token_configured,
      headers: Object.fromEntries(webhook.headers.map((header) => [header.name.trim(), cleanSecret(header.value)])),
      headers_configured: webhook.headers_configured,
      secret: cleanSecret(webhook.secret),
      secret_configured: webhook.secret_configured,
    };
  }
  if (channel.kind === "syslog" && channel.syslog) base.syslog = { ...channel.syslog };
  return base;
}

function toConfigDTO(config: ConfigDraft): NotificationConfig {
  return {
    enabled: config.enabled,
    queue_size: config.queue_size,
    channels: config.channels.map(toChannelDTO),
  };
}

function newWebhook(): WebhookDraft {
  return {
    template: "generic",
    url: "",
    url_configured: false,
    bearer_token: "",
    bearer_token_configured: false,
    headers: [],
    headers_configured: false,
    secret: "",
    secret_configured: false,
  };
}

function newSyslog(): NotificationSyslog {
  return { host: "", port: 514, transport: "udp", facility: 16 };
}

function newChannel(kind: NotificationChannelKind): ChannelDraft {
  draftSequence += 1;
  return {
    id: `notify_${Date.now().toString(36)}_${draftSequence}`,
    enabled: true,
    kind,
    decisions: ["deny", "error"],
    include_sql: false,
    allow_private_endpoints: false,
    webhook: kind === "webhook" ? newWebhook() : undefined,
    syslog: kind === "syslog" ? newSyslog() : undefined,
  };
}

function validateChannel(channel: ChannelDraft, allChannels: ChannelDraft[]): string | null {
  const id = channel.id.trim();
  if (!id) return "请填写通道 ID";
  if (allChannels.filter((item) => item.id.trim() === id).length > 1) return `通道 ID“${id}”重复`;
  if (channel.decisions.length === 0) return `通道“${id}”至少选择一个触发决策`;
  if (channel.kind === "webhook") {
    const webhook = channel.webhook;
    if (!webhook) return `通道“${id}”缺少 Webhook 配置`;
    const url = cleanSecret(webhook.url).trim();
    if (!url && !webhook.url_configured) return `通道“${id}”必须填写 Webhook URL`;
    if (url && !/^https?:\/\//i.test(url)) return `通道“${id}”的 Webhook URL 必须使用 HTTP 或 HTTPS`;
    const names = new Set<string>();
    for (const header of webhook.headers) {
      const name = header.name.trim();
      if (!name) return `通道“${id}”存在未填写名称的 Header`;
      if (/\r|\n/.test(name) || /\r|\n/.test(header.value)) return `通道“${id}”的 Header 不能包含换行符`;
      const normalized = name.toLowerCase();
      if (names.has(normalized)) return `通道“${id}”存在重复 Header：${name}`;
      names.add(normalized);
      if (!header.configured && !header.value) return `通道“${id}”的新 Header“${name}”必须填写值`;
    }
  } else {
    const syslog = channel.syslog;
    if (!syslog) return `通道“${id}”缺少 Syslog 配置`;
    if (!syslog.host.trim()) return `通道“${id}”必须填写 Syslog 主机`;
    if (!Number.isInteger(syslog.port) || syslog.port < 1 || syslog.port > 65_535) {
      return `通道“${id}”的 Syslog 端口必须在 1–65535 之间`;
    }
    if (!Number.isInteger(syslog.facility) || syslog.facility < 0 || syslog.facility > 23) {
      return `通道“${id}”的 Facility 必须在 0–23 之间`;
    }
  }
  return null;
}

function secretPlaceholder(configured: boolean, name: string): string {
  return configured ? `已配置，留空表示不修改${name}` : `输入${name}`;
}

interface ChannelCardProps {
  channel: ChannelDraft;
  index: number;
  channels: ChannelDraft[];
  testing: boolean;
  onChange: (next: ChannelDraft) => void;
  onDelete: () => void;
  onTest: () => void;
}

function ChannelCard({ channel, index, channels, testing, onChange, onDelete, onTest }: ChannelCardProps) {
  const patchChannel = (patch: Partial<ChannelDraft>) => onChange({ ...channel, ...patch });
  const patchWebhook = (patch: Partial<WebhookDraft>) => {
    if (channel.webhook) patchChannel({ webhook: { ...channel.webhook, ...patch } });
  };
  const patchSyslog = (patch: Partial<NotificationSyslog>) => {
    if (channel.syslog) patchChannel({ syslog: { ...channel.syslog, ...patch } });
  };
  const changeKind = (kind: NotificationChannelKind) => {
    patchChannel({
      kind,
      webhook: kind === "webhook" ? newWebhook() : undefined,
      syslog: kind === "syslog" ? newSyslog() : undefined,
    });
  };
  const updateHeader = (key: string, patch: Partial<HeaderDraft>) => {
    if (!channel.webhook) return;
    patchWebhook({
      headers: channel.webhook.headers.map((header) => header.key === key ? { ...header, ...patch } : header),
    });
  };
  const removeHeader = (key: string) => {
    if (!channel.webhook) return;
    const target = channel.webhook.headers.find((header) => header.key === key);
    if (channel.webhook.headers.length === 1 && target?.configured) {
      void message.warning("当前接口不支持单独清空全部既有 Header；可修改名称或值，留空表示保留");
      return;
    }
    patchWebhook({ headers: channel.webhook.headers.filter((header) => header.key !== key) });
  };
  const addHeader = () => {
    if (!channel.webhook) return;
    patchWebhook({
      headers: [...channel.webhook.headers, {
        key: nextDraftKey("header"), name: "", value: "", configured: false,
      }],
    });
  };

  const validationError = validateChannel(channel, channels);
  const title = (
    <Space wrap>
      <span>通道 {index + 1}</span>
      <Tag color={channel.kind === "webhook" ? "blue" : "purple"}>
        {channel.kind === "webhook" ? "Webhook" : "Syslog"}
      </Tag>
      {!channel.enabled ? <Tag>已停用</Tag> : null}
    </Space>
  );

  return (
    <Card
      className="ntf-channel-card"
      title={title}
      extra={(
        <Space wrap>
          <Button icon={<SendOutlined />} loading={testing} disabled={testing} onClick={onTest}>
            发送测试
          </Button>
          <Popconfirm
            title="删除此通知通道？"
            description="保存后删除才会生效。"
            okText="删除"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            onConfirm={onDelete}
          >
            <Button danger icon={<DeleteOutlined />}>删除</Button>
          </Popconfirm>
        </Space>
      )}
    >
      {validationError ? <Alert className="ntf-card-alert" type="warning" showIcon message={validationError} /> : null}
      <div className="ntf-form-grid ntf-form-grid-basic">
        <label className="ntf-field">
          <span>通道 ID</span>
          <Input value={channel.id} placeholder="如 security_webhook" onChange={(event) => patchChannel({ id: event.target.value })} />
        </label>
        <label className="ntf-field">
          <span>类型</span>
          <Select
            value={channel.kind}
            options={[{ value: "webhook", label: "Webhook" }, { value: "syslog", label: "Syslog" }]}
            onChange={changeKind}
          />
        </label>
        <div className="ntf-field ntf-switch-field">
          <span>通道状态</span>
          <Space><Switch checked={channel.enabled} onChange={(enabled) => patchChannel({ enabled })} /><span>{channel.enabled ? "启用" : "停用"}</span></Space>
        </div>
        <label className="ntf-field ntf-field-wide">
          <span>触发决策</span>
          <Select
            mode="multiple"
            value={channel.decisions}
            options={decisionOptions}
            placeholder="至少选择一个决策"
            onChange={(decisions: NotificationDecision[]) => patchChannel({ decisions })}
          />
        </label>
      </div>

      <div className="ntf-toggle-grid">
        <div className="ntf-toggle-option">
          <Switch checked={channel.include_sql} onChange={(include_sql) => patchChannel({ include_sql })} />
          <div>
            <strong>包含规范化 SQL</strong>
            <span>开启会把规范化 SQL 外发，请确认目标系统与数据处理要求。</span>
          </div>
        </div>
        <div className={`ntf-toggle-option ntf-private-option${channel.allow_private_endpoints ? " ntf-private-option-active" : ""}`}>
          <Switch
            checked={channel.allow_private_endpoints}
            onChange={(allow_private_endpoints) => patchChannel({ allow_private_endpoints })}
          />
          <div>
            <strong>允许私有地址</strong>
            <span>允许回环与内网地址，仅用于自测或内网 SIEM；明文 HTTP Webhook 也仅在此开关开启时可用。</span>
          </div>
        </div>
      </div>

      {channel.kind === "webhook" && channel.webhook ? (
        <section className="ntf-channel-section">
          <h3>Webhook 配置</h3>
          <div className="ntf-form-grid">
            <label className="ntf-field">
              <span>模板</span>
              <Select value={channel.webhook.template} options={[...templateOptions]} onChange={(template) => patchWebhook({ template })} />
            </label>
            <label className="ntf-field ntf-field-wide">
              <span>URL {channel.webhook.url_configured ? <Tag color="success">已配置</Tag> : null}</span>
              <Input.Password
                autoComplete="new-password"
                value={channel.webhook.url}
                placeholder={secretPlaceholder(channel.webhook.url_configured, " URL")}
                onChange={(event) => patchWebhook({ url: event.target.value })}
              />
            </label>
            <label className="ntf-field">
              <span>Bearer Token {channel.webhook.bearer_token_configured ? <Tag color="success">已配置</Tag> : null}</span>
              <Input.Password
                autoComplete="new-password"
                value={channel.webhook.bearer_token}
                placeholder={secretPlaceholder(channel.webhook.bearer_token_configured, " Token")}
                onChange={(event) => patchWebhook({ bearer_token: event.target.value })}
              />
            </label>
            <label className="ntf-field">
              <span>签名 Secret {channel.webhook.secret_configured ? <Tag color="success">已配置</Tag> : null}</span>
              <Input.Password
                autoComplete="new-password"
                value={channel.webhook.secret}
                placeholder={secretPlaceholder(channel.webhook.secret_configured, " Secret")}
                onChange={(event) => patchWebhook({ secret: event.target.value })}
              />
            </label>
          </div>
          <div className="ntf-headers-heading">
            <div>
              <strong>自定义 Headers</strong>
              <span>Header 值属于秘密；已配置的值留空表示不修改。</span>
            </div>
            <Button size="small" icon={<PlusOutlined />} onClick={addHeader}>添加 Header</Button>
          </div>
          {channel.webhook.headers.length === 0 ? (
            <div className="ntf-headers-empty">未配置自定义 Header</div>
          ) : (
            <div className="ntf-headers-list">
              {channel.webhook.headers.map((header) => (
                <div className="ntf-header-row" key={header.key}>
                  <Input
                    aria-label="Header 名称"
                    value={header.name}
                    placeholder="Header 名称"
                    onChange={(event) => updateHeader(header.key, { name: event.target.value })}
                  />
                  <Input.Password
                    aria-label="Header 值"
                    autoComplete="new-password"
                    value={header.value}
                    placeholder={header.configured ? "已配置，留空表示不修改" : "Header 值"}
                    onChange={(event) => updateHeader(header.key, { value: event.target.value })}
                  />
                  {header.configured ? <Tag color="success">已配置</Tag> : null}
                  <Tooltip title="移除此 Header；清空全部既有 Header 暂无独立操作">
                    <Button danger type="text" icon={<DeleteOutlined />} aria-label="删除 Header" onClick={() => removeHeader(header.key)} />
                  </Tooltip>
                </div>
              ))}
            </div>
          )}
        </section>
      ) : null}

      {channel.kind === "syslog" && channel.syslog ? (
        <section className="ntf-channel-section">
          <h3>Syslog 配置</h3>
          <div className="ntf-form-grid ntf-form-grid-syslog">
            <label className="ntf-field ntf-field-wide">
              <span>主机</span>
              <Input value={channel.syslog.host} placeholder="如 siem.example.com" onChange={(event) => patchSyslog({ host: event.target.value })} />
            </label>
            <label className="ntf-field">
              <span>端口</span>
              <InputNumber className="ntf-full-width" min={1} max={65_535} precision={0} value={channel.syslog.port} onChange={(port) => patchSyslog({ port: port || 0 })} />
            </label>
            <label className="ntf-field">
              <span>传输</span>
              <Select value={channel.syslog.transport} options={[{ value: "udp", label: "UDP" }, { value: "tcp", label: "TCP" }]} onChange={(transport) => patchSyslog({ transport })} />
            </label>
            <label className="ntf-field">
              <span>Facility</span>
              <Select showSearch optionFilterProp="label" value={channel.syslog.facility} options={facilityOptions} onChange={(facility) => patchSyslog({ facility })} />
            </label>
          </div>
        </section>
      ) : null}
    </Card>
  );
}

export function Notifications() {
  const [config, setConfig] = useState<ConfigDraft | null>(null);
  const [health, setHealth] = useState<NotificationHealthChannel[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadFailed, setLoadFailed] = useState(false);
  const [saving, setSaving] = useState(false);
  const [healthLoading, setHealthLoading] = useState(false);
  const [healthFailed, setHealthFailed] = useState(false);
  const [testingID, setTestingID] = useState("");
  const mountedRef = useRef(true);
  const loadControllerRef = useRef<AbortController | null>(null);
  const healthControllerRef = useRef<AbortController | null>(null);

  const loadConfig = useCallback(async () => {
    loadControllerRef.current?.abort();
    const controller = new AbortController();
    loadControllerRef.current = controller;
    setLoading(true);
    setLoadFailed(false);
    try {
      const response = await getNotificationConfig(controller.signal);
      if (!controller.signal.aborted && mountedRef.current) setConfig(sanitizeConfig(response));
    } catch (error: unknown) {
      if (!controller.signal.aborted && mountedRef.current && !isCanceled(error)) {
        setLoadFailed(true);
        void message.error(apiErrorMessage(error, "通知配置加载失败"));
      }
    } finally {
      if (loadControllerRef.current === controller) loadControllerRef.current = null;
      if (mountedRef.current && !controller.signal.aborted) setLoading(false);
    }
  }, []);

  const loadHealth = useCallback(async () => {
    healthControllerRef.current?.abort();
    const controller = new AbortController();
    healthControllerRef.current = controller;
    setHealthLoading(true);
    setHealthFailed(false);
    try {
      const response = await getNotificationHealth(controller.signal);
      if (!controller.signal.aborted && mountedRef.current) setHealth(response.channels || []);
    } catch (error: unknown) {
      if (!controller.signal.aborted && mountedRef.current && !isCanceled(error)) setHealthFailed(true);
    } finally {
      if (healthControllerRef.current === controller) healthControllerRef.current = null;
      if (mountedRef.current && !controller.signal.aborted) setHealthLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void loadConfig();
    void loadHealth();
    return () => {
      mountedRef.current = false;
      loadControllerRef.current?.abort();
      healthControllerRef.current?.abort();
    };
  }, [loadConfig, loadHealth]);

  const updateChannel = (index: number, next: ChannelDraft) => {
    setConfig((current) => current ? {
      ...current,
      channels: current.channels.map((channel, itemIndex) => itemIndex === index ? next : channel),
    } : current);
  };

  const addChannel = (kind: NotificationChannelKind) => {
    setConfig((current) => current ? { ...current, channels: [...current.channels, newChannel(kind)] } : current);
  };

  const removeChannel = (index: number) => {
    setConfig((current) => current ? {
      ...current,
      channels: current.channels.filter((_, itemIndex) => itemIndex !== index),
    } : current);
  };

  const validateConfig = (): string | null => {
    if (!config) return "通知配置尚未加载";
    if (!Number.isInteger(config.queue_size) || config.queue_size < 1 || config.queue_size > MAX_QUEUE_SIZE) {
      return `队列长度必须是 1–${MAX_QUEUE_SIZE.toLocaleString("zh-CN")} 的整数`;
    }
    for (const channel of config.channels) {
      const error = validateChannel(channel, config.channels);
      if (error) return error;
    }
    return null;
  };

  const save = async () => {
    if (!config || saving) return;
    const validationError = validateConfig();
    if (validationError) {
      void message.warning(validationError);
      return;
    }
    setSaving(true);
    try {
      const response = await putNotificationConfig(toConfigDTO(config));
      if (!mountedRef.current) return;
      setConfig(sanitizeConfig(response));
      void message.success("通知配置已保存并生效");
      void loadHealth();
    } catch (error: unknown) {
      if (!mountedRef.current || isCanceled(error)) return;
      if (httpStatus(error) === 422) {
        void message.error("配置校验未通过：请检查必填项、地址安全、通道参数；回环、内网和云元数据地址默认不允许");
      } else {
        void message.error(apiErrorMessage(error, "通知配置保存失败，请稍后重试"));
      }
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  const testChannel = async (channel: ChannelDraft) => {
    if (!config || testingID) return;
    const validationError = validateChannel(channel, config.channels);
    if (validationError) {
      void message.warning(validationError);
      return;
    }
    setTestingID(channel.id);
    try {
      const result = await testNotificationChannel(toChannelDTO(channel));
      if (!mountedRef.current) return;
      if (result.success) {
        void message.success(`通道“${result.channel_id || channel.id}”测试发送成功`);
      } else {
        void message.error(`测试发送失败：${categoryLabels[result.category] || "未知失败类别"}`);
      }
    } catch (error: unknown) {
      if (!mountedRef.current || isCanceled(error)) return;
      if (httpStatus(error) === 422) {
        void message.error("测试配置校验未通过，请检查目标地址、必填项和地址安全开关");
      } else {
        void message.error("测试请求失败，请稍后重试");
      }
    } finally {
      if (mountedRef.current) setTestingID("");
    }
  };

  const healthColumns: TableProps<NotificationHealthChannel>["columns"] = [
    { title: "通道 ID", dataIndex: "id", width: 210, render: (value: string) => <code>{value}</code> },
    { title: "已发送", dataIndex: "sent", width: 110, align: "right", render: (value: number) => Number(value || 0).toLocaleString("zh-CN") },
    { title: "失败", dataIndex: "failed", width: 110, align: "right", render: (value: number) => Number(value || 0).toLocaleString("zh-CN") },
    { title: "丢弃", dataIndex: "dropped", width: 110, align: "right", render: (value: number) => Number(value || 0).toLocaleString("zh-CN") },
    {
      title: "最近错误",
      dataIndex: "last_error",
      width: 190,
      render: (value: string | undefined) => value
        ? <Tag color="error">{categoryLabels[value] || "其他发送错误"}</Tag>
        : <span className="ntf-muted">—</span>,
    },
    {
      title: "最近成功时间",
      dataIndex: "last_success_at",
      width: 200,
      render: (value: string | undefined) => value?.startsWith("0001-") ? "—" : formatDateTime(value),
    },
  ];

  return (
    <PageContainer
      title="通知设置"
      subtitle="配置安全事件的 Webhook 与 Syslog 旁路通知"
      extra={<Button type="primary" icon={<SaveOutlined />} loading={saving} disabled={!config || loading} onClick={() => void save()}>保存</Button>}
    >
      <Alert
        className="ntf-notice"
        type="info"
        showIcon
        message="通知是 best-effort 旁路副本，审计库才是权威记录"
        description="默认只发送 deny/error 且最小化外发内容。保存前会校验目标地址，回环、内网和云元数据地址默认被拒绝。"
      />

      {loadFailed ? (
        <Alert
          className="ntf-load-alert"
          type="error"
          showIcon
          message="通知配置加载失败"
          description="当前内容不可编辑，请检查服务状态后重试。"
          action={<Button size="small" onClick={() => void loadConfig()}>重试</Button>}
        />
      ) : null}

      {loading && !config ? <div className="ntf-loading"><Spin tip="正在加载通知配置" /></div> : null}

      {config ? (
        <>
          <Card className="ntf-global-card" title={<Space><BellOutlined /><span>全局设置</span></Space>}>
            <div className="ntf-global-controls">
              <div className="ntf-toggle-option">
                <Switch checked={config.enabled} onChange={(enabled) => setConfig({ ...config, enabled })} />
                <div>
                  <strong>启用通知</strong>
                  <span>关闭后保留通道配置，但不投递新通知。</span>
                </div>
              </div>
              <label className="ntf-field ntf-queue-field">
                <span>队列长度 queue_size</span>
                <InputNumber
                  className="ntf-full-width"
                  min={1}
                  max={MAX_QUEUE_SIZE}
                  precision={0}
                  value={config.queue_size}
                  onChange={(queue_size) => setConfig({ ...config, queue_size: queue_size || 0 })}
                />
                <small>每个通道的内存队列，建议 1–65,535，默认 64；队列满时按 best-effort 语义丢弃。</small>
              </label>
            </div>
          </Card>

          <div className="ntf-section-heading">
            <div>
              <h2>通道管理</h2>
              <Typography.Text type="secondary">共 {config.channels.length} 个通道</Typography.Text>
            </div>
            <Space wrap>
              <Button icon={<PlusOutlined />} onClick={() => addChannel("webhook")}>新增 Webhook</Button>
              <Button icon={<PlusOutlined />} onClick={() => addChannel("syslog")}>新增 Syslog</Button>
            </Space>
          </div>

          {config.channels.length === 0 ? (
            <div className="ntf-empty"><Empty description="暂无通知通道，请新增 Webhook 或 Syslog" /></div>
          ) : (
            <div className="ntf-channel-list">
              {config.channels.map((channel, index) => (
                <ChannelCard
                  key={`${index}-${channel.kind}`}
                  channel={channel}
                  index={index}
                  channels={config.channels}
                  testing={testingID === channel.id}
                  onChange={(next) => updateChannel(index, next)}
                  onDelete={() => removeChannel(index)}
                  onTest={() => void testChannel(channel)}
                />
              ))}
            </div>
          )}
        </>
      ) : null}

      <Card
        className="ntf-health-card"
        title="健康状态"
        extra={<Button icon={<ReloadOutlined />} loading={healthLoading} onClick={() => void loadHealth()}>刷新</Button>}
      >
        <Typography.Paragraph type="secondary" className="ntf-health-note">
          计数为当前服务进程内累计值，重启后不作为持久化历史。
        </Typography.Paragraph>
        {healthFailed ? (
          <Alert
            className="ntf-card-alert"
            type="error"
            showIcon
            message="健康状态加载失败"
            action={<Button size="small" onClick={() => void loadHealth()}>重试</Button>}
          />
        ) : null}
        <Table
          className="ntf-health-table"
          rowKey="id"
          columns={healthColumns}
          dataSource={health}
          loading={healthLoading}
          pagination={false}
          scroll={{ x: 930 }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无运行中通道状态" /> }}
        />
      </Card>
    </PageContainer>
  );
}
