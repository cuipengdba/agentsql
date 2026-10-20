import { request } from "./client";

export interface PolicyBundle {
  schema_version: "agentsql.policy-bundle/v1";
  tenant_id: string;
  policies: Array<Record<string, unknown> & { id: string }>;
}

export interface PolicyBundleSnapshot {
  bundle: PolicyBundle;
  digest: string;
}

export interface PolicyBundleChange {
  id: string;
  operation: "create" | "update" | "delete";
  broadens_grant: boolean;
  from_revision?: number;
  to_revision?: number;
}

export interface PolicyBundlePlan {
  base_digest: string;
  desired_digest: string;
  changes: PolicyBundleChange[];
  replay: Array<{ agent_id: string; datasource_id: string; object_name: string; before: string; after: string; reason: string }>;
  live_check: boolean;
  notice: string;
}

export interface PolicyBundleProposal {
  id: string;
  tenant_id: string;
  base_digest: string;
  desired_digest: string;
  desired: PolicyBundle;
  author_user_id: string;
  reviewer_user_id?: string;
  status: "pending" | "approved" | "rejected" | "applied";
  created_at: string;
  reviewed_at?: string;
  applied_at?: string;
}

export interface PolicyBundleStatus {
  current_digest: string;
  approved_digest?: string;
  proposal_id?: string;
  state: "uninitialized" | "approved" | "drifted";
}

export function currentPolicyBundle(): Promise<PolicyBundleSnapshot> {
  return request<PolicyBundleSnapshot>({ method: "GET", url: "/policy-bundles/current" });
}

export function planPolicyBundle(baseDigest: string, desired: PolicyBundle, replayCases: Array<{ agent_id: string; datasource_id: string; object_name: string }> = []): Promise<PolicyBundlePlan> {
  return request<PolicyBundlePlan>({ method: "POST", url: "/policy-bundles/plan", data: { base_digest: baseDigest, desired, replay_cases: replayCases } });
}

export function policyBundleStatus(): Promise<PolicyBundleStatus> {
  return request<PolicyBundleStatus>({ method: "GET", url: "/policy-bundles/status" });
}

export function proposePolicyBundle(baseDigest: string, desired: PolicyBundle): Promise<PolicyBundleProposal> {
  return request<PolicyBundleProposal>({ method: "POST", url: "/policy-bundles/proposals", data: { base_digest: baseDigest, desired } });
}

export function getPolicyBundleProposal(id: string): Promise<PolicyBundleProposal> {
  return request<PolicyBundleProposal>({ method: "GET", url: `/policy-bundles/proposals/${encodeURIComponent(id)}` });
}

export function reviewPolicyBundleProposal(id: string, approve: boolean): Promise<PolicyBundleProposal> {
  return request<PolicyBundleProposal>({ method: "POST", url: `/policy-bundles/proposals/${encodeURIComponent(id)}/${approve ? "approve" : "reject"}` });
}

export function applyPolicyBundleProposal(id: string): Promise<PolicyBundleStatus> {
  return request<PolicyBundleStatus>({ method: "POST", url: `/policy-bundles/proposals/${encodeURIComponent(id)}/apply` });
}
