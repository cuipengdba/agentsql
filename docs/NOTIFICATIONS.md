# AgentSQL 通知外发指南

AgentSQL 可以把已经写入审计库的决策事件，以 Webhook 或 Syslog 方式旁路发送到告警群、SIEM 或自建接收端。本文面向负责部署、网络边界和密钥管理的运维与安全工程师。

> **重要：通知是 best-effort 副本，审计库才是权威记录。** 通知失败、排队丢弃、进程重启或配置热加载期间的短暂缺口，绝不阻断或改写 SQL 决策、受控执行和审计落库。不要把通知接收记录当作完整审计账本。

## 功能定位与数据流

通知只消费成功持久化后的审计事件，使用仅实时、不回放历史的订阅：

```text
SQL 请求 → 鉴权 / 解析 / 授权 / 规则 / 执行 → 审计库（权威记录）
                                                   │
                                                   └─ 决策事件
                                                        ↓
                                                  live 事件总线
                                             （只收订阅后的新事件）
                                                        ↓
                                                按通道过滤 decision
                                                        ↓
                                      每通道独立有界队列与发送 worker
                                                ↙               ↘
                                         Webhook POST       Syslog UDP/TCP
```

保存配置会热加载通知器。热加载取消旧的 live 订阅后建立新订阅，不会重放事件总线中的历史；切换瞬间允许极少量事件丢失。全局关闭通知或没有启用的通道时，通知器不会订阅事件总线。

## 默认安全姿态

一套未配置的控制面数据库采用以下默认值：

- 全局通知关闭，且没有通道。
- 新建通道默认只匹配 `deny`、`error`。
- `include_sql=false`，默认不外发任何 SQL 文本。
- `allow_private_endpoints=false`，默认拒绝回环、内网、链路本地、云元数据和保留地址。
- 每个通道的运行时队列默认长度为 64。
- 错误信息只映射为固定的低基数 `error_code`，不外发数据库或驱动的原始错误串。

## 通道类型

### Webhook

Webhook 使用 HTTP `POST` 和 `Content-Type: application/json`，支持五种模板。

| 模板 | 请求体 | 凭据与获取位置 |
| --- | --- | --- |
| `generic` | AgentSQL 白名单 JSON 事件 | 在自建接收端或 SIEM 中创建 HTTP 接收地址；按接收端要求取得 Bearer Token 或自定义 Header 值。可另设 AgentSQL HMAC Secret。 |
| `feishu` | `msg_type=text` | 在目标飞书群的群设置中添加“自定义机器人”，复制机器人详情中的 Webhook URL；URL 的 hook 标识本身是凭据。AgentSQL 的“签名 Secret”对该模板不生效。 |
| `dingtalk` | `msgtype=markdown` | 在目标钉钉群的机器人/智能群助手设置中添加自定义机器人，复制 Webhook URL；URL 中含 `access_token`。若安全设置选择“加签”，同时复制 `SEC...` Secret 到“签名 Secret”。 |
| `wecom` | `msgtype=markdown` | 在企业微信群的聊天信息/群机器人（部分版本显示“消息推送 → 自定义消息推送”）中添加机器人，在机器人详情复制 Webhook URL；`key` 查询参数就是凭据，没有单独的 AgentSQL 签名 Secret。 |
| `slack` | `{"text":"..."}` | 在 Slack App 管理页创建或打开应用，进入 **Incoming Webhooks**，启用后选择 **Add New Webhook to Workspace**，授权目标频道，并从 **Webhook URLs for Your Workspace** 复制 URL。URL 路径中的值就是秘密。 |

平台界面名称可能随版本或管理员策略变化。以平台当前文档为准：[飞书自定义机器人](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)、[钉钉自定义机器人](https://open.dingtalk.com/document/orgapp/custom-robot-access)、[企业微信群机器人](https://developer.work.weixin.qq.com/document/path/91770)、[Slack Incoming Webhooks](https://api.slack.com/messaging/webhooks)。

Webhook 的公共请求头为：

```text
Content-Type: application/json
User-Agent: AgentSQL-notify/1
Authorization: Bearer <bearer_token>   # 仅配置后发送
<自定义 Header>                         # 仅配置后发送
```

以下 Header 名称保留，不能在自定义 Headers 中设置：`Host`、`Content-Length`、`Connection`、`Transfer-Encoding`、`X-AgentSQL-Timestamp`、`X-AgentSQL-Signature`。Header 名称和值也不能包含换行符。

#### generic 事件结构

`generic` 模板直接发送白名单字段。例如：

```json
{
  "audit_id": 1842,
  "timestamp": "2026-09-19T08:30:01.123456Z",
  "decision": "deny",
  "risk_level": 3,
  "rule_ids": ["R002"],
  "datasource": {
    "id": "ds-prod",
    "name": "生产只读库"
  },
  "agent": {
    "id": "agent-report",
    "name": "报表 Agent"
  },
  "mcp_tool": "execute_write",
  "stmt_type": "UPDATE",
  "est_rows": 120000,
  "latency_ms": 18,
  "error_code": "row_limit"
}
```

`risk_level`、`mcp_tool`、`stmt_type`、`est_rows`、`rows_returned`、`latency_ms`、`error_code` 和 `sql_norm` 没有值时省略。`rule_ids` 始终是数组；`datasource` 和 `agent` 始终是对象，其 `id`、`name` 没有值时可为空对象。`sql_norm` 只有在通道明确开启 `include_sql` 且审计记录有规范化 SQL 时才出现。

其他四种模板把同一份白名单事件渲染成平台文本或 Markdown；正文包含决策、风险、规则 ID、数据源、Agent、审计 ID、时间和相对详情路径 `/audit/<audit_id>`，并按需附加 `error_code` 与 `sql_norm`。

### Syslog

Syslog 支持 UDP 和 TCP：

- `host`：接收端主机名或 IP，同样受 SSRF/地址策略约束。
- `port`：`1`–`65535`；控制台新建通道默认 `514`。
- `transport`：`udp` 或 `tcp`。
- `facility`：`0`–`23`；控制台新建通道默认 `16`（`local0`）。

当前实现输出 **RFC5424 风格**消息，不是 RFC3164。格式为：

```text
<PRI>1 <RFC3339Nano UTC 时间> - AgentSQL - audit [agentsql@32473 product="AgentSQL" audit_id="<ID>"] <白名单 JSON>
```

优先级计算为 `PRI = facility × 8 + severity`。severity 映射如下：

| decision | severity | 名称 |
| --- | ---: | --- |
| `deny`、`error` | 3 | err |
| `warn` | 4 | warning |
| `approve` | 5 | notice |
| `allow` 或其他值 | 6 | informational |

TCP 每条消息末尾追加换行，UDP 不追加。UDP 的“发送成功”只表示本机写入数据报成功，不代表接收端已经处理；当前没有 TLS Syslog、消息确认或回执编排。

## 使用控制台配置

以管理员登录后，进入「设置与集成 → 通知设置」。页面中的“保存”会校验并持久化整套配置，然后热加载；即使通道处于停用状态，其目标地址和字段仍必须通过校验。

### 全局设置

- **启用通知**：全局开关。关闭会保留通道配置，但不投递新通知。
- **队列长度 `queue_size`**：每个启用通道各自的内存队列长度。控制台允许 `1`–`65535`，默认显示 64；队列满时直接丢弃新副本并增加 `dropped`。

后端 API 还接受 `queue_size=0`，运行时会把它归一化为 64；负数无效。运维配置建议显式保存正整数。

### 通道公共字段

- **通道 ID**：非空且在整套配置中唯一；健康状态和 Prometheus 的 `channel` 标签都使用它。稳定使用低基数 ID，避免在 ID 中放 URL、租户秘密或动态值。
- **类型**：`webhook` 或 `syslog`；每个通道只能有对应的一组配置。
- **通道状态**：停用后不建立发送 worker，但配置仍保存。
- **触发决策**：后端支持 `allow`、`warn`、`approve`、`deny`、`error`，空数组按后端默认归一化为 `deny,error`。控制台要求至少选择一个，并且当前下拉框只能新选 `deny`、`error`、`warn`、`allow`；从 API 读到的 `approve` 可以保留，但控制台不能新建该选项。
- **包含规范化 SQL**：对应 `include_sql`。开启前确认接收端授权、保留周期和数据跨境要求；它只允许 `sql_norm`，不会发送 `sql_raw`。
- **允许私有地址**：对应 `allow_private_endpoints`，同时影响 Webhook 和 Syslog。默认关闭，只应在本机自测或内网 SIEM 场景临时、明确开启。

### Webhook 字段

- **模板**：`generic`、`feishu`、`dingtalk`、`wecom` 或 `slack`。
- **URL**：必填，值按秘密处理。只允许 `http`/`https`，不能包含 URL 用户信息或 fragment，端口必须是 `1`–`65535`。
- **Bearer Token**：可选；配置后发送 `Authorization: Bearer <token>`。通常只供 generic 接收端使用。
- **签名 Secret**：可选；仅 `generic` 和 `dingtalk` 模板实际使用。飞书、企业微信和 Slack 模板不会读取此值。
- **自定义 Headers**：可选，所有值按秘密处理。已保存值留空表示保留；详见“秘密存储与更新契约”。

### Syslog 字段

- **主机**、**端口**、**传输**、**Facility** 按前文 Syslog 约束填写。
- 主机在保存时即解析并做地址类别校验，发送拨号时再次校验。

### 发送测试与健康状态

每张通道卡片的“发送测试”会发送一条合成的 `deny` 事件。测试强制把该通道视为启用、只匹配 `deny`，单次发送超时为 750 ms、不重试，整个测试请求最多等待 2 秒；它不修改已存配置，也不写审计库。

测试结果只有低基数类别，例如 `sent`、`connect_error`、`timeout`、`http_status`、`send_error`。测试成功后仍要触发一条真实决策并检查审计库、接收端和健康计数。

页面底部“健康状态”显示进程内状态。手动点击“刷新”读取最新值，不是持久化历史。

## 管理 API

四个操作都位于 `/api/v1` 下，并走现有管理员 Bearer 鉴权：

```http
Authorization: Bearer <管理员登录取得的 token>
Content-Type: application/json
```

`Bearer` 与 token 之间必须恰好使用一个空格，token 之前、内部或末尾不能有空白。未认证返回 HTTP 401。成功响应统一使用：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {}
}
```

失败响应使用 HTTP 状态码作为 `code`，`data` 为 `null`。常见状态为 400（请求体无效）、401（未认证）、422（通知配置无效）、503（通知管理器不可用）和 500（存储或热加载失败）。

### GET `/api/v1/integrations/notifications`

读取完整配置。秘密值永不明文返回：已配置的 URL、Bearer Token、Secret 和每个 Header 值显示为固定掩码 `********`，并返回相应的 `*_configured` 布尔值。

```bash
curl -sS \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://127.0.0.1:7780/api/v1/integrations/notifications
```

示例响应：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "enabled": true,
    "queue_size": 64,
    "channels": [
      {
        "id": "security_webhook",
        "enabled": true,
        "kind": "webhook",
        "decisions": ["deny", "error"],
        "include_sql": false,
        "allow_private_endpoints": false,
        "webhook": {
          "template": "generic",
          "url": "********",
          "url_configured": true,
          "bearer_token": "",
          "bearer_token_configured": false,
          "headers": {
            "X-Tenant": "********"
          },
          "headers_configured": true,
          "secret": "********",
          "secret_configured": true
        }
      }
    ]
  }
}
```

### PUT `/api/v1/integrations/notifications`

原子替换整套配置并热加载。保存前会验证所有通道；持久化成功但热加载失败时会尝试恢复旧配置。响应仍经过脱敏。

```bash
curl -sS -X PUT \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @- \
  http://127.0.0.1:7780/api/v1/integrations/notifications <<'JSON'
{
  "enabled": true,
  "queue_size": 64,
  "channels": [
    {
      "id": "security_webhook",
      "enabled": true,
      "kind": "webhook",
      "decisions": ["deny", "error"],
      "include_sql": false,
      "allow_private_endpoints": false,
      "webhook": {
        "template": "generic",
        "url": "https://siem.example.com/hooks/agentsql",
        "url_configured": false,
        "bearer_token": "replace-with-receiver-token",
        "bearer_token_configured": false,
        "headers": {
          "X-Tenant": "security"
        },
        "headers_configured": false,
        "secret": "replace-with-signing-secret",
        "secret_configured": false
      }
    },
    {
      "id": "siem_syslog",
      "enabled": true,
      "kind": "syslog",
      "decisions": ["deny", "error"],
      "include_sql": false,
      "allow_private_endpoints": false,
      "syslog": {
        "host": "siem.example.com",
        "port": 514,
        "transport": "tcp",
        "facility": 16
      }
    }
  ]
}
JSON
```

请求中的 `url_configured`、`bearer_token_configured`、`headers_configured`、`secret_configured` 是视图状态，不决定秘密如何合并；真正的更新契约是“同一通道 ID 下，值留空或传 `********` 表示不修改”。

### POST `/api/v1/integrations/notifications/test`

请求体包含一个 `channel`，字段与配置中的单个通道相同。若通道 ID 与已存通道相同，可以把已配置秘密留空来复用。

```bash
curl -sS -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @- \
  http://127.0.0.1:7780/api/v1/integrations/notifications/test <<'JSON'
{
  "channel": {
    "id": "security_webhook",
    "enabled": true,
    "kind": "webhook",
    "decisions": ["deny"],
    "include_sql": false,
    "allow_private_endpoints": false,
    "webhook": {
      "template": "generic",
      "url": "",
      "url_configured": true,
      "bearer_token": "",
      "bearer_token_configured": true,
      "headers": {
        "X-Tenant": ""
      },
      "headers_configured": true,
      "secret": "",
      "secret_configured": true
    }
  }
}
JSON
```

成功投递：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "channel_id": "security_webhook",
    "success": true,
    "category": "sent"
  }
}
```

合法配置但投递失败仍返回 HTTP 200，`success=false`，`category` 为失败类别；配置本身无效则返回 HTTP 422。

### GET `/api/v1/integrations/notifications/health`

```bash
curl -sS \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://127.0.0.1:7780/api/v1/integrations/notifications/health
```

示例响应：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "channels": [
      {
        "id": "security_webhook",
        "sent": 12,
        "failed": 1,
        "dropped": 3,
        "last_error": "timeout",
        "last_success_at": "2026-09-19T08:30:01.123456Z"
      }
    ]
  }
}
```

通道按 ID 排序。只有曾在当前进程中建立运行状态的通道才会出现；删除或停用后的旧 ID 可能保留到进程重启。尚无成功发送时，`last_success_at` 可能是 Go 零时间 `0001-01-01T00:00:00Z`，控制台将其显示为“—”。

## 安全边界

### SSRF 默认 fail-closed

Webhook 与 Syslog 共用出站地址策略。`allow_private_endpoints=false` 时，保存/测试阶段对主机名的全部 A/AAAA 结果进行校验，拨号阶段重新解析并直接连接已校验的 IP；连接建立后还校验实际远端 IP，以降低 DNS 重绑定风险。

默认拒绝范围包括但不限于：

- 回环：`127.0.0.0/8`、`::1/128`。
- RFC1918 私网：`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`。
- IPv6 ULA 与链路本地：`fc00::/7`、`fe80::/10`。
- IPv4 链路本地 `169.254.0.0/16`，包括云元数据 `169.254.169.254`。
- 共享地址、文档地址、基准测试地址和其他保留网段，例如 `100.64.0.0/10`、`192.0.2.0/24`、`198.18.0.0/15`、`198.51.100.0/24`、`203.0.113.0/24`、`2001:db8::/32`。
- 未指定地址、`0.0.0.0/8`、IPv4/IPv6 多播和保留高地址段。

> **安全警告：** `allow_private_endpoints=true` 会放宽私网、回环、链路本地和多数保留地址检查，因而也可能放行云元数据网段。只在隔离的本机自测或确有需要的内网 SIEM 通道开启，并用主机防火墙、出口 ACL 和接收端认证继续收敛目标。`0.0.0.0/8`、多播、IPv4 `240.0.0.0/4`、IPv6 未指定地址和 IPv6 多播即使开启该开关仍被拒绝。

Webhook 只允许 HTTP 或 HTTPS。**明文 HTTP 仅在 `allow_private_endpoints=true` 时可用**；这只是为了本地测试和受控内网兼容，不表示 HTTP 具备传输安全。生产外发应使用有效证书的 HTTPS。

### 秘密存储与更新契约

下列值都按秘密处理：Webhook URL（常内含 token/key）、Bearer Token、签名 Secret、全部自定义 Header 值。

- 控制面使用 `PasswordCipher` 和 `AGENTSQL_SECRET` 提供的恰好 32 字节密钥，以 AES-256-GCM 加密存储。
- 每次加密使用新的随机 nonce，通知值的 AAD 固定为 `agentsql:notification-secret:v1`。
- GET 和 PUT 的响应永不回显明文，只返回 `********`、对应 `*_configured` 布尔值及控制台“已配置”状态。
- 对同一通道 ID，PUT 或测试请求中的 URL、Bearer Token、Secret 留空或传 `********` 表示保留原值。
- 对已存在的同名 Header，值留空或传 `********` 表示保留；非空值表示替换。Header 名称比较不区分大小写。
- 如果提交空 `headers`，而旧配置有 Header，服务端会保留整组旧 Header。因此当前接口不能通过一次“清空 map”删除全部既有 Header；控制台也禁止删除最后一个已配置 Header。需要彻底清空时，先删除该通道并保存，再重新创建并再次保存。
- 如果提交非空 `headers`，旧配置中未提交的 Header 会被删除；新 Header 必须提供非空值。

不要把真实 URL、token、Secret 或 Header 值写入工单、日志、命令历史、Git 仓库或截图。示例中的值均为占位符。

### 隐私最小化

外发载荷由显式白名单构造，不会直接序列化完整审计记录：

- 默认不发送 `sql_raw`、`sql_norm`、绑定值、对象列表、结果集、客户端 IP、会话 ID、会话内容或模型名。
- `include_sql=true` 只允许规范化 SQL `sql_norm`；原始 SQL `sql_raw` 仍不发送。
- 原始 `ErrorMsg` 永不外发，也不做截断外发；它只映射为固定 `error_code`。
- `RuleHits` 不原样发送，只去重投影规则 ID 到 `rule_ids`；规则 message、suggestion 等内容不会外发。
- Agent 和数据源只包含稳定 ID 与可选显示名。名称缓存刷新失败不会阻断通知，届时名称可能为空。

`error_code` 的真实枚举与含义如下：

| `error_code` | 含义 |
| --- | --- |
| `rate_limited` | 命中速率/QPS/429 类限制 |
| `row_limit` | 结果行数、结果大小或行限制超出 |
| `timeout` | 超时、截止时间耗尽 |
| `permission_denied` | 权限、认证、未授权或禁止访问 |
| `syntax` | SQL 语法或解析错误 |
| `connect_error` | DNS、拨号、拒绝连接、网络不可达或连接重置 |
| `unknown` | 没有原始错误、未匹配上述固定模式或未知错误 |

映射使用固定顺序的字符串类别匹配，目的是降低敏感信息泄漏和指标基数，不应用于替代审计详情中的根因分析。

### generic HMAC 验签

`generic` 模板配置 Secret 后，AgentSQL 添加两个 Header：

```text
X-AgentSQL-Timestamp: <Unix 秒>
X-AgentSQL-Signature: sha256=<64 个小写十六进制字符>
```

签名算法为：

```text
base = ASCII(timestamp) + "." + raw_body
signature = "sha256=" + hex(HMAC-SHA256(key=secret, message=base))
```

接收端必须使用收到的**原始请求体字节**验签，不能先解析 JSON 再序列化；使用常量时间比较，并拒绝与本机时间相差超过 ±300 秒的请求。时间窗校验由接收端执行，AgentSQL 不保存 nonce，也不提供回放去重存储。

以下 Python 仅使用标准库，可直接作为临时验签接收端：

```python
#!/usr/bin/env python3
import hashlib
import hmac
import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SECRET = os.environ["AGENTSQL_WEBHOOK_SECRET"].encode()


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        timestamp = self.headers.get("X-AgentSQL-Timestamp", "")
        supplied = self.headers.get("X-AgentSQL-Signature", "")
        try:
            fresh = abs(int(time.time()) - int(timestamp)) <= 300
        except ValueError:
            fresh = False
        expected = "sha256=" + hmac.new(
            SECRET, timestamp.encode("ascii", "strict") + b"." + body, hashlib.sha256
        ).hexdigest()
        valid = fresh and hmac.compare_digest(supplied, expected)
        if valid:
            print("[OK] signature verified")
            print(json.dumps(json.loads(body), ensure_ascii=False, indent=2))
            self.send_response(204)
        else:
            print("[REJECT] invalid signature or stale timestamp")
            self.send_response(401)
        self.end_headers()

    def log_message(self, format, *args):
        return


ThreadingHTTPServer(("127.0.0.1", 18080), Handler).serve_forever()
```

### 平台模板的来源校验

- **钉钉**：配置加签 Secret 后，AgentSQL 使用 Secret 作为 HMAC-SHA256 key，对 `timestamp_ms + "\n" + secret` 签名，Base64 后把 `timestamp`、`sign` URL 编码为查询参数。此算法由钉钉接收端校验。Webhook URL 中的 `access_token` 也必须保密。
- **飞书**：当前 `feishu` 模板只依赖 Webhook URL 中的 hook 凭据，不生成飞书的 `timestamp/sign` 字段；“签名 Secret”字段对它无效。可在飞书机器人安全设置中使用 IP 白名单，或使用包含 `AgentSQL notification` 的自定义关键词做额外限制。不要为该机器人启用“签名校验”，否则当前 AgentSQL 载荷无法通过。
- **企业微信**：当前 `wecom` 模板依赖 Webhook URL 的 `key`；平台通过该不可猜测凭据接受请求，AgentSQL 不另加企业微信签名。只允许机器人创建者/受控管理员查看 URL，泄露后立即删除或重建机器人。
- **Slack**：Incoming Webhook URL 与指定工作区、应用和频道绑定，URL 本身是秘密凭据；AgentSQL 不另加 Slack 签名。通过工作区应用管理限制安装和目标频道，URL 泄露后在 Slack App 管理页撤销并重新创建。

上述飞书/企微/Slack 说明的是“平台验证 AgentSQL 向平台发消息的凭据”，不是 Slack/飞书向 AgentSQL 发回调时使用的请求签名方案。通知功能是单向外发，不接收平台回调。

## 投递语义与可靠性

- 每个启用通道拥有独立 worker 和有界内存队列；慢通道不会同步阻塞 SQL 主链路或其他通道。
- 队列满时非阻塞丢弃新通知，增加该通道 `dropped`；不落盘、不补发。
- 通知只订阅启动/热加载之后的新事件，不回放历史。进程停止、事件总线拥塞摘除订阅者或热加载切换期间均可能丢失通知副本。
- 每次发送尝试超时 3 秒。该超时也用于 Syslog 的解析、拨号和写入。
- 首次失败后最多重试 2 次，即最多 3 次发送尝试；当前对所有发送错误都执行相同有限重试。
- 两次重试的基础退避分别为 200 ms、800 ms，并施加 `0.75`–`1.25` 倍抖动，约为 150–250 ms、600–1000 ms；上限为 1 秒。
- Webhook 仅把 HTTP `2xx` 视为成功；最多读取并丢弃 4096 字节响应体，不把下游响应写入健康状态。
- Webhook 不跟随重定向，且显式禁用环境代理。需要企业出口代理时，应在网络层为明确目标配置受控转发，而不是依赖 `HTTP_PROXY`/`HTTPS_PROXY`。
- TCP Syslog 每次通知新建连接、写入后关闭；UDP/TCP 都没有应用层确认、幂等键或持久重试队列。

因此该功能不保证顺序、唯一或必达。接收端如需去重，可使用 `audit_id`；完整性核对必须回到审计库。

## 健康状态与可观测性

健康端点字段均为当前 AgentSQL 进程内累计值，进程重启后清零：

| 字段 | 含义 |
| --- | --- |
| `sent` | 完成发送的通知数；重试后成功只增加一次 |
| `failed` | 用尽全部尝试后仍失败的通知数；单次尝试失败不单独累计 |
| `dropped` | 通道自身队列已满而丢弃的通知数 |
| `last_error` | 最近一次最终失败的固定类别；后续成功会清空 |
| `last_success_at` | 最近一次成功发送时间 |

`last_error`/指标 `reason` 的可能值为 `panic`、`unknown`、`timeout`、`ssrf_blocked`、`connect_error`、`http_status`、`canceled`、`send_error`。它与外发载荷中的业务 `error_code` 是两组不同枚举：前者描述通知投递故障，后者描述被通知的 SQL/数据库事件。

Prometheus 暴露以下通知指标：

| 指标 | 类型 | 标签 | 含义 |
| --- | --- | --- | --- |
| `agentsql_notification_sent_total` | Counter | `channel` | 成功发送总数 |
| `agentsql_notification_failed_total` | Counter | `channel`, `reason` | 用尽尝试后的失败总数 |
| `agentsql_notification_dropped_total` | Counter | `channel` | 通道队列满丢弃总数 |
| `agentsql_notification_last_error_info` | Gauge | `channel`, `reason` | 当前最近错误类别为 1；成功后删除该类别时间序列 |
| `agentsql_notification_last_success_timestamp_seconds` | Gauge | `channel` | 最近成功发送的 Unix 秒时间戳 |

建议至少告警：失败计数持续增加、丢弃计数增加、最近成功时间长期不更新。`dropped` 只统计通知器通道队列满，不代表所有上游 live 事件总线丢失。

## 持久化、迁移与密钥轮换

通知设置位于控制面元数据存储的 `notification_settings`、`notification_channels` 表，由 `0002_notifications.sql` 创建：

- 默认/combined 部署：与其他元数据和审计一起存放在 SQLite，或一起存放在 PostgreSQL。
- 分离式 PostgreSQL：通知配置只进入 metadata 库，不进入独立 audit 库。
- SQLite → PostgreSQL 迁移会复制这两张通知表，包括已经加密的 Webhook 字段。

加密密文依赖生成它时的 `AGENTSQL_SECRET`。迁移或切换控制面数据库时，目标实例必须使用同一个恰好 32 字节的 SECRET；否则通知秘密无法认证解密，服务启动加载通知配置会失败。先备份控制面数据库和环境文件，再迁移，并在切流前用 GET、发送测试和真实 `deny` 事件验证。

更换 `AGENTSQL_SECRET` 不能只改环境变量：现有数据源密码和通知秘密都使用该密钥。当前没有在线重加密通知配置的独立命令；需要按整体控制面密钥迁移方案处理并安排回滚。

## 端到端最小示例：本地 generic Webhook

本例把接收端绑定到 `127.0.0.1:18080`。这是回环目标，必须临时开启 `allow_private_endpoints`；只用于单机自测，完成后立即关闭该开关并删除测试通道。示例要求 AgentSQL 与接收端处于同一网络命名空间；如果 AgentSQL 在容器内，容器的 `127.0.0.1` 不是宿主机，应把接收端放进同一容器/网络命名空间，或改用受控的宿主机可达私网地址并同步修改 URL。

### 1. 启动临时验签接收端

把“generic HMAC 验签”中的 Python 保存为临时文件 `receiver.py`，在 AgentSQL 所在主机运行：

```bash
export AGENTSQL_WEBHOOK_SECRET='local-test-signing-secret'
python3 receiver.py
```

Windows PowerShell：

```powershell
$env:AGENTSQL_WEBHOOK_SECRET = 'local-test-signing-secret'
python .\receiver.py
```

### 2. 在控制台保存通道

进入「设置与集成 → 通知设置」：

1. 打开“启用通知”，保持 `queue_size=64`。
2. 新增 Webhook；通道 ID 填 `local_verify`，保持启用。
3. 触发决策选择“拒绝（deny）”；“包含规范化 SQL”保持关闭。
4. 打开“允许私有地址”。确认仅用于本次回环自测。
5. 模板选“通用（generic）”，URL 填 `http://127.0.0.1:18080/notify`。
6. Bearer Token 和自定义 Headers 留空；签名 Secret 填 `local-test-signing-secret`。
7. 点击“保存”。再次进入页面时，URL 和 Secret 只应显示“已配置”，不能看到明文。

### 3. 先发送合成测试

点击通道卡片上的“发送测试”。接收端应输出：

```text
[OK] signature verified
```

控制台显示测试发送成功。该步骤只验证合成 `deny`，不写审计库。

### 4. 触发真实 deny

使用已经配置好的 Agent 与数据源，从 MCP 客户端发起一条确定会被当前策略拒绝的请求。例如，用 `readonly` Agent 调用 `execute_write`，或查询一个没有授权策略的表。确认调用结果为 `deny`，并在控制台“审计”页找到对应 `audit_id`。

接收端随后应再次输出 `[OK] signature verified`，JSON 中的 `decision` 为 `deny`，且 `audit_id` 与审计页一致；默认不应出现 `sql_norm` 或任何原始错误串。返回「通知设置」刷新健康状态，`sent` 应增加。

如果处于启用了 Live Demo 的本地演示环境，也可以在“演示台 → 真实试运行（Live）”选择一个预期 `deny` 的固定场景来产生真实审计事件；“静态评估”只做静态展示，不写审计，不能用于本步骤。

### 5. 清理

验证完成后停掉接收端，删除 `local_verify` 通道并保存，或至少关闭“允许私有地址”和全局通知。不要把本例的 HTTP/回环配置复制到生产环境。

## 非目标与能力边界

- 不替代审计库、数据库原生日志或 SIEM 的完整采集链路。
- 不提供邮件、短信、PagerDuty、双向机器人、平台回调、人工回执或工作流编排。
- 不保证必达、顺序或恰好一次；没有磁盘队列、死信队列或历史补发。
- 不发送结果集，不做完整 DLP，也不因通知失败改变 SQL 的 `allow`、`warn`、`approve`、`deny`、`error` 决策。
- 不提供 TLS Syslog/mTLS；敏感或跨网络 Syslog 应通过受控的安全隧道/本地 relay 转发，或优先使用 HTTPS generic Webhook。
