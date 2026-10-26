# v4.2.0 Release Notes: Multi-turn Q&A and Controlled File Data (Candidate)

**Language:** [Chinese](../release-notes-v4.2.0.md) | [English](release-notes-v4.2.0.md)

Status: candidate, not yet released. This document describes implemented behavior with local evidence. The release status will be updated after formal artifacts, upgrade checks and public acceptance. See the [v4.2.0 design record](../v4.2.0-design.md) for detailed boundaries.

## New capabilities

| Capability | Scope in this version |
| --- | --- |
| Multi-turn Q&A | Up to three successful questions and SQL statements from the same conversation provide context; every turn rechecks identity, datasource binding and table/column permissions before controlled execution and audit |
| Business semantics | Manage terms, SQL examples and recommended questions within the workspace and currently authorized datasource; examples are bounded model context and are never executed directly |
| File datasources | Administrators import bounded CSV/XLSX files through an isolated managed PostgreSQL child binding, encrypted staging, audited publication, controlled preview and deletion; the original SQLBot page can select a subset of sheets |
| Row rules | Candidate path for PostgreSQL single-table queries with explicit qualified names and narrow predicates; the gateway applies current rules, and preview, historical reads and export reauthorize them |
| Query results | AgentSQL's own page provides CSV/XLSX exports under current permissions and actual execution summaries; the original SQLBot Excel download was compared for synthetic history |
| History migration | Import verifiable conversation metadata from the pinned SQLBot Community baseline, preserving original IDs and provenance; old SQL, result rows, charts and credentials are excluded |

## Verified compatibility boundary

Local checks used the unmodified SQLBot Community v1.10.2 browser, an isolated PostgreSQL 16 managed database and a local Qwen model. For synthetic CSV data, the original page completed login, upload, datasource save, schema, preview, three Q&A turns, historical reads, Excel download and deletion. A two-sheet XLSX was previewed with both sheets selected and with only West selected. Under a temporary `region = 'east'` row rule, datasource preview, three historical reads and the Excel file returned only East. After restarting the AgentSQL gateway and ChatBI application containers, the original page could still read the existing managed datasource and its two preview rows. This evidence covers the stated synthetic data and client version only.

File import rejects formulas, duplicate headers, path traversal, oversized decompressed content and numeric overflow. Selected sheets are stored in the encrypted binding; unselected sheets cannot be queried through that datasource. Focused tests for cross-user access, audit failure, revocation and lost responses check denial and recovery. They do not establish acceptance of the user's business system.

## Limits and upgrade

This release does not claim parity with every SQLBot page or API, or zero-change replacement of the user's existing application. Dashboards, assistants, embedding, full settings, richer charts, analysis and prediction remain for later versions. Managed files currently accept CSV/XLSX only; subset-sheet SQL uses the closed PostgreSQL SELECT grammar. Unknown syntax and authorization paths that cannot be proved are rejected. Row rules cover only the verified narrow PostgreSQL shape.

Before upgrading, back up the metadata, audit and managed databases together with the AgentSQL encryption key. Keep the original SQLBot instance and configuration until real business integration has passed. The standard offline service package does not include model weights or a model runtime. The public Demo offers clearly labeled simulated interactions only and does not connect to a real model or business database.

## Release gates

The same commit must pass Go, Web, documentation and dependency checks; fresh installation, upgrade and rollback, signed artifacts, dual-architecture images and anonymous pulls must be verified. Only then should the GitHub Release, website and Demo be updated to the actual version. Unverified items remain open; route counts and static pages are not compatibility evidence.
