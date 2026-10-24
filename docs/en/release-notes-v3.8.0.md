# AgentSQL v3.8.0 Release Notes

**Language:** [English](release-notes-v3.8.0.md) | [Simplified Chinese](../release-notes-v3.8.0.md)

Status: formally released; the full outcome is recorded in the [release evidence](v3.8.0-release-evidence.md).

## Changes

1. `query_analytics` on the specified single-node ClickHouse 26.8.31.2 gains an optional datasource budget. Administrators may tighten per-query rows read from 10000 to 100–10000, bytes read from 1 MiB to 64 KiB–1 MiB, and memory from 128 MiB to 16–128 MiB. Query settings require overflow to throw, and metadata reads are limited to 128 columns. The readonly database role must permit these per-query settings within constrained ceilings; missing constraints fail closed.
2. `search_vectors` on the specified standalone Milvus 2.6.25 gains optional row-level tenant scope. Administrators select a static String/VarChar tenant field and up to eight approved filter fields. The gateway supplies the current Agent's authoritative tenant ID. Callers may submit at most four approved equality filters, never a native expression or tenant value. Each field needs an exact column grant. Returned hits are rechecked against tenant and filter values, and internal verification fields are withheld.
3. The Web datasource page edits and displays these `read_control_json` profiles with Chinese and English guidance. Existing `{}` profiles and MCP requests without `filter` remain supported.
4. The frontend build tools are pinned to Vite 8.3.5 and Vitest 4.1.12 with the affected transitive dependency updated; the full lockfile audit reports no known vulnerabilities.

## Compatibility and limits

There is no schema migration. Milvus tenant IDs and scalar filter values currently accept only 1–128 ASCII letters, digits, dots, underscores, or hyphens; other characters are denied. The Milvus `{}` path retains only the previous control-plane tenant isolation and does not imply row-level isolation in a shared collection. The ClickHouse `{}` path retains the prior budget and role contract. These capabilities cover only the specified image versions, single instances, TLS, least-privilege accounts, and synthetic data. Clusters, arbitrary SQL or expressions, writes, customer production workloads, and full production support are outside this conclusion. The public Demo still shows synthetic PostgreSQL/MySQL scenarios only.

See the [upgrade guide](v3.8.0-upgrade.md) for configuration and rollback, and the [acceptance record](v3.8.0-acceptance.md) for verified scope.
