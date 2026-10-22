# AgentSQL v2.6.0 Release Notes

**Language:** [English](release-notes-v2.6.0.md) | [Simplified Chinese](../release-notes-v2.6.0.md)

Status: Candidate version; officially released assets, GitHub Release, GHCR, official website and public website Demo must be updated after the closure CI and upgrade recovery acceptance.

## Scope of this edition

- Add Agent MCP `read_key` to specify TLS single instance for Redis 8.2.9 and Valkey 8.1.10. Requests only accept data source IDs and exact key names, and the server consistently executes `TYPE` and `GETRANGE` up to 64 KiB. Only UTF-8 strings are delivered; oversize, non-string, wildcard, native command, and cross-repository requests are rejected.
- Added independent `key` policy type: `dbN:exact-key` or bounded prefix `dbN:prefix*`. Recheck the current Agent, tenant, data source, and policy for each request; rejection takes precedence. The management API only allows key policies to be registered in the Redis/Valkey data source, and the console provides corresponding entry prompts.
- Desensitize the rules configured by the key value source, and write persistent audits for both pre-check and final state. Audit objects are identified using HMAC-SHA256 isolated by deployment key and data source, and the original keyname or value is not logged; data is not delivered when auditing or masking is not available.

## Validation boundaries

The isolated environment uses Linux x86_64 Docker images of Redis 8.2.9 and Valkey 8.1.10, test CA, TLS, minimal ACLs, and synthetic data. The real HTTP MCP permission and denial, masking, error CA, policy revocation of the two products, as well as the independent audit library and business database shutdown and re-masking reading after recovery have passed. MCP Inspector 0.16.8 and Dify MCPClient 1.17.1 complete the real allow and deny calls to the two databases respectively; the final results of the official package upgrade and public release chain are shown in [ release evidence ](v2.6.0-release-evidence.md), and the test details are shown in [ candidate acceptance ](v2.6.0-acceptance.md).

Other versions, Sentinel, Cluster, production certificate rotation, persistence strategies, long-term loads, binary values, lists/hashes/sets/ordered sets, writes and full production support are not verified. The public network Demo continues to use synthetic PostgreSQL data and does not provide public network Redis/Valkey.
