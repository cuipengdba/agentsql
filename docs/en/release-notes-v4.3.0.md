# v4.3.0 Release Notes: Q&A Presentation, Dashboards and Embedding

**Language:** [Chinese](../release-notes-v4.3.0.md) | [English](release-notes-v4.3.0.md)

Status: published on 2026-10-26 UTC. The [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.3.0) contains signed assets. See the [v4.3.0 design record](v4.3.0-design.md) for scope and the [release acceptance record](v4.3.0-acceptance.md) for evidence.

## Delivered

| Capability | Verified scope |
| --- | --- |
| AgentSQL Q&A presentation | Table, bar, line and pie views of currently authorized results. Reopening a historical chart reauthorizes and audits through the gateway instead of replaying stored rows |
| Independent dashboards | Resource tree, text and record-chart cards, tabs, layout, search, preview, and configuration import/export without result rows. Selected canvas workflows passed against an unmodified SQLBot v1.10.2 client with local synthetic data |
| Analysis and trends | Real local-model analysis stream and historical child records. Ordered, untruncated numeric or date series can show an illustrative linear trend with its input range |
| Assistants and page embedding | Independent management page for basic, advanced and web-embedded applications; encrypted settings, enable/disable, secret rotation and transactional audit. Embed tokens bind to an existing non-admin user, workspace and live app state; conversations are isolated between apps |
| Original-client comparison | The unmodified SQLBot v1.10.2 embedding SDK loaded its original Q&A page locally, selected a synthetic file datasource, created a conversation and completed a permitted two-row column read. The record contained two rows and gateway audit ID 713 |

## Security and applicability

AgentSQL is an independently implemented database security and Q&A product. It can act as a controlled database gateway for an existing SQLBot deployment or provide its own Q&A and management pages. The compatibility comparison covers only the pinned client, synthetic data and listed actions. It does not establish complete SQLBot Community feature or page parity. Zero-change replacement of the user's business system requires before/after cutover evidence from its isolated environment and remains a separate v4.5.0 gate.

The application signing secret is exposed only through authorized management endpoints; the public app handshake never returns it. Disabling an app, rotating its secret, or changing user state or authorization invalidates subsequent reads. When an app declares a datasource list, requests are limited to it; an empty list follows the signed account's current datasource grants.

The current column-authorization mode rejects aggregation, sorting and complex SQL when it cannot prove them safe. Local `count(*)` and sorting requests released no result and are not claimed as supported. Complex chart mapping, dashboard sharing, all assistant callbacks, the API/MCP compatibility matrix and real business integration remain for later acceptance. Model interactions in the public Demo are clearly labeled simulations and neither run nor call a real model.

## Upgrade and release

Back up metadata, audit, managed-file databases and encryption keys before upgrading. The local synthetic v4.2.0-to-v4.3.0 upgrade, failed-start rollback and formal asset verification passed; the website and public Demo now serve v4.3.0. Use the signed packages, dual-architecture images, GitHub Release and [release acceptance record](v4.3.0-acceptance.md) from the same source commit.
