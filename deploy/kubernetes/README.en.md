# AgentSQL three-gateway Kubernetes deployment reference

**Language:** [English](README.en.md) | [Simplified Chinese](README.md)

This directory is a v3.5.0 fixed three-gateway candidate, starting from the accepted v2.4.0 dual-gateway sample. The [v3.5.0 design](../../docs/en/v3.5.0-design.md) and real three-node acceptance determine which capabilities ship. Rehearse production upgrades against the [current operations guide](../../docs/en/OPERATIONS.md).

This manifest targets **three nodes, exactly three replicas, and stateless read-only MCP requests**. The admin event stream resumes per tenant from the separate PostgreSQL audit database, with at most 50 SSE connections per node. Stateful HTTP sessions and B5 transactions remain disabled; the admin event cursor is not an MCP session cursor. The three-replica manifest does not establish cross-instance ownership of stateful sessions.

## Prepare

- Three schedulable Kubernetes worker nodes, supporting StorageClass of independent `ReadWriteOnce` PVC per replica.
- The PostgreSQL metadata library and independent audit library have been configured for TLS, backup, and minimal privileges running account. Don't use SQLite. Both DSNs must point to the same set of confirmed control plane data, and the running account must not have DDL permissions.
- Private environment files are provided separately`agentsql-runtime`with`agentsql-migration`Secret is created. Files do not enter Git; migration files use a dual-repository account with DDL permissions, and running files uses an account with minimum permissions. Both must use the same**Exactly 32 bytes**of`AGENTSQL_SECRET`, and provide`AGENTSQL_ADMIN_PASSWORD`、`AGENTSQL_STORE_METADATA_DSN`、`AGENTSQL_STORE_AUDIT_DSN`. Credentials are only injected via Secret.
- Before migration, make a complete set of backups of metadata and audit, and record rollback points. Do not write the new deployment to the same control plane as the old version at the same time, unless the mixed version acceptance has passed.

The following command assumes that the current directory is the repository root directory and the namespace is `agentsql`. The environment file path is specified by operation and maintenance and does not include credential content on the command line.

```sh
kubectl create namespace agentsql
kubectl -n agentsql create secret generic agentsql-runtime --from-env-file=/secure/agentsql-runtime.env
kubectl -n agentsql create secret generic agentsql-migration --from-env-file=/secure/agentsql-migration.env
kubectl -n agentsql apply -f deploy/kubernetes/configmap.yaml
```

## First installation or upgrade from an earlier release

First make a complete set of backups of the two PostgreSQL libraries and record the old image summary. `migrate` may execute DDL; log the check results even if this version of the schema has not changed. The job is not in the base `kustomization.yaml` to avoid implicit migration in normal deployment. **Active B2 gateways require compatible version digests. Versions without separate compatibility proof must not serve together. This manifest uses `OnDelete`, and B2 version changes still require a maintenance window.**

When upgrading from an earlier release, perform the following maintenance-window operations first; skip them for a first installation. Record the old Pods that actually exist before stopping all replicas.

```sh
old_pods=$(kubectl -n agentsql get pods -l app.kubernetes.io/name=agentsql -o name)
kubectl -n agentsql scale statefulset/agentsql --replicas=0
for pod in $old_pods; do kubectl -n agentsql wait --for=delete "$pod" --timeout=180s; done
# Wait for the old runtime lease to expire; the default lease is 15 seconds, the actual value is subject to the configuration value, allowing for clock offset.
sleep 30
```

Perform migration and read-only schema checks after the old gateway is completely stopped. The first installation also starts here.

```sh
kubectl -n agentsql apply -f deploy/kubernetes/jobs/migrate.yaml
kubectl -n agentsql wait --for=condition=complete job/agentsql-migrate-v3-5-0 --timeout=300s
kubectl -n agentsql logs job/agentsql-migrate-v3-5-0
kubectl -n agentsql apply -f deploy/kubernetes/jobs/schema-check.yaml
kubectl -n agentsql wait --for=condition=complete job/agentsql-schema-check-v3-5-0 --timeout=300s
kubectl -n agentsql logs job/agentsql-schema-check-v3-5-0
bash deploy/kubernetes/preflight.sh agentsql
```

After preflight passes, apply and verify all three replicas.

```sh
kubectl -n agentsql apply -k deploy/kubernetes
kubectl -n agentsql wait --for=create pod/agentsql-0 pod/agentsql-1 pod/agentsql-2 --timeout=180s
kubectl -n agentsql wait --for=condition=Ready pod/agentsql-0 pod/agentsql-1 pod/agentsql-2 --timeout=600s
```

`migrate --check` Must display `status=current` for both metadata and audit with exit code 0. The gateway must not be started when the version table is missing, behind or ahead of version. All three initContainers are checked again for read-only. The readiness probe only adds available copies to the Service; the survival probe only determines whether the process is alive.

When creating a new PostgreSQL column-level policy, first create a table-level permission policy for the same object, and then create column policies that specify the `object_type: column`, `action: allow`, and `columns` lists. Read the `ETag` of this column policy, call `POST /api/v1/policies/{id}/bind-postgres` with `If-Match`, and the request body is `{"sql":"SELECT id,name,phone FROM public.customers"}`. Registration SQL is only used for catalog proof, not execution; this version only accepts a single ordinary table of PostgreSQL `CATALOG_CLOSED_V1`, explicit column projection, and all reference columns must be in the policy list. The returned new policy revision and formal binding are subject to three-node MCP allow/deny revalidation. Management-side registration of native extended schemas, views, cross-table or wildcard projections is still not open; unbound column policies will not be executed as table authorizations on failure.

Deployment acceptance should read `/healthz` and `/readyz` of `agentsql-0`, `agentsql-1` and `agentsql-2` to confirm distinct B2 instance IDs; perform allow, deny, masking and persistent audit tests through the Service. Connect admin SSE to a different gateway and verify newly generated audit IDs, tenant cursors, resume and cross-tenant isolation. Stop a Pod and confirm that the two remaining Pods continue processing; requests in flight on the lost node fail without silent continuation. Audit or metadata outages must close requests, followed by recovery checks. HTTP 200 alone is not a complete security check.

If the metadata interruption exceeds the B2 lease, the existing process will remain `B2_RUNTIME_HEARTBEAT_FAILED` or `B2_RUNTIME_LEASE_EXPIRED` downgraded; after restoring the database, rebuild all three Pods according to the maintenance process, and reconfirm all three `B2_READY`, allow/deny/masking and persistent auditing. Database reconnection alone does not count as capacity restoration.

```sh
kubectl -n agentsql get pods -l app.kubernetes.io/name=agentsql -o wide
kubectl -n agentsql get endpointslice -l kubernetes.io/service-name=agentsql
kubectl -n agentsql port-forward pod/agentsql-0 17780:7780
```

All three replicas have separate 1 GiB PVCs for persistent B2 instance identity. The same PVC must not be mounted to two copies, and the PVC must not be deleted during a normal upgrade. Each replica limits Agent QPS to 10, giving a theoretical three-node ceiling of 30; after one node fails, the ceiling is 20, and uneven distribution may limit the flow earlier. Admin SSE is capped at 50 connections per node, or 150 across three nodes. A reconnecting client sends its tenant-local `Last-Event-ID`; a backlog exceeding 1000 events or a sequence gap requires a fresh read through the authoritative audit API. Changes to replica count and budgets require a new design and validation.

## Changes, rollbacks and credentials

Before upgrading, run a read-only schema job, backup the set, and record the current image summary and configuration. Explicitly stop all old Pods, wait for the old lease to expire, then apply the new image and start three new Pods; stop publishing when migration or version deviation fails, and check logs and database snapshots. Code rollback also requires stopping all three new Pods, waiting for the lease to expire, and then using the old version of the official image and isolation-verified dual database backup; if the schema has changed, you cannot just roll back the image. The admin ingress should be protected by outer TLS/access control; this list does not create a public network ingress.

After the migration is completed, delete the migration job and `agentsql-migration` Secret. To run Secret rotation, you need to stop all replicas during the maintenance window, verify the compatibility of the new key with the existing encrypted data and B2 lease, and then start the new copy at the same time; you cannot directly replace `AGENTSQL_SECRET` without migrating or verifying the encrypted data. All logs and acceptance reports only record versions, exit codes, non-sensitive instance IDs, and audit IDs, not DSNs, passwords, or Agent Keys.
