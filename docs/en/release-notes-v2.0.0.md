# AgentSQL v2.0.0 Release Notes

**Language:** [English](release-notes-v2.0.0.md) | [Simplified Chinese](../release-notes-v2.0.0.md)

v2.0.0 adds a fixed dual-gateway Kubernetes deployment solution. The two gateways share the PostgreSQL metadata database and independent audit database, and each saves the B2 runtime instance ID; stateless MCP read-only requests are continued to be processed under control by the other gateway when one gateway is unavailable. Deployment includes non-root run, standalone PVC, node anti-affinity, Service, readiness probe, manual migration job, read-only schema check and pre-deployment check script.

PostgreSQL column policy adds new management side `POST /api/v1/policies/{id}/bind-postgres`. The administrator submits the policy current `ETag` and a registration SELECT; the gateway first discovers the directory, then holds the business table directory lock under the control plane policy version lock, reviews the object and column identities, and then submits the atomic submission for formal binding. Register SQL is not executed. This version only supports `CATALOG_CLOSED_V1`, single ordinary table and explicit column projection; management-side registration of native extended mode, cross-table, view and wildcard projection will still be rejected.

When two gateways of the same version are started at the same time, there will be bounded retries for brief concurrency conflicts between the initial creation of RBAC and the B2 control fence. A persistent database failure, a policy not registered, or a different binary digest still fails to close.

## Upgrade and run boundaries

- Uses the same PostgreSQL metadata 21, audit 6 schema as v1.9.0. The migration must still be run explicitly and check the dual database status, it cannot be migrated covertly by the gateway.
- B2 lease binding binary digest, v1.9.0 and v2.0.0 cannot be mixed active. Kubernetes manifests use `OnDelete`; both upgrades and rollbacks require a maintenance window: backup the set, stop both old Pods, wait for the lease to expire, check the schema, and start the new version. **This version does not promise no-downtime rolling upgrade.**
- Dual-gateway configuration turns off B5 cross-request transactions, HTTP status sessions, and event resumption; node local connections or WALs are not treated as shared status. The original single-node deployment contract will be applicable separately.
- Two replicas are fixed, the upper limit of Agent QPS of each is 10, the theoretical upper limit of the cluster is 20; the upper limit when a single node fails is 10. This current limit is executed on a process-by-process basis and does not constitute a global strong consistent quota; arbitrary horizontal expansion is not supported.
- High availability, TLS, backups, least privileges and capacity for the PostgreSQL metabase, audit repository are provided by the deployer. This version of Kubernetes acceptance uses an isolated two-node kind cluster with synthetic PostgreSQL data and is not equivalent to customer production topology certification.

This version does not add database adaptation or expand the production support statement of existing databases. For details on deployment, fault recovery and acceptance, see [Kubernetes operation guide ](../../deploy/kubernetes/README.en.md), [ design ](v2.0.0-design.md) and [ acceptance record ](v2.0.0-acceptance.md).
