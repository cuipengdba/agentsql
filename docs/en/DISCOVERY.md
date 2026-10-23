# AgentSQL Sensitive Column Discovery Guide

**Language:** [English](DISCOVERY.md) | [Simplified Chinese](../DISCOVERY.md)

> Current guidance: Applies to v3.1.0 and compatible 3.1.x maintenance releases. The historical introduction version and acceptance record retain the original facts; for daily entry, see [ document index ](README.md).

AgentSQL's sensitive column discovery is targeted at DBAs and security administrators. It performs a controlled, synchronous, read-only check on a small number of basic tables explicitly designated by the administrator, combines column names and optional samples to find suspected sensitive columns, and can generate draft masking rules that are ineffective by default for currently supported categories.

> **Important: Findings are heuristic recommendations, not data classification conclusions or proof of compliance.** It may be a false positive or a false negative. Before generating a draft, the business meaning of the columns must be checked by an administrator who understands the purpose of the data; the scope and masking effects must also be verified before the rules are enabled.

## Functional positioning and non-targeting

Sensitive column discovery is only used to assist in the masking configuration of existing data sources:

- Read the column metadata of the explicitly specified table, and only do optional sampling for candidate columns whose column names match.
- The discovery request is returned synchronously without creating an asynchronous task or persisting a discovery result that can be queried repeatedly.
- It was found that only six categories were identified: mobile phone number, email address, ID card, bank card, IP address, and date of birth, and only draft rules for `algo=mask` and `enabled=false` were generated; the drafts would not automatically participate in runtime masking.
- It was found that using the database account already saved in the data source does not require adding permissions to the MCP Agent, nor does it require new MCP tools.

It is not an automatic masking, complete DLP or compliance determination tool. It does not modify data in business tables, does not automatically enable rules, and does not provide a "leave blank to scan the entire database/all tables" mode. It does not deduce or generate business SQL from natural language; the controlled read-only service only generates fixed metadata queries and sample statements in the form `SELECT <候选列> FROM <限定表> LIMIT <上限>`, and verifies the read-only statement structure before execution.

Administrators can manually create `hash`, `block`, and `range` rules on the "masking Rules" page; `generic`, `number`, and `date` are not discovery categories. None of them enter the discovery process. discovery/advisor always only produces the aforementioned six categories of `mask` recommendations, and apply only accepts these recommendation combinations; submissions of `range`, `number`, `date`, `hash`, `block` or `generic` will be rejected.

## Discovery categories and identification basis

Currently, six categories of discovery are fixedly supported, and `mask` drafts can be generated for all six categories. Column names will be split according to snake case, camel case, dashes and spaces. Only complete tokens or known phrases will be matched, without any substring matching. `data_type` will be returned with the results for administrators to check, but the current classifier does not use the database type as an independent decision signal.

| Category | API value | Typical column name signals | Sample verification summary | Drafts can be generated with one click | Current algorithm |
| --- | --- | --- | --- | --- | --- |
| Mobile phone number | `phone` | `mobile`, `phone`, `phone_number`; medium signal `tel` | Optional `+86`, verify the 11-digit number segment in mainland China | Yes | `phone` + `mask` |
| Email | `email` | `email`, `email_address`; medium signal `mail` | Complete email format | Yes | `email` + `mask` |
| ID card | `idcard` | `id_card`, `idcard`, `identity_number`; medium signal `identity` | 18 digits to verify the true date of birth and check digit; 15 digits to verify the old certificate date | Yes | `idcard` + `mask` |
| bank card | `bankcard` | `bank_card`, `bank_account`; medium signal `card_no` | ASCII spaces/hyphens allowed; 13–19 bits normalized and passed Luhn | Yes | `bankcard` + `mask` |
| IP address | `ip` | `ip`、`ip_address`、`client_ip` | Verify IPv4, IPv6, `inet` or `cidr` with `net/netip` | Yes | `ip` + `mask` |
| date of birth | `birthdate` | `birth_date`, `date_of_birth`, `dob`, `birthday`; medium signal `birth` | Verify real calendar, non-future date and age 0–120 years old | Yes | `birthdate` + `mask` |

The six categories of discovery items will all return recommendation rules and`algo=mask`, under normal circumstances`applicable=true`, which can be generated after being checked in the console`enabled=false`draft. Found not to be recognized or labeled`number` / `date`, would not recommend or apply`range`, nor will it generate`generic`、`hash`、`block`draft. When bucketing of values ​​or date truncation is required, you must go to the "Anonymization Rules" page to create a new one manually.`number` / `date`of`range`rule.

This boundary is intentionally designed: automatic discovery makes classifications based on column names and sample formats, while numerical bucket width, bucket offset, and date truncation granularity depend on the report caliber, re-identification risk, and downstream usage. They are business decisions and should not be automatically determined by sampling. Names and addresses are not currently found either, and naked `name`, `address`, and `username` will not be candidates.

## Category six `mask` runtime masking style

When a rule is enabled, AgentSQL applies the following deterministic partial masking by column at the query result layer. Input is stripped of leading and trailing whitespace; the number of stars in the table is part of a fixed algorithm.

| Category | original value | Occlusion value | Summary of rules |
| --- | --- | --- | --- |
| Mobile phone number `phone` | `13812345678` | `138****5678` | 11-digit mobile phone number retains the first 3 and last 4, and fixes 4 stars in the middle |
| Email `email` | `alice@example.com` | `a***@example.com` | Only the first character is retained in the local part and 3 stars are appended, and the domain name is retained |
| ID card `idcard` (18 digits) | `110105199001011234` | `110105********1234` | The first 6 + 8 stars + the last 4; the last `x` is uniformly output as `X` |
| ID card `idcard` (15 digits) | `130503670401001` | `130503******001` | Top 6 + 6 stars + Next 3 |
| Bank card `bankcard` | `4111 1111-1111 1111` | `411111******1111` | After removing ASCII spaces/hyphens, the first 6 digits of BIN and the last 4 digits are retained; the number of asterisks in the middle is the total number of digits minus 10 |
| IP address `ip` (IPv4) | `192.168.1.2` | `192.168.*.*` | Keep the first two paragraphs, i.e. `/16` granularity |
| IP address `ip` (IPv6) | `2001:0db8:85a3::1` | `2001:0db8:****` | Keep the first 32 bits of the two hextets and output them as fixed four-digit lowercase |
| Date of birth `birthdate` | `1990-01-02`、`1990/01/02`、`19900102`、`1990-01-02T00:00:00Z` | Both are `1990-**-**` | Keep the four-digit year, month and day coding, and discard the time and time zone |

Date of birth is also accepted`1990/01/02`、`19900102`, RFC3339/RFC3339Nano and SQL timestamp; for example`1990-01-02T00:00:00Z`Also output`1990-**-**`, time and time zone suffixes are not preserved. Bank cards are only morphologically normalized and do not perform Luhn verification in the masking stage; the length must be 13–19 digits. PostgreSQL`inet`/`cidr`returned`/32`、`/128`、`/24`etc. prefixes will be removed before masking; IPv6 with zone, domain name and`host:port`Not accepted as an IP address.

> **Risk reminder: Partial masking does not equal anonymization.** The mobile phone number still retains the first 3 and the last 4, the ID card still retains the first 6 and the last 4, the bank card still retains the BIN and the last 4, the IP retains the network segment, and the date of birth still retains the year. These trade-offs serve the purpose of operation and maintenance readability, statistics, and troubleshooting, and do not constitute irreversible anonymization, nor do they replace storage layer protection. When irreversible hash fingerprinting, integer blocking with a fixed `***`, value bucketing, or date truncation is required, `hash`, `block`, or `range` can be manually configured on the Masking Rules page; none of these algorithms are recommended by the discovery process.

## Strict discovery, loose masking

Discovery and runtime masking have different responsibilities, and the differences in strategies are by design:

- **strictly found that** answered "Is this column a sensitive column?" The classifier verifies the real date of birth and check digit for ID cards, Luhn for bank cards, `net/netip` for IP, real calendar for date of birth and checks 0–120 years and future dates. The strategic preference is not to report the problem, rather than mislead the administrator with weak evidence.
- **loosely obscures** to enforce the protection intent that the administrator has enabled. During runtime, only the corresponding type of form is judged and normalized, and ID card verification digits, bank card Luhn or age verification are not repeatedly performed; if the form is identifiable, mask it according to the table above.
- After the corresponding type rule is enabled for a column, if the non-null value cannot be normalized according to the type, the fixed constant `[REDACTED]` will be output and the original value will never be returned. Empty strings, SQL `NULL` and `<nil>` sentinels remain as is and do not count towards `MaskedCells`.

The real database-driven return shape has been incorporated into two layers of processing. For example, after MySQL DSN enables `parseTime`, DATE may become `2026-09-17T00:00:00Z` through stringifier; PostgreSQL `date` may also become RFC3339Nano, and `inet`/`cidr` may return `192.0.2.10/32` and `198.51.100.0/24` respectively. Both discovery and masking are compatible with these forms.

## Confidence and human review

Sample evidence excludes `NULL` and blank values. At least 3 valid samples are required to form supporting or contradictory evidence; at least 80% of the valid samples pass the corresponding format check as "support", and less than 50% are regarded as "contradictory".

| Conditions | result |
| --- | --- |
| Turn on sampling, strong listing signal and sample support | high |
| Sampling is turned on, the listing signal is strong but the effective samples are insufficient, or the support rate is 50%–less than 80% | in |
| When sampling is turned on, the strong listing signal is inconsistent with the sample. | Do not return this discovery |
| Enable sampling, medium listing signal and sample support | in |
| Turn on sampling, medium listed signal but not supported by the sample | Do not return this discovery |
| Turn off sampling, strong list signal | in |
| Off sampling, medium listing signal | low |

When there are no column name candidates, the sample will not be read for the purpose of "trying it out", nor will a column be discovered based on the sample content alone. The overall strategy favors false negatives and avoids false positives, but it cannot eliminate false positives or false negatives; for example, product code columns whose names look like mobile phone numbers still require manual judgment.

## Scan range and fixed upper limit

Currently 1–20 `{schema, table}` must be specified explicitly. Empty arrays, duplicate tables, only schema, or more than 20 tables will fail; there is no "leave empty and scan all tables" behavior. Only scan `BASE TABLE` identified in the database metadata, not views, materialized views, or external tables.

> **500 is the upper limit of the total number of metadata columns, not the upper limit of the number of tables.** When the total number of tables listed in one request exceeds 500 columns, the entire request will fail and the table range must be split; the system will not silently intercept the first 500 tables or continue running the first 500 columns.

All upper limits are server-side fixed values, and there are currently no corresponding configuration items or environment variables:

| Project | Default value | hard cap or behavior |
| --- | ---: | --- |
| Explicit table number | Required | 1–20 |
| Total number of metadata columns | — | 500 |
| Number of candidate columns | — | 50 |
| Number of candidate columns for each sampling SQL | — | 10, if exceeded, batch according to the same table |
| `sample_rows` | 10 | 20; can only be 1–20 when sampling is enabled |
| Single statement timeout | 2 seconds | If the data source `stmt_timeout_ms` is smaller, use the smaller value |
| overall discovery deadline | 12 seconds | Do not return partial results after timeout |
| Total number of sampled values | The default number of rows is up to 500 | 1,000; by return cell count, including empty cells |
| Current limiting and concurrency | — | The same administrator is 1 QPS; the same "administrator + data source" concurrency is 1, the same data source is concurrent 1, and the global concurrency is 2 |

Each sampling SQL returns at most `sample_rows` rows. The implementation does not have a separate "sum of rows returned for all batches" constant; the total is constrained by a maximum of 20 rows per bar, a maximum of 10 candidate columns per bar, a maximum of 50 candidate columns, and a maximum of 1,000 sampled cells.

After sampling is turned off, the system only reads column metadata, and the amount of business data read is zero; `sample_rows` should not be sent with the request, and the highest confidence level is medium.

## Permissions, Visibility, and Least Privilege Recommendations

Discover the saved database account of the reused data source. It does not reuse the object authorization policy of the MCP Agent, so there is no need to open additional MCP permissions; the database account itself must be able to read the metadata of the listed underlying tables and be able to `SELECT` candidate columns when sampling is enabled. A separate, minimally privileged, read-only account should be used instead of PostgreSQL superuser, database owner, or MySQL `root`.

Permission errors are distinguished by the stage of occurrence:

- When establishing a connection or an unknown database in the `Ping` stage, without permission to enter the database, authentication failure and network failure, the executor will be uniformly classified as "data source unreachable" and will not leak driver errors to the client. The current discovery management API will converge such unsubdivided internal errors to `500 DISCOVERY_INTERNAL` and "Sensitive Discovery Failure"; you can first perform "Test Connection" troubleshooting on the data source page.
- After the connection has been established, during the query PostgreSQL SQLSTATE `42501`, and MySQL `1044`, `1142`, `1143`, `1227`, the mapping is `403 DISCOVERY_PERMISSION_DENIED`, and the page prompts "The data source account does not have the right to read some objects."
- The metadata interface may silently hide results for unprivileged objects. In order to avoid misjudgment of "invisible" as "no sensitive column", if any specified table is missing or invisible, it will fail as a whole with `403 DISCOVERY_SCOPE_NOT_VISIBLE`, and the table will not be skipped to continue to return partial results.

PostgreSQL is scoped to the current database and excludes `pg_catalog` and `information_schema`; MySQL's `schema` must equal the data source's configured `database`.

## Sample data security

Sampled values are passed to the classifier only in process memory for the current request:

- Does not write to the control plane database or discovery results table.
- The management audit text is not written; the audit only records explicit table ranges, switches, row counts, and aggregate counts.
- Not outgoing via SSE real-time events or webhooks or syslog notifications.
- The discovery response is not written, and the page is not returned; the page only sees column-level metadata, signal names, and matching/valid sample counts.
- After the request is completed, the relevant references are released and the sample preview is not retained for subsequent requests.

Go string and database driver buffers do not provide cryptographically secure erasure, so there is no guarantee that sample bytes will be reliably zeroed out in process memory. Highly sensitive environments should turn off "sample verification", use only column name heuristics, and further narrow the visible range through database account permissions.

## Generate drafts and rule scopes

apply only accepts the `mask` combination corresponding to each of the six discovery categories, such as `idcard/idcard/mask`; the category and sensitive type must be the same. Submitting non-recommended combinations such as `algo=range`, `sensitive_type=number`, `sensitive_type=date`, or `algo=hash`, `algo=block`, `sensitive_type=generic` will be rejected with HTTP `422`, `DISCOVERY_NOT_APPLICABLE` and no draft will be created. The admin HTTP layer will reject these combinations; the draft application entrance that bypasses HTTP and directly calls the store will also be rejected, and non-discovery drafts cannot be generated through this. New rules are forcibly set by the server:

- `enabled=false`: Draft does not take effect by default.
- `schema_name=""`: Discovery always generates table-only drafts and does not write the schema in the discovery metadata as a precise scope. PostgreSQL will not hard-fill `public`; an empty schema matches the table in any schema. MySQL empty schema represents the table in the database to which the current data source is connected.
- `table_name=<发现到的真实表名>`: Table names retain original case as runtime matching range.
- `column_name=<规范化列名>`, `algo=mask`: The column name removes the leading and trailing blanks and converts it to lowercase. The algorithm is fixed to `mask`.

The draft must go to the "Masking Rules" page to manually check the data source, table, normalized column names, sensitive types and algorithms before manually enabling it. The `enabled=false` draft itself does not desensitize any results. When the PostgreSQL schema or explicit MySQL database needs to be precisely defined, `schema_name` should be filled in manually on the masking page; the discovery process will not fill it in for you.

The physical identity of the discovered item is `(datasource, schema, table, column)`; after generating the table-only draft, the persisted unique key is `(datasource_id, schema_name='', table_name, normalized column_name)`. Therefore, `customers.phone` and `suppliers.phone` will generate two independent drafts and will not be merged by column name; columns with the same name in two different tables must be checked and enabled separately. Since the draft intentionally clears `schema_name`, the discovery items of "different schemas, same table_name, and same column_name" in the same data source will fall into the same table-only target key and cannot be distinguished by automatic drafts; in such scenarios, schema-qualified rules should be manually established on the masking page.

apply is idempotent and responds by sorting the items into the following array:

| result | meaning | Handling suggestions |
| --- | --- | --- |
| `created` | Create a new table-only, inactive draft | Go to the "Hypolysis Rules" page to check the list and activate it. |
| `existing` | The same datasource/table/column target key already has rules of the same type and algorithm and will not be created repeatedly; existing rules can be enabled or disabled. | View existing rule status and content |
| `covered_by_global` | Enabled global column rules (`schema_name=''`, `table_name=''`) already exist for columns with the same name, and table-only drafts will no longer be created. | Check the type, algorithm and scope of global rules |
| `conflicts` | The same datasource/table/column target key already has rules of different types or algorithms | Return `409 MASK_RULE_CONFLICT`; process existing rules first. Do not create a new draft of the batch plan when a conflict occurs in the batch |
| `ambiguous` | The same request submitted conflicting categories, types, or algorithms for the same table/column target key | Do not create a draft of the target key; check the source of the request and try again |

only**enabled**Only global column rules can be entered.`covered_by_global`. Disabled global drafts do not block discovery, and target tables can still create their own table-only drafts. A maximum of 50 items can be applied at one time; the exact same target key in the same request will be aggregated idempotently, and different`table_name`Columns with the same name are processed separately.

## Console operation steps

1. Log in to the console as an administrator and enter "Data Sources".
2. Turn on "Sensitive Discovery" in the operations of the target data source.
3. Click "Add Table" and fill in 1-20 complete schema and basic table names.
4. Determine whether to turn on "sample verification" according to the environment; when turning it on, select 1-20 lines, and the default is 10 lines. Close sampling in highly sensitive environments.
5. Click "Start Discovery" to view the category, confidence, signal and `matched_samples / eligible_samples` sample support; the original sample will not be returned to the page.
6. Manually check column usage and recommended types; six categories, including mobile phone number, email address, ID card, bank card, IP address, and date of birth, can be checked.
7. Click "Generate masking draft". Each different table and column generates its own table-only draft; when conflicts or ambiguities are encountered, follow the page prompts to narrow the scope or check existing rules.
8. Go to the "masking Rules" page, check the data source, `schema_name`, `table_name`, normalized column name, type and algorithm again, and activate it manually after confirmation.

This process is a management plane operation, reusing the data source account, and does not require modification of the MCP client, Agent API Key or Agent authorization policy. The discovery capabilities themselves are also not exposed as MCP tools.

## Management API

Both endpoints require administrator Bearer tokens, belong to management operations, and write `discover` and `discover.apply` management audits respectively. When there is no valid administrator authentication, `401` is returned. The request body uses strict JSON: unknown fields and trailing JSON return `400`, up to a maximum of `1 MiB`.

### Initiate discovery

`POST /api/v1/datasources/{id}/discover`

```json
{
  "tables": [
    {"schema": "public", "table": "customers"}
  ],
  "sampling": true,
  "sample_rows": 10,
  "categories": ["phone", "email", "idcard", "bankcard", "ip", "birthdate"]
}
```

- `tables`: required, 1–20 unique `{schema, table}`.
- `sampling`: optional, default `true`.
- `sample_rows`: Optional when sampling is enabled, the default is 10; the value passed explicitly can only be 1–20. This field cannot be transmitted at the same time when `sampling=false` is set.
- `categories`: Optional; omitting or empty array indicates all six categories.

A successful response to `data` contains:

- `scope`: Actual data source, table, sampling switch, number of sampling rows and category.
- `stats`: Number of tables requested/scanned, columns read, columns candidate, columns/values ​​sampled, and items found.
- `limits`: Current fixed upper limit.
- `findings[]`: `schema`, `table`, `column`, `data_type`, `category`, `signals` , `confidence`, `sampled`, `matched_samples`, `eligible_samples`, `recommended_rule`, `applicable`, `existing_rule` and optional `reason`. It does not contain the original sample values.

### Generate draft

`POST /api/v1/datasources/{id}/discover/apply`

```json
{
  "items": [
    {
      "schema": "public",
      "table": "customers",
      "column": "phone",
      "category": "phone",
      "sensitive_type": "phone",
      "algo": "mask"
    }
  ]
}
```

`items` must be 1–50 items. All six categories can be submitted, but `category` and `sensitive_type` must be the same, and `algo` must be `mask`; for example, `birthdate/birthdate/mask`. `range`, `number`, `date`, `hash`, `block` and `generic` are not applicable to discovery apply. `422 DISCOVERY_NOT_APPLICABLE` will be returned after submission. If necessary, you should manually create a new ordinary rule on the "Densitization Rules" page. Request DTO does not accept `enabled`, sample value or free signal fields. The successfully responded `data` contains five arrays `created`, `existing`, `covered_by_global`, `conflicts`, `ambiguous` and `counts` summary.

`schema` in the apply request is the discovery source field and is not equal to `schema_name` in the final rule. The rules generated by the server are fixed to `schema_name:""`, `table_name:<真实表名>`, and `enabled:false`. When the rule is subsequently read via `GET /api/v1/mask_rules`, `schema_name` will be echoed; when normal rules are manually maintained via `POST /api/v1/mask_rules` or complete `PUT /api/v1/mask_rules/{id}`, separate `schema_name` and `table_name` fields are also used. To tighten the table-only draft to the exact schema, `schema_name` should be filled out explicitly via a masking page or a full PUT before enabling.

### HTTP status code

| HTTP status code | Stable error code or scenario | meaning | Handling suggestions |
| ---: | --- | --- | --- |
| `200` | Success; may also contain only `existing`, `covered_by_global` or `ambiguous` | Request processed | Check response categories and counts, don't just look at HTTP successes |
| `400` | `INVALID_JSON` | Illegal JSON, unknown field or trailing JSON | Fix request by DTO; do not pass `enabled` |
| `401` | Admin Bearer is invalid | Failed to pass management authentication | Log in again or update admin token |
| `403` | `DISCOVERY_PERMISSION_DENIED` | Connection successful, but insufficient metadata or sampling query permissions | Provide the minimum necessary permissions for the read-only account of the data source |
| `403` | `DISCOVERY_SCOPE_NOT_VISIBLE` | The specified table is missing or invisible to the account, and the entire table is fail-closed. | Check schema, table name and visibility, will not skip silently |
| `404` | Data source does not exist | Data source ID in path is invalid | Refresh the data source list and check the IDs |
| `409` | `MASK_RULE_CONFLICT` | The same datasource/table/column target key already has rules of different types or algorithms | Go to the "Masking Rules" page to handle the conflict; the response can include grouping results |
| `413` | `REQUEST_TOO_LARGE` | JSON request body exceeds `1 MiB`; not a logical range code for "21 tables" | Reduce the request body; see `422` for logical limits such as the number of tables and columns. |
| `422` | `DISCOVERY_SCOPE_LIMIT`, `DISCOVERY_CANDIDATE_LIMIT`, `DISCOVERY_SAMPLE_LIMIT`, etc. | Empty range, table/column/candidate/sampling exceeds limit, or parameter or category is illegal | Split the table range, reduce the number of sampled rows, or correct parameters |
| `422` | `DISCOVERY_NOT_APPLICABLE` | The category, sensitivity type or algorithm is not an acceptable combination for the current discovery apply, including `range` / `number` / `date` and `hash` / `block` / `generic` | Submit the six types of sensitivity types of the same category and use `mask`; for other rules, go to the "masking Rules" page to manually create new ones |
| `429` | `DISCOVERY_RATE_LIMITED` | 1 QPS or concurrency quota is occupied | Wait for the current request to complete and then try again |
| `500` | `DISCOVERY_INTERNAL` | Including internal failures such as connection/Ping "data source unreachable" that are not further differentiated by the current API | Test the connection first; then check the server logs and data source configuration |
| `503` | `DISCOVERY_UNAVAILABLE` | Controlled discovery service is not ready | Check service startup and dependency injection status and try again |
| `503` | `AUDIT_UNAVAILABLE` | Auditing is not available; successful results will be discarded and apply persistence will fail-closed | Restore the metadata/audit control plane and try again |
| `504` | `DISCOVERY_TIMEOUT` | Server-side single statement or overall deadline timeout | Reduce the range, reduce the number of sampled lines, or turn off sampling |

The server currently does not actively return `408`. The console will display the reverse proxy's `408`, client 15-second timeout, and `504` as timeouts, and give the same narrowing recommendations.

It was found that the audit only writes fixed whitelist statistics, not samples. apply audit record request count, `created`, `existing`, `covered_by_global`, `conflict`, `ambiguous` count and normalized table.column key; notify projection not to send out these management details.

## Configuration and deployment

Sensitive column discovery itself has no new YAML configuration items or environment variables. It continues to use the existing administrator authentication, `AGENTSQL_SECRET` decrypted data source credentials, metadata/audit control plane and data source `stmt_timeout_ms`, but will tighten the discovery single statement timeout to no more than 2 seconds. Manually configuring `hash` rules required for `redaction.hash_key` / `AGENTSQL_REDACTION_HASH_KEY` are not part of the discovery process, see [ Deployment Guide ](DEPLOY.md#masked-hash-key-management); `block` does not require any keys or configuration. Control plane migration and backup requirements remain subject to the deployment guide.

If administrative auditing is not available, discovery will not return results to the caller; apply will not treat "unaudited success" as a normal completion. The audit-prioritized fail-closed process is used when metadata and audit are divided into databases. In extreme cases, records may be left that have been audited but the metadata draft has not been created. When retrying, the apply response and the "Anonymization Rules" page should prevail.

## Upgrading from an earlier version

Table. Column-aware masking is migrated from the control plane. `0005_mask_rule_scope.sql` is added. `schema_name` is added to solidify the existing rules into global column rules and upgrade the unique key to datasource/schema/table/normalized column. By default, the four SQLite/PostgreSQL migration flows of merged and independent metadata will be executed; for upgrade, pre-check, backup, rollback and `current=latest=5`, see [ deployment guide ](DEPLOY.md#v03-table-column-aware-masking-control-plane-migration-0005).

There is one area of ​​compatibility behavior that needs to be checked before upgrading: if you have bypassed the management API, contact`mask_rules`write`sensitive_type`for`idcard`、`bankcard`、`ip`or`birthdate`And enabled rules, these rules are not supported in earlier versions and will fail to load; after the upgrade, they will be loaded normally and participate in runtime masking immediately. Please check and review these four types of historical direct writing rules before upgrading.`datasource_id`, normalized column names, types, algorithms and`enabled`state.`phone`、`email`The existing behavior remains unchanged.

At the same time, inventory `algo=hash` rules: If there is an enabled rule, at least a 32-byte hash key must be safely injected before the upgrade is started, otherwise the service will fail-fast; only disabled `hash` can be started without a key in draft. `block` does not participate in the startup access control and can run normally without a key. Key rotation changes all fingerprints, see the deployment guide for details.

## Limitations and follow-up planning

The following discovery capabilities are under planning and are not supported by the current version:

- Automatically discover `number` / `date` and recommend `range` parameters; `range` can be manually configured in ordinary masking rules, but the discovery process will not recommend, generate or apply it. `hash`, `block` and `generic` also do not enter the discovery process.
- Automatically deduce and fill accurate PostgreSQL schema from discovery results; current discovery intentionally generates table-only drafts, with manual tightening when schema-qualified is required.
- Discovery and masking of names, addresses and more sensitive types.
- Asynchronous, timed, or large-scale discovery of tasks, as well as cross-request progress, cancellation, and retries.
- Export findings.
- Link with compliance policies, automated grading or disposition processes.

## Known limitations

- 1–20 underlying tables must be specified explicitly; schema-wide, full database, all tables, or view scans are not supported.
- A single request can read up to 500 metadata columns, identify 50 candidate columns, and process 1,000 sample cells; if the limit is exceeded, the entire request will fail and no partial results will be returned.
- Identification relies on fixed column names and format heuristics, which may result in false positives or false negatives; the database type is currently only for display and does not participate in classification.
- When sampling is turned off, only the column names are looked at, and the highest confidence level is medium; when sampling is turned on, only the first few rows in the table that are not guaranteed to be sorted are extracted.
- Only the above six categories are found and generated`enabled=false`of`mask`draft; name, address,`number`、`date`Not currently found.`range`、`hash`、`block`and`generic`It can only be configured manually on the Masking Rules page and will not be recommended, generated or applied by the discovery process.
- The draft does not take effect by default and must be manually checked and enabled; business data will not be automatically modified when found.
- Drafts are processed by table-only target keys; columns with the same name in different tables generate separate rules. It was found that the exact schema will not be automatically filled in. Tables/columns with the same name across schemas from the same data source require manual establishment of schema-qualified rules.
- The sample is not persisted, returned, or entered into the audit body or notification, but the process memory is not cryptographically securely erased.
- The function is only provided in the management API/console, it is not an MCP tool, and it is not restricted by the MCP Agent authorization policy.
- Discovery is a synchronous operation with an overall deadline of 12 seconds; asynchronous tasks, result export or compliance conclusions are not supported.
