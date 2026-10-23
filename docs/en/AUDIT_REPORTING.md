# Audit query and compliance report (v3.0.x)

**Language:** [English](AUDIT_REPORTING.md) | [Simplified Chinese](../AUDIT_REPORTING.md)

> Current guidance: Applies to v3.1.0 and compatible 3.1.x maintenance releases. The historical introduction version and acceptance record retain the original facts; for daily entry, see [ document index ](README.md).

> This article describes the current read-only capabilities of `agentsqlctl audit`. The specific parameters are subject to this version `agentsqlctl audit --help`; for the boundaries of compliance archiving and external witness, see [v2.9.0 design ](v2.9.0-design.md).

`agentsqlctl audit` directly reads the audit storage pointed to by the configuration: `audit_logs` in the metadata database is read when the storage is shared, and audit PostgreSQL is read when the independent audit storage is enabled. The command only reads the database and does not perform migration.

## Audit query

The default output is a table suitable for terminal reading; `--format json` outputs a JSON object containing `total`, `returned`, `limit`, and `records`. By default, a maximum of 100 items are returned, and `--limit` can be set from 1 to 1000; when there are more matching events, a prompt will be given on stderr.

```powershell
agentsqlctl audit query -c config.yaml `
  --since 2026-09-01T00:00:00Z `
  --until 2026-10-01T00:00:00Z `
  --action query --actor agent-prod-1 --db finance `
  --rule pii --status success --limit 200 --format json
```

The time parameters use RFC3339/RFC3339Nano, and the boundaries are all inclusive relationships.

## Filter fields

The CLI name maps to the real fields of `audit_logs` as follows. All exact matches use parameterized queries.

| CLI parameters | Actual fields/rules | Matching method |
|---|---|---|
| `--since` | `ts` | `ts >=`, including bounds |
| `--until` | `ts` | `ts <=`, including bounds |
| `--action` | `action` | exact match |
| `--actor` | `actor_id` or `agent_id` compatible with old traffic records | Exactly match any field |
| `--db` | `datasource_id` | Exact match; the current schema has no column named `db` |
| `--rule` | `rule_hits` | Literal quantum string matching, `%`, `_` are not used as wildcards |
| `--status error` | `decision` | `decision = 'error'` |
| `--status success` | `decision` | `decision IN ('allow','deny','approve','warn')` |
| `--error-code` | `error_code` | exact match |
| `--uuid` | `event_uuid` | Exact match after verification as UUID |

`audit_logs` currently does not have a separate `status` column, so `status` is query semantics derived from the real `decision` column; a policy rejection (`deny`) indicates successful audit processing and does not equate to a system error. `--rule` directly queries the existing `rule_hits`. It does not create a fictitious `rule` column, nor does it query `details_json`.

## Compliance report export

The report must be written to a new file via `--out`, stdout only prints the path, format, number of events and summary path. To avoid accidental overwriting, the command fails when the target file or digest file already exists. CSV is the default format, JSONL is also an option.

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

The PDF is generated using pure Go, without launching a browser or external binary; the report includes generation time, applied filters, decision/action distribution, and event-by-event summary, and supports Chinese. The full archive contains both CSV, JSONL, PDF, digest, and file-by-file SHA-256 manifests:

```powershell
agentsqlctl audit report -c config.yaml --format pdf `
  --out .\reports\audit-compliance.pdf
agentsqlctl audit report -c config.yaml --format archive `
  --out .\reports\audit-compliance.zip
agentsqlctl audit verify-archive --in .\reports\audit-compliance.zip
```

`verify-archive` checks the ZIP filename, member set, size, `manifest.json` schema, and SHA-256 of each payload on a fail-closed basis; any directory traversal, duplicate/extra members, digest, or size inconsistency will fail. It only proves that "the verified ZIP is consistent with the package manifest" and does not verify author identity, signatures, external timestamps, or currency.

The default safety limit for reports is 10,000, which can be increased to 100,000 using `--limit`. Instead of generating a truncated report when the number of hits exceeds the limit, it fails and requires filtering to be narrowed or the limit to be explicitly raised.

Successful CSV, JSONL, and PDF exports also generate `<out>.summary.json`; archive places the same summary within the ZIP as `summary.json` and no longer generates a bypass file. Summary contains:

- `total_events`: The total number of events exported this time;
- `time_range`: The earliest and latest `ts` in the exported event, when the report is empty, the two are `null`;
- `action_distribution`: Statistics are based on `action`, and empty values are recorded as `<none>`;
- `status_distribution`: Statistics based on the above success/error derivation rules;
- `top_error_codes`: Up to 10 error codes, stably sorted in descending order of quantity and ascending name;
- `top_actors`: Priority is given to `actor_id`, compatible with fallback to `agent_id`, up to 10;
- `columns`: Report fixed column contract.

## Fixed export columns

CSV headers and JSONL objects use the following 27 `audit_logs` business columns, in the following order:

```text
id, ts, agent_id, datasource_id, session_id, conversation_id,
mcp_tool, db_type, sql_raw, sql_norm, stmt_type, objects,
decision, rule_hits, risk_level, est_rows, rows_returned, latency_ms,
client_ip, model_name, error_msg, error_code, action, actor_type,
actor_id, details_json, event_uuid
```

CSV uses a UTF-8 BOM, empty database values ​​are output as empty fields, and text prefixes that may trigger spreadsheet formula execution are neutralized. JSONL has one complete object per row, and the database null value output is JSON `null`. Chain maintenance fields `chain_seq`, `prev_hash`, `self_hash`, `chain_key_version`, and `chain_format_version` do not belong to this version of the report contract; chain status and integrity verification continue to use `agentsqlctl chain`.

## current scope

- PDF uses standard CJK CID font mapping; no external font files are embedded, and no browser calls are made. If an extremely old or incomplete PDF reader lacks CJK alternative fonts, the display effect may be different, and a pre-release spot check should be done on the organization's standard reader.
- The archive's `manifest.json` is not signed, and SHA-256 does not prevent an attacker from replacing both the payload and the manifest. Organization-level signatures, WORM/SIEM, and trusted timestamps are still required when source authenticity, undeletable retention, or external currency is required.
- This version does not connect to `b5_tx_events`; the B5 event has been associated with audit records through `audit_log_id`, but the fixed report uses `audit_logs` as the only data source.
- All currently required filter fields can be mapped to the real schema. There are no separate `status`, `db`, `rule` columns and their downgrade/mapping rules are clearly stated in the table above.
