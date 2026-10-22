# Task-level temporary read-only authorization

**Language:** [English](task-read-grants.md) | [Simplified Chinese](../task-read-grants.md)

> Current guidance: Applies to v3.0.2 and compatible 3.0.x maintenance releases. The historical introduction version and acceptance record retain the original facts; for daily entry, see [ document index ](README.md).

v0.8.0 changes temporary authorization to application, approval and one-time collection; v0.9.0 increases the result return budget for new applications. Applicants need
`query.execute`; the approver needs `query.execute` and `strategy.manage`, and must be
Another valid user of the same tenant. Return to the old `POST /api/v1/task-read-grants` direct issuance portal
HTTP 410; v0.7.0 The issued authorization will still be verified according to the original execution constraints before expiration or revocation.

## Management API

1. `POST /api/v1/task-read-grant-requests` Submit `agent_id`, `datasource_id`,
   `sql`, RFC 3339 `expires_at`, client-generated `idempotency_key`, and optionally
   `max_uses` (1–10, defaults to 1 when omitted). server from
   Log in to identify the applicant and tenant, and verify a single simple SELECT with a validity period of up to 24 hours.
   Save SQL SHA-256 with SQL encrypted using the `AGENTSQL_SECRET` derived key, without saving the clear text.
   The same applicant retries the same content with the same idempotent key,
   Return to the original application; if the content is different, HTTP 409 is returned.
2. `GET /api/v1/task-read-grant-requests` and `GET .../{id}` return application status.
   Applicants can only see their own applications; users with approval permissions can view this tenant's application.
   The list does not return SQL; only when an approver different from the applicant reads the details, the server decrypts and
   Verify SHA-256 to verify the exact statement. Lost keys or corrupted ciphertext can prevent approval.
3. `POST .../{id}/approve` or `/reject` only allows one other approver to process pending approval and
   Applications that have not expired. Applicants cannot self-approval; repeated approval will return HTTP 409.
4. Upon approval, `POST .../{id}/issue` will be called by the original applicant. The key only successfully responded to the display this time.
   Issuance status and authorization records are submitted in a metadata transaction; repeated calls, expiration, and revoking of approvers
   or the Agent has expired, a second valid authorization will not be generated.
5. `GET /api/v1/task-read-grants`, `GET .../{id}` only returns authorization metadata to the administrator.
   Among them, `max_uses` and `used_count` display the result budget and used times; the empty budget of the old version authorization
   Indicates unlimited times.
   `POST .../{id}/revoke` allows the original applicant to revoke his or her authorization, and also allows those with administrative rights
   The user revokes authorization from the same tenant; subsequent new requests are rejected.

Management auditing and status changes of applications, approvals, rejections, and issuances share metadata transactions. Deploy directly in the same library
Write to the audit table; PostgreSQL sub-library is deployed in the transaction to persistently manage the audit outbox, and then relay
Posted to the audit library. State changes are rolled back when metadata audit writes fail. Sub-library delivery delays and failures require
Monitored by the current tenant outbox backlog status and Prometheus metrics in the overview; traffic execution still requires persistent auditing to be available, otherwise execution is closed.
The audit only includes the application ID, authorization ID, SQL hash, status and operator, and does not record the Key or SQL original text.
When upgrading and restoring, the metadata backup should be kept in pairs with the original `AGENTSQL_SECRET`, otherwise it will be subject to approval.
The requested SQL cannot be decrypted. SQLite uses a consistent backup interface (such as `sqlite3 .backup`);
If there is a WAL page that has not been checkpointed, copying `agentsql.db` alone will miss the authorization and audit records.
The authorization ID of the console can jump to the audit page, and press `task_grant_id` to accurately match the management record and
Execution records entering the SQL pipeline; requests that are rejected before protocol authentication are still not within the scope of traffic auditing.

## execution boundary

The task key only allows the normal `query` path to the MCP. SQL must be exactly the same as the original bytes when applying
Identical and re-parsed by the gateway in the data source dialect. The current conservative subset only accepts a single entry, starting with `SELECT`
Simple query without parentheses at the beginning; function calls, subqueries, `WITH`, `INTO`, comments,
Row locks, `EXPLAIN`, multiple statements, and all write statements are denied.

The task key continues to be subject to the original Agent's permanent policies, rules, masking, and auditing, and its permissions will not be expanded.
`SELECT` type restrictions do not prove that a database function or view has no side effects; production data sources should still use
Minimum privileges, read-only database account.

The query that is already being executed will verify authorization again before database execution, before audit writing, and after audit writing; if
The last review finds that the authorization has been revoked or expired, and the gateway clears the result and appends the associated denial audit.
There is still a cross-instance concurrency window between the last review and the actual response submission, so revocation is not guaranteed to prevent all
Results already in transit are returned.

The budgeted authorization for v0.9.0 is passed through the same metabase after the persistent audit but before the results are actually returned.
Condition update atomic deduction once. Exhausted keys will no longer execute new queries; if a query that has been started concurrently is
The last number of contentions before the result is delivered, and only the request that grabs the budget can be returned. Transmission failure and process exception
Deducted times will not be refunded. Rejecting results, parsing failures, and audit failures do not consume the budget. v0.8.0 and earlier
The created empty budget authorization still operates according to the original validity period and revocation rules.
