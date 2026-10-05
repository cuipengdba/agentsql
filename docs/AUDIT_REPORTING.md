# 审计查询与合规报表（v0.5 规划/新增）

> 本文描述 AgentSQL v0.5 规划中的 `agentsqlctl audit` 能力。v0.4 已发布命令的行为不受影响。

`agentsqlctl audit` 直接读取配置所指向的审计存储：共享存储时读取 metadata 数据库中的 `audit_logs`，启用独立审计存储时读取 audit PostgreSQL。命令只读数据库，不执行迁移。

## 审计查询

默认输出适合终端阅读的表格；`--format json` 输出包含 `total`、`returned`、`limit` 和 `records` 的 JSON 对象。默认最多返回 100 条，`--limit` 可设为 1～1000；存在更多匹配事件时会在 stderr 给出提示。

```powershell
agentsqlctl audit query -c config.yaml `
  --since 2026-09-01T00:00:00Z `
  --until 2026-10-01T00:00:00Z `
  --action query --actor agent-prod-1 --db finance `
  --rule pii --status success --limit 200 --format json
```

时间参数使用 RFC3339/RFC3339Nano，并且边界均为包含关系。

## 过滤字段

CLI 名称与 `audit_logs` 的真实字段映射如下。所有精确匹配均使用参数化查询。

| CLI 参数 | 实际字段/规则 | 匹配方式 |
|---|---|---|
| `--since` | `ts` | `ts >=`，包含边界 |
| `--until` | `ts` | `ts <=`，包含边界 |
| `--action` | `action` | 精确匹配 |
| `--actor` | `actor_id` 或兼容旧流量记录的 `agent_id` | 精确匹配任一字段 |
| `--db` | `datasource_id` | 精确匹配；当前 schema 没有名为 `db` 的列 |
| `--rule` | `rule_hits` | 字面量子串匹配，`%`、`_` 不作为通配符 |
| `--status error` | `decision` | `decision = 'error'` |
| `--status success` | `decision` | `decision IN ('allow','deny','approve','warn')` |
| `--error-code` | `error_code` | 精确匹配 |
| `--uuid` | `event_uuid` | 校验为 UUID 后精确匹配 |

`audit_logs` 当前没有独立的 `status` 列，因此 `status` 是由真实的 `decision` 列派生的查询语义；策略拒绝（`deny`）表示审计处理成功，并不等同于系统错误。`--rule` 直接查询现有 `rule_hits`，不虚构 `rule` 列，也不查询 `details_json`。

## 合规报表导出

报表必须通过 `--out` 写入新文件，stdout 只打印路径、格式、事件数和摘要路径。为避免意外覆盖，目标文件或摘要文件已存在时命令失败。CSV 为默认格式，也可选择 JSONL。

```powershell
agentsqlctl audit report -c config.yaml `
  --since 2026-09-01T00:00:00Z --until 2026-10-01T00:00:00Z `
  --status error --format csv --out .\reports\audit-errors-2026-09.csv
```

```powershell
agentsqlctl audit report -c config.yaml `
  --action query --db finance --format jsonl `
  --out .\reports\finance-query.jsonl
```

PDF 使用纯 Go 生成，不启动浏览器或外部二进制；报告包含生成时间、已应用筛选、决策/动作分布和逐条事件摘要，并支持中文。完整归档同时包含 CSV、JSONL、PDF、摘要和逐文件 SHA-256 清单：

```powershell
agentsqlctl audit report -c config.yaml --format pdf `
  --out .\reports\audit-compliance.pdf
agentsqlctl audit report -c config.yaml --format archive `
  --out .\reports\audit-compliance.zip
agentsqlctl audit verify-archive --in .\reports\audit-compliance.zip
```

`verify-archive` 会失败关闭地检查 ZIP 文件名、成员全集、大小、`manifest.json` schema 以及每个载荷的 SHA-256；任何目录穿越、重复/额外成员、摘要或大小不一致都会失败。它只证明“所验 ZIP 与包内清单一致”，不验证制作者身份、签名、外部时间戳或最新性。

报表默认安全上限为 10,000 条，可用 `--limit` 提高到 100,000。匹配数超过上限时不会生成截断报表，而是失败并要求缩小过滤范围或明确提高上限。

CSV、JSONL 和 PDF 成功导出还会生成 `<out>.summary.json`；archive 将同一摘要作为 `summary.json` 放在 ZIP 内，不再生成旁路文件。摘要包含：

- `total_events`：本次导出的事件总数；
- `time_range`：导出事件中最早和最晚的 `ts`，空报表时两者为 `null`；
- `action_distribution`：按 `action` 统计，空值记为 `<none>`；
- `status_distribution`：按上述 success/error 派生规则统计；
- `top_error_codes`：最多 10 个错误码，按数量降序、名称升序稳定排序；
- `top_actors`：优先使用 `actor_id`、兼容回退到 `agent_id`，最多 10 个；
- `columns`：报表固定列契约。

## 固定导出列

CSV 表头和 JSONL 对象固定使用以下 27 个 `audit_logs` 业务列，顺序如下：

```text
id, ts, agent_id, datasource_id, session_id, conversation_id,
mcp_tool, db_type, sql_raw, sql_norm, stmt_type, objects,
decision, rule_hits, risk_level, est_rows, rows_returned, latency_ms,
client_ip, model_name, error_msg, error_code, action, actor_type,
actor_id, details_json, event_uuid
```

CSV 使用 UTF-8 BOM，空数据库值输出为空字段，并对可能触发电子表格公式执行的文本前缀进行中和。JSONL 每行一个完整对象，数据库空值输出为 JSON `null`。链维护字段 `chain_seq`、`prev_hash`、`self_hash`、`chain_key_version`、`chain_format_version` 不属于本版报表列契约；链状态与完整性验证继续使用 `agentsqlctl chain`。

## 当前范围

- PDF 使用标准 CJK CID 字体映射；不嵌入外部字体文件，也不调用浏览器。极老或不完整的 PDF 阅读器若缺少 CJK 替代字体，显示效果可能不同，应在组织的标准阅读器上做发布前抽检。
- archive 的 `manifest.json` 没有签名，SHA-256 不能防止攻击者同时替换载荷和清单。需要来源真实性、不可删除保留或外部最新性时，仍需组织级签名、WORM/SIEM 和可信时间戳。
- 本版不联接 `b5_tx_events`；B5 事件已经通过 `audit_log_id` 关联审计记录，但固定报表以 `audit_logs` 为唯一数据源。
- 当前所需过滤字段均可映射到真实 schema。不存在单独的 `status`、`db`、`rule` 列，其降级/映射规则已在上表明确说明。
