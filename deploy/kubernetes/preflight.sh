#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-default}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

command -v kubectl >/dev/null || { echo 'kubectl is required' >&2; exit 1; }

# A fixed two-replica StatefulSet has required anti-affinity by hostname.
# Count ready workers; control-plane nodes are normally tainted NoSchedule.
node_count="$(kubectl get nodes --no-headers | awk '$2 == "Ready" && $3 !~ /control-plane|master/ { count++ } END { print count+0 }')"
if [[ "$node_count" -lt 2 ]]; then
  echo 'preflight failed: at least two Ready worker nodes are required' >&2
  exit 1
fi
kubectl wait --for=condition=Ready node --all --timeout=30s >/dev/null

for key in AGENTSQL_SECRET AGENTSQL_ADMIN_PASSWORD AGENTSQL_STORE_METADATA_DSN AGENTSQL_STORE_AUDIT_DSN; do
  if [[ -z "$(kubectl -n "$namespace" get secret agentsql-runtime -o "jsonpath={.data.${key}}" 2>/dev/null)" ]]; then
    echo "preflight failed: agentsql-runtime is missing $key" >&2
    exit 1
  fi
done

rendered="$(kubectl kustomize "$script_dir")"
if ! grep -Fq 'replicas: 2' <<<"$rendered" ||
   ! grep -Fq 'type: OnDelete' <<<"$rendered" ||
   ! grep -Fq 'image: ghcr.io/cuipengdba/agentsql:v2.4.0' <<<"$rendered" ||
   ! grep -Fq 'instance_id_file: "/var/lib/agentsql/instance-id"' <<<"$rendered" ||
   [[ "$(grep -Fc 'driver: postgres' <<<"$rendered")" -lt 2 ]] ||
   grep -Fq 'sqlite_path:' <<<"$rendered" ||
   ! grep -Fq 'stateful: false' <<<"$rendered" ||
   ! grep -Fq 'event_store_enabled: false' <<<"$rendered" ||
   ! grep -Fq 'auto_migrate: false' <<<"$rendered"; then
  echo 'preflight failed: expected fixed two-replica v2.4.0 safety profile is missing' >&2
  exit 1
fi

kubectl -n "$namespace" apply --dry-run=server -k "$script_dir" >/dev/null
echo "KUBERNETES_PREFLIGHT=PASS namespace=$namespace nodes=$node_count"
