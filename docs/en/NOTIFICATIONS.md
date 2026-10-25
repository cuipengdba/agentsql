# AgentSQL Notification Outgoing Guide

**Language:** [English](NOTIFICATIONS.md) | [Simplified Chinese](../NOTIFICATIONS.md)

> Current guidance: Applies to v4.1.0. Historical introduction versions and acceptance records retain their original facts; see the [document index](README.md) for the current entry point.

AgentSQL can bypass the decision-making events written into the audit database and send them to the alarm group, SIEM or self-built receiver in the form of Webhook or Syslog. This article is intended for operations and security engineers responsible for deployment, network perimeter, and key management.

> **Important: The audit database is the authoritative record.** Ordinary webhooks and Syslog are best-effort copies; v3.6.0 can enable a durable cursor and at-least-once delivery for a webhook. A notification target failure does not change a committed SQL decision or audit. Do not treat notification receipts as a complete audit ledger.

## Function positioning and data flow

Ordinary channels consume audit events after successful persistence through a live subscription that does not replay history:

```text
SQL request → Authentication / Parsing / Authorization / Rules / Execution → Audit library (authoritative record)
                                                   │
                                                   └─ Decision-making events
                                                        ↓
                                                  live event bus
                                             (Only new events after subscription are received)
                                                        ↓
                                                Filter decisions by channel
                                                        ↓
                                      Independent bounded queue and send worker per channel
                                                ↙               ↘
                                         Webhook POST       Syslog UDP/TCP
```

Saving the configuration hot-reloads the ordinary notifier. The old live subscription is replaced without replaying event-bus history, so a small gap is possible. The notifier does not subscribe to the event bus when notifications are globally disabled or no ordinary channel is enabled.

Reliable webhooks read the committed tenant audit stream in order. Durable cursors, leases, and retry state in the metadata database coordinate gateway workers; this path does not depend on the in-process queue above. Audits predating channel creation are not automatically replayed.

## Default security posture

An unconfigured control plane database uses the following defaults:

- Global notifications are turned off and there are no channels.
- By default, new channels only match `deny` and `error`.
- `include_sql=false`, no SQL text is sent out by default.
- `allow_private_endpoints=false`, the default is to deny loopback, intranet, link local, cloud metadata and reserved addresses.
- The default runtime queue length per channel is 64.
- The error information is only mapped to the fixed low base `error_code`, and the original error string of the database or driver is not sent out.

## Channel type

### Webhook

Webhooks use HTTP `POST` and `Content-Type: application/json` and support five templates.

| Template | Request body | Credentials and getting location |
| --- | --- | --- |
| `generic` | AgentSQL Whitelist JSON Event | Create an HTTP receiving address in a self-built receiver or SIEM; obtain the Bearer Token or custom Header value as required by the receiver. An additional AgentSQL HMAC Secret can be set. |
| `feishu` | `msg_type=text` | Add a "custom robot" in the group settings of the target Feishu group and copy the Webhook URL in the robot details; the hook identifier of the URL itself is the credential. AgentSQL's "Signature Secret" does not take effect on this template. |
| `dingtalk` | `msgtype=markdown` | Add a custom robot in the robot/smart group assistant settings of the target DingTalk group and copy the Webhook URL; the URL contains `access_token`. If the security setting selects "Signature", also copy `SEC...` Secret to "Signature Secret". |
| `wecom` | `msgtype=markdown` | Add the robot to the chat information/group robot of the enterprise WeChat group (some versions display "Message Push → Customized Message Push"), and copy the Webhook URL in the robot details; the `key` query parameter is the credential, and there is no separate AgentSQL signature Secret. |
| `slack` | `{"text":"..."}` | Create or open the app on the Slack App management page, enter **Incoming Webhooks**, select **Add New Webhook to Workspace** after enabling it, authorize the target channel, and copy the URL from **Webhook URLs for Your Workspace**. The value in the URL path is the secret. |

Platform interface names may change depending on version or administrator policy. Based on the current platform documents: [ Feishu custom robot ](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot), [ DingTalk custom robot ](https://open.dingtalk.com/document/orgapp/custom-robot-access), [ enterprise WeChat group robot ](https://developer.work.weixin.qq.com/document/path/91770), [Slack Incoming Webhooks](https://api.slack.com/messaging/webhooks).

The public request headers of Webhook are:

```text
Content-Type: application/json
User-Agent: AgentSQL-notify/1
Authorization: Bearer <bearer_token> # Only sent after configuration
<自定义 Header> # Only sent after configuration
```

The following Header names are reserved and cannot be set in custom Headers: `Host`, `Content-Length`, `Connection`, `Transfer-Encoding`, `X-AgentSQL-Timestamp`, `X-AgentSQL-Signature`. Header names and values ​​also cannot contain newlines.

#### generic event structure

The `generic` template directly sends the whitelist field. For example:

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

Omitted when `risk_level`, `mcp_tool`, `stmt_type`, `est_rows`, `rows_returned`, `latency_ms`, `error_code` and `sql_norm` have no value. `rule_ids` is always an array; `datasource` and `agent` are always objects, and their `id` and `name` can be empty objects when they have no value. `sql_norm` only appears if the channel is explicitly turned on `include_sql` and the audit record has normalized SQL.

The other four templates render the same whitelist event into platform text or Markdown; the text contains decision, risk, rule ID, data source, agent, audit ID, time and relative detail path `/audit/<audit_id>`, with `error_code` and `sql_norm` appended as needed.

### Syslog

Syslog supports UDP and TCP:

- `host`: Receiver hostname or IP, also subject to SSRF/address policy.
- `port`: `1`–`65535`; the default for new channels in the console is `514`.
- `transport`: `udp` or `tcp`.
- `facility`: `0`–`23`; the default for new channels in the console is `16` (`local0`).

The current implementation outputs **RFC5424 style** messages, not RFC3164. The format is:

```text
<PRI>1 <RFC3339Nano UTC 时间> - AgentSQL - audit [agentsql@32473 product="AgentSQL" audit_id="<ID>"] <白名单 JSON>
```

The priority is calculated as `PRI = facility × 8 + severity`. severity is mapped as follows:

| decision | severity | Name |
| --- | ---: | --- |
| `deny`、`error` | 3 | err |
| `warn` | 4 | warning |
| `approve` | 5 | notice |
| `allow` or other value | 6 | informational |

TCP appends a newline at the end of each message, but UDP does not. UDP's "send successfully" only means that the datagram was successfully written by the local machine, and does not mean that the receiving end has processed it; there is currently no TLS Syslog, message confirmation or receipt orchestration.

## Configure using the console

After logging in as an administrator, go to "Settings and Integration → Notification Settings". "Save" in the page will verify and persist the entire configuration and then hot load it; even if the channel is deactivated, its target address and fields must still pass the verification.

### Global settings

- **Enable notification**: Global switch. Closing preserves the channel configuration but does not deliver new notifications.
- **queue length `queue_size`**: The respective memory queue length of each enabled channel. The console allows `1`–`65535`, and the default display is 64; when the queue is full, the new copy is directly discarded and `dropped` is added.

The backend API also accepts `queue_size=0`, which is normalized to 64 at runtime; negative numbers have no effect. It is recommended to explicitly save positive integers in operation and maintenance configuration.

### Channel public fields

- **channel ID**: non-empty and unique in the entire configuration; it is used by both health status and Prometheus' `channel` tag. Use low-radix IDs consistently and avoid putting URLs, tenant secrets, or dynamic values ​​in the ID.
- **type**: `webhook` or `syslog`; each channel can only have a corresponding set of configurations.
- **Channel status**: The sending worker will not be established after deactivation, but the configuration is still saved.
- **triggers decision**: The backend supports `allow`, `warn`, `approve`, `deny`, and `error`. The empty array is normalized to `deny,error` by default in the backend. The console requires at least one to be selected, and the current drop-down box can only newly select `deny`, `error`, `warn`, and `allow`; `approve` read from the API can be retained, but the console cannot create a new option.
- **contains normalized SQL**: corresponding to `include_sql`. Confirm the receiving end authorization, retention period and data cross-border requirements before opening; it only allows `sql_norm` and will not send `sql_raw`.
- **allows private address**: corresponds to `allow_private_endpoints`, affecting both Webhooks and Syslog. It is turned off by default and should only be turned on temporarily and explicitly in local self-test or intranet SIEM scenarios.

### Webhook fields

- **template**: `generic`, `feishu`, `dingtalk`, `wecom` or `slack`.
- **URL**: required, value is treated as secret. Only `http`/`https` is allowed, URL user information or fragment cannot be included, and the port must be `1`–`65535`.
- **Bearer Token**: Optional; send `Authorization: Bearer <token>` after configuration. Usually only used by generic receivers.
- **signature Secret**: Optional; only actually used by `generic` and `dingtalk` templates. Feishu, WeChat Business, and Slack templates do not read this value.
- **Custom Headers**: Optional, all values ​​are treated as secret. Leave the saved value blank to indicate retention; see "Secret Storage and Update Contract" for details.

### Syslog field

- **host**, **port**, **transmission**, **Facility** are filled in according to the previous Syslog constraints.
- The host parses and verifies the address type when saving, and verifies it again when sending the dial.

### Send test and health status

Each channel card's "Send Test" sends a synthesized `deny` event. The test forces the channel to be considered enabled and only matches `deny`. The single sending timeout is 750 ms and there is no retry. The entire test request waits for up to 2 seconds; it does not modify the existing configuration and does not write the audit library.

The test results only have low cardinality categories, such as `sent`, `connect_error`, `timeout`, `http_status`, `send_error`. A successful test still triggers a real decision and checks the audit library, sink, and health count.

"Health Status" at the bottom of the page displays the in-process status. Manually click "Refresh" to read the latest value, not the persistent history.

## Management API

The four operations are all located under `/api/v1` and require Bearer authentication by the existing administrator:

```http
Authorization: Bearer <管理员登录取得的 token>
Content-Type: application/json
```

There must be exactly one space between `Bearer` and token, and there must be no spaces before, inside, or at the end of token. Unauthenticated returns HTTP 401. Successful responses are uniformly used:

```json
{
  "code": 0,
  "msg": "ok",
  "data": {}
}
```

Failure responses use HTTP status codes as `code` and `data` as `null`. Common statuses are 400 (Invalid request body), 401 (Not authenticated), 422 (Invalid notification configuration), 503 (Notification manager unavailable), and 500 (Store or hot reload failed).

### GET `/api/v1/integrations/notifications`

Read the complete configuration. Secret values ​​are never returned in clear text: the configured URL, Bearer Token, Secret, and each Header value are displayed as a fixed mask of `********`, and the corresponding `*_configured` boolean value is returned.

```bash
curl -sS \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://127.0.0.1:7780/api/v1/integrations/notifications
```

Sample response:

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

Atomic replacement of the entire configuration and hot reloading. All channels will be verified before saving; if persistence is successful but hot reloading fails, an attempt will be made to restore the old configuration. The response remains masked.

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

`url_configured`, `bearer_token_configured`, `headers_configured`, and `secret_configured` in the request are view states and do not determine how the secrets are merged; the real update contract is "under the same channel ID, leave the value blank or pass `********` to indicate no modification."

### POST `/api/v1/integrations/notifications/test`

The request body contains a `channel` with the same fields as the single channel in the configuration. If the channel ID is the same as a saved channel, you can leave the configured secret blank to reuse it.

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

Successful delivery:

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

Legal configuration but delivery failure still returns HTTP 200, `success=false`, `category` are failure categories; if the configuration itself is invalid, HTTP 422 is returned.

### GET `/api/v1/integrations/notifications/health`

```bash
curl -sS \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://127.0.0.1:7780/api/v1/integrations/notifications/health
```

Sample response:

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

Channels are sorted by ID. Only channels that were established running in the current process will appear; old IDs after deletion or deactivation may remain until the process is restarted. `last_success_at` may be Go zero time `0001-01-01T00:00:00Z` when there has been no successful transmission, and the console will display it as "—".

## security boundary

### SSRF default fail-closed

Webhooks share outbound address policies with Syslog. When `allow_private_endpoints=false` is set, all A/AAAA results of the host name are verified during the save/test phase, and the verified IP is re-parsed and directly connected during the dial-up phase; the actual remote IP is also verified after the connection is established to reduce the risk of DNS rebinding.

The default denial scope includes but is not limited to:

- Loopback: `127.0.0.0/8`, `::1/128`.
- RFC1918 private network: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`.
- IPv6 ULA and link-local: `fc00::/7`, `fe80::/10`.
- IPv4 link local `169.254.0.0/16`, including cloud metadata `169.254.169.254`.
- Shared addresses, document addresses, benchmark addresses and other reserved network segments, such as `100.64.0.0/10`, `192.0.2.0/24`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`.
- Unspecified address, `0.0.0.0/8`, IPv4/IPv6 multicast and reserved high address range.

> **Security Warning:** `allow_private_endpoints=true` will relax private network, loopback, link local and most reserved address checks, and therefore may also allow cloud metadata network segments. Only enable intranet SIEM channels in isolated local self-tests or when necessary, and continue to converge on targets using host firewalls, egress ACLs, and receiver authentication. `0.0.0.0/8`, Multicast, IPv4 `240.0.0.0/4`, IPv6 Unspecified Address and IPv6 Multicast are still rejected even if the switch is turned on.

Webhooks only allow HTTP or HTTPS. **plaintext HTTP is only available when `allow_private_endpoints=true` is used and** is available; this is only for local testing and compatibility with controlled intranets, and does not mean that HTTP has transmission security. Production outbound should use HTTPS with a valid certificate.

### Secret Storage and Update Contract

The following values are treated as secrets: Webhook URL (often containing token/key), Bearer Token, signature Secret, and all custom header values.

- The control plane is stored encrypted with AES-256-GCM using exactly 32-byte keys provided by `PasswordCipher` and `AGENTSQL_SECRET`.
- Each encryption uses a new random nonce, and the AAD of the notification value is fixed at `agentsql:notification-secret:v1`.
- GET and PUT responses never return clear text, only `********`, the corresponding `*_configured` Boolean value, and the console "configured" status.
- For the same channel ID, leaving the URL, Bearer Token, and Secret in the PUT or test request blank or passing `********` means retaining the original values.
- For an existing Header with the same name, leave the value blank or pass `********` to indicate retention; a non-empty value indicates replacement. Header name comparison is not case sensitive.
- If you submit an empty `headers` and the old configuration has headers, the server will retain the entire set of old headers. Therefore, the current interface cannot delete all existing headers through one "clear map"; the console also prohibits deletion of the last configured header. When it is necessary to completely clear the channel, delete the channel and save it, then re-create it and save it again.
- If a non-null `headers` is submitted, the uncommitted Header in the old configuration will be deleted; the new Header must provide a non-null value.

Do not write real URLs, tokens, secrets, or header values ​​into tickets, logs, command history, Git repositories, or screenshots. The values ​​in the examples are placeholders.

### privacy minimization

Outgoing payloads are constructed from explicit whitelisting and full audit records are not serialized directly:

- By default, `sql_raw`, `sql_norm`, binding values, object lists, result sets, client IPs, session IDs, session contents, or model names are not sent.
- `include_sql=true` Only normalized SQL `sql_norm` is allowed; raw SQL `sql_raw` is still not sent.
- The original `ErrorMsg` is never emitted, nor is it truncated; it is only mapped to the fixed `error_code`.
- `RuleHits` is not sent as it is, only the rule ID is reprojected to `rule_ids`; the rule message, suggestion and other contents will not be sent out.
- Agents and data sources contain only stable IDs and optional display names. Failure to refresh the name cache will not block notifications, and the name may be empty at that time.

The real enumeration and meaning of `error_code` are as follows:

| `error_code` | meaning |
| --- | --- |
| `rate_limited` | Hit rate/QPS/429 class limit |
| `row_limit` | Number of result rows, result size, or row limit exceeded |
| `timeout` | Timeout, deadline exhausted |
| `permission_denied` | Permissions, authentication, unauthorized or prohibited access |
| `syntax` | SQL syntax or parsing error |
| `connect_error` | DNS, dial-up, connection refused, network unreachable or connection reset |
| `unknown` | No original error, does not match the fixed pattern above, or is an unknown error |

Mapping uses a fixed order of string category matching to reduce sensitive information leakage and metric cardinality, and should not be used to replace root cause analysis in audit details.

### generic HMAC signature verification

After the Secret is configured in the `generic` template, AgentSQL adds two Headers:

```text
X-AgentSQL-Timestamp: <Unix 秒>
X-AgentSQL-Signature: sha256=<64 个小写十六进制字符>
```

The signature algorithm is:

```text
base = ASCII(timestamp) + "." + raw_body
signature = "sha256=" + hex(HMAC-SHA256(key=secret, message=base))
```

The receiving end must use the received **original request body byte** to verify the signature. It cannot parse JSON first and then serialize it; use constant time comparison and reject requests that differ from the local time by more than ±300 seconds. Time window verification is performed by the receiving end. AgentSQL does not save nonces and does not provide playback deduplication storage.

The following Python only uses the standard library and can be directly used as a temporary signature verification receiver:

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

### Source verification of platform templates

- **Dingding**: After configuring the signing Secret, AgentSQL uses the Secret as the HMAC-SHA256 key to sign `timestamp_ms + "\n" + secret`, Base64 and then encode the `timestamp` and `sign` URLs as query parameters. This algorithm is verified by the DingTalk receiving end. `access_token` in the webhook URL must also be kept secret.
- **Feishu**: The current `feishu` template only relies on the hook credentials in the Webhook URL and does not generate Feishu's `timestamp/sign` field; the "Signature Secret" field is invalid for it. You can use an IP whitelist in the Feishu robot security settings, or use a custom keyword containing `AgentSQL notification` for additional restrictions. Do not enable "Signature Verification" for this robot, otherwise the current AgentSQL payload will not pass.
- **Enterprise WeChat**: The current `wecom` template relies on `key` of the Webhook URL; the platform accepts requests through this unguessable credential, and AgentSQL does not add an additional Enterprise WeChat signature. Only the bot creator/controlled administrators are allowed to view the URL, and the bot is immediately deleted or rebuilt upon leakage.
- **Slack**: The Incoming Webhook URL is bound to the specified workspace, application, and channel, and the URL itself is the secret credential; AgentSQL does not add a Slack signature. Restrict installations and target channels through Workspace App Management, and revoke and recreate the URL in the Slack App Management page after it was leaked.

The above description of Feishu/Qiwei/Slack is "the platform verifies the credentials for AgentSQL to send messages to the platform", not the request signature scheme used by Slack/Feishu when sending callbacks to AgentSQL. The notification function is one-way outgoing and does not receive platform callbacks.

## Delivery semantics and reliability

### Ordinary webhooks and Syslog

- Each enabled channel has independent workers and bounded memory queues; slow channels do not synchronously block the SQL main link or other channels.
- When the queue is full, new notifications will be discarded in a non-blocking manner and the channel `dropped` will be added; no disk placement or reissue will be performed.
- Notifications only subscribe to new events after startup/hot reloading, and do not replay history. Notification copies can be lost during process stop, event bus congestion, subscriber removal, or hot reload switching.
- Each send attempt times out 3 seconds. This timeout is also used for syslog parsing, dialing, and writing.
- Retries up to 2 times after first failure, for a maximum of 3 send attempts; currently the same limited retries are performed for all send errors.
- The basic backoff for the two retries is 200 ms and 800 ms respectively, and `0.75`–`1.25` times jitter is applied, which is approximately 150–250 ms and 600–1000 ms; the upper limit is 1 second.
- The webhook only considers HTTP `2xx` as a success; reads and discards up to 4096 bytes of the response body, and does not write the health status of the downstream response.
- Webhooks do not follow redirects, and environment proxies are explicitly disabled. When an enterprise egress proxy is required, controlled forwarding should be configured at the network layer for explicit destinations rather than relying on `HTTP_PROXY`/`HTTPS_PROXY`.
- TCP Syslog notifies each new connection and closes after writing; UDP/TCP has no application layer confirmation, idempotent key or persistent retry queue.

Ordinary channels therefore do not guarantee ordering, uniqueness, or arrival. A receiver can use `audit_id` for deduplication; completeness must be checked against the audit database.

### Reliable webhooks

- Enable “reliable delivery” on an individual webhook channel and use an HTTPS receiver. This mode consumes final events from the committed tenant audit stream and resumes through a durable tenant/channel cursor after process restarts or across gateways. It does not apply to Syslog.
- Every request carries a stable `X-AgentSQL-Delivery-ID`; generic JSON also contains `delivery_id`. Receivers must process this ID idempotently. A crash after remote success but before local acknowledgement can resend, so delivery is at least once, with no exactly-once or guaranteed receiver acceptance claim.
- Failures use bounded backoff; after five failures the cursor enters a dead-letter state without advancing. Administrators can inspect backlog, bounded failure category, and oldest pending event in notification health. Replaying a dead letter requires authorization and a durable management audit first.
- A new channel starts at the committed stream tail read before configuration publication; earlier events are not automatically replayed. A gap between separate metadata and audit databases may replay a few events. To change endpoint, credentials, or filter, create a new channel ID, drain the old channel, then disable it.
- After restoring older metadata and audit backups, use a new channel ID before re-enabling delivery so divergent new events cannot reuse an old sequence's Delivery ID. Restore paired backups and reconcile receiver deduplication records separately.

## Health status and observability

The following ordinary-channel counters are process-local and reset on restart. Reliable channels also show the durable cursor, backlog, failures, next retry, and dead-letter state in notification health:

| Field | meaning |
| --- | --- |
| `sent` | Number of completed notifications sent; only incremented once after successful retries |
| `failed` | Number of failed notifications after exhausting all attempts; single failed attempts are not accumulated individually |
| `dropped` | The number of notifications discarded because the channel's own queue is full |
| `last_error` | Fixed category for the most recent final failure; subsequent successes will clear it |
| `last_success_at` | Last successful sending time |

`last_error`/Indicator `reason` Possible values are `panic`, `unknown`, `timeout`, `ssrf_blocked`, `connect_error`, `http_status`, `canceled`, `send_error`. It and the business `error_code` in the outgoing payload are two different enumerations: the former describes the notification delivery failure, and the latter describes the SQL/database event that was notified.

Prometheus exposes the following notification metrics:

| indicator | Type | label | meaning |
| --- | --- | --- | --- |
| `agentsql_notification_sent_total` | Counter | `channel` | Total number of successful sends |
| `agentsql_notification_failed_total` | Counter | `channel`, `reason` | Total number of failures after exhausting attempts |
| `agentsql_notification_dropped_total` | Counter | `channel` | The total number of discards when the channel queue is full |
| `agentsql_notification_last_error_info` | Gauge | `channel`, `reason` | The current most recent error category is 1; delete the category time series on success |
| `agentsql_notification_last_success_timestamp_seconds` | Gauge | `channel` | The Unix seconds timestamp of the most recent successful send |

It is recommended to at least warn: the failure count continues to increase, the discard count increases, and the latest success time is not updated for a long time. `dropped` only counts the notifier channel queue full, which does not mean that all upstream live event buses are lost.

## Persistence, migration and key rotation

Notification settings live in the metadata database's `notification_settings` and `notification_channels` tables, created by `0002_notifications.sql`. Reliable delivery also uses `notification_delivery_cursors`, added in v3.6.0:

- Default/combined deployment: in SQLite with other metadata and auditing, or in PostgreSQL.
- Detached PostgreSQL: Notification configuration only enters the metadata library and does not enter the independent audit library.
- SQLite → PostgreSQL migration copies notification settings, encrypted webhook fields, and reliable delivery cursors.

The encrypted ciphertext relies on `AGENTSQL_SECRET` when it was generated. When migrating or switching the control plane database, the target instance must use the same SECRET of exactly 32 bytes; otherwise, the notification secret cannot be authenticated and decrypted, and the service startup and loading notification configuration will fail. Back up the control plane database and environment files first, then migrate them, and use GET, send test and real `deny` event verification before cutting the flow.

Replacing `AGENTSQL_SECRET` cannot just change the environment variable: both the existing data source password and the notification secret use this key. There is currently no independent command for online re-encryption notification configuration; it needs to be processed according to the overall control plane key migration plan and a rollback arranged.

## End-to-end minimal example: local generic webhook

This example binds the receiving end to `127.0.0.1:18080`. This is the loopback target and `allow_private_endpoints` must be temporarily turned on; it is only used for stand-alone self-test. After completion, turn off the switch immediately and delete the test channel. The example requires AgentSQL to be in the same network namespace as the receiver; if AgentSQL is in a container and the container's `127.0.0.1` is not the host, the receiver should be put into the same container/network namespace, or a controlled private network address reachable by the host should be used and the URL should be modified simultaneously.

### 1. Start the temporary signature verification receiving end

Save the Python in "generic HMAC signature verification" as a temporary file `receiver.py`, and run it on the host where AgentSQL is located:

```bash
export AGENTSQL_WEBHOOK_SECRET='local-test-signing-secret'
python3 receiver.py
```

Windows PowerShell：

```powershell
$env:AGENTSQL_WEBHOOK_SECRET = 'local-test-signing-secret'
python .\receiver.py
```

### 2. Save the channel in the console

Enter "Settings and Integration → Notification Settings":

1. Turn on "Enable notifications" and keep `queue_size=64`.
2. Add a new Webhook; fill in the channel ID as `local_verify` and keep it enabled.
3. The trigger decision selects "deny"; "Include normalized SQL" remains off.
4. Turn on "Allow private addresses". The confirmation is only used for this loopback self-test.
5. Select "generic" for the template and fill in `http://127.0.0.1:18080/notify` for the URL.
6. Leave Bearer Token and custom headers blank; fill in `local-test-signing-secret` for signature Secret.
7. Click "Save". When you enter the page again, the URL and Secret should only show "Configured" and no clear text can be seen.

### 3. Send synthetic test first

Click "Send Test" on the channel card. The receiving end should output:

```text
[OK] signature verified
```

The console shows that the test was sent successfully. This step only verifies the synthesized `deny` and does not write the audit library.

### 4. Trigger real deny

Use the configured Agent and data source to initiate a request from the MCP client that is sure to be rejected by the current policy. For example, use`readonly`Agent call`execute_write`, or query a table without authorization policy. Confirm that the call result is`deny`, and find the corresponding one on the "Audit" page of the console`audit_id`。

The receiving end should then output `[OK] signature verified` again, `decision` in JSON is `deny`, and `audit_id` is consistent with the audit page; `sql_norm` or any of the original error strings should not appear by default. Return to "Notification Settings" to refresh the health status, `sent` should be added.

If you are in a local demonstration environment with Live Demo enabled, you can also select a fixed scenario with expected `deny` in "Demonstration Station → Real Trial Run (Live)" to generate real audit events; "Static Evaluation" only provides static display and does not write audits, so it cannot be used in this step.

### 5. Clean up

After verification is complete, stop the receiving end, delete the `local_verify` channel and save it, or at least turn off "Allow private addresses" and global notifications. Do not copy this example HTTP/loopback configuration to production.

## Non-goals and capability boundaries

- It does not replace the audit library, database native logs, or SIEM's complete collection link.
- No email, SMS, PagerDuty, two-way bots, platform callbacks, manual receipts, or workflow orchestration are provided.
- Ordinary channels have no guaranteed arrival, ordering, or exactly-once delivery. Reliable webhooks use durable cursors, retries, and dead letters, but still do not guarantee exactly-once receiver handling or automatically replay events predating enablement.
- No result sets are sent, no full DLP is done, and no SQL decisions for `allow`, `warn`, `approve`, `deny`, `error` are changed due to notification failure.
- TLS Syslog/mTLS is not provided; sensitive or cross-network Syslog should be forwarded through a controlled secure tunnel/local relay, or use HTTPS generic webhook in preference.
