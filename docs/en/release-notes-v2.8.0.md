# AgentSQL v2.8.0 Release Notes

**Language:** [English](release-notes-v2.8.0.md) | [Simplified Chinese](../release-notes-v2.8.0.md)

Status: Officially released. For the completion records of CI, signature assets, upgrade recovery, official website and public network Demo, see [ release evidence ](v2.8.0-release-evidence.md).

## Scope of this edition

- Specifying TLS standalone for Milvus 2.6.25`default`Add Agent MCP to the database`search_vectors`. The caller submits the data source ID, exact collection, FloatVector field, a finite float32 vector, explicit scalar field, and 1–20`top_k`;Does not submit native expressions or administrative commands.
- The gateway re-verifies the agent, tenant, current data source, exact `default.collection` table authorization, and column whitelisting of vector fields and all output fields each time. Controlled path rejects clear text, no authentication, `root`, and trusts any certificate configuration; connection verifies CA, hostname, and server version.
- Check collection structure, vector dimensions and COSINE index before execution; results are subject to request time limit, number of hits, number of bytes and source field masking. Milvus is connected only after the persistent preflight audit is successful, and the results are delivered only after the persistent final state audit is successful. Raw vector and scalar values ​​are not written for auditing.
- The old Milvus management preview remains a connection-level capability. The management side connection test of the controlled TLS data source changes to the new path; the old clear text configuration will not obtain the `search_vectors` permission.

## Validation boundaries

Milvus 2.6.25 official standalone image, synthetic data, minimal Search role for target collection, real HTTP MCP, allow/deny calls for MCP Inspector 0.16.8 and Dify MCPClient 1.17.1 passed. Wrong certificates, dimension mismatch, unauthorized collections/fields, revocation, result masking, persistent auditing, and failed shutdown and recovery after independent audit library and Milvus outage have also been verified; see [ Candidate Acceptance ](v2.8.0-acceptance.md) for details.

Other Milvus versions, clustering/replication, hybrid retrieval, writing, production certificate rotation, long-term loads, production data, and full production support are not verified. The public network demo continues to use synthetic PostgreSQL data.
