package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cuipengdba/agentsql/internal/policybundle"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/stretchr/testify/require"
)

func TestPolicyBundlePlanIsTenantBoundOfflineAndReadOnly(t *testing.T) {
	fixture := newAdminFixture(t)
	status, _ := fixture.request(http.MethodGet, "/api/v1/policy-bundles/current", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, body := fixture.request(http.MethodGet, "/api/v1/policy-bundles/current", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	var current struct {
		Data policyBundleSnapshot `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &current))
	require.Equal(t, policybundle.SchemaVersion, current.Data.Bundle.SchemaVersion)
	require.NotEmpty(t, current.Data.Digest)
	require.NotEmpty(t, current.Data.Bundle.Policies)

	desired := current.Data.Bundle
	desired.TenantID = "other-tenant"
	encoded, err := json.Marshal(policyBundlePlanInput{BaseDigest: current.Data.Digest, Desired: desired})
	require.NoError(t, err)
	status, _ = fixture.request(http.MethodPost, "/api/v1/policy-bundles/plan", fixture.adminToken, string(encoded))
	require.Equal(t, http.StatusForbidden, status)

	desired.TenantID = current.Data.Bundle.TenantID
	encoded, err = json.Marshal(policyBundlePlanInput{BaseDigest: "sha256:stale", Desired: desired})
	require.NoError(t, err)
	status, _ = fixture.request(http.MethodPost, "/api/v1/policy-bundles/plan", fixture.adminToken, string(encoded))
	require.Equal(t, http.StatusPreconditionFailed, status)

	desired.Policies[0].ObjectName = "public.changed"
	encoded, err = json.Marshal(policyBundlePlanInput{BaseDigest: current.Data.Digest, Desired: desired,
		ReplayCases: []policybundle.ReplayCase{{AgentID: desired.Policies[0].AgentID, DatasourceID: desired.Policies[0].DatasourceID, ObjectName: "public.changed"}}})
	require.NoError(t, err)
	status, body = fixture.request(http.MethodPost, "/api/v1/policy-bundles/plan", fixture.adminToken, string(encoded))
	require.Equal(t, http.StatusOK, status, body)
	var plan struct {
		Data policyBundlePlanView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &plan))
	require.Len(t, plan.Data.Changes, 1)
	require.Equal(t, "update", plan.Data.Changes[0].Operation)
	require.True(t, plan.Data.LiveCheck)
	require.Len(t, plan.Data.Replay, 1)
	require.NotEqual(t, "allow", plan.Data.Replay[0].After, "offline replay cannot promise real execution")
	status, body = fixture.request(http.MethodGet, "/api/v1/policy-bundles/current", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	var after struct {
		Data policyBundleSnapshot `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &after))
	require.Equal(t, current.Data.Digest, after.Data.Digest, "offline plan changed persisted policy")
}

func TestPolicyBundleHTTPReviewAndApplyIsTenantIsolated(t *testing.T) {
	fixture := newAdminFixture(t)
	permissions := []string{rbac.PermissionStrategyManage, rbac.PermissionAuditView}
	maker := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "bundle-maker", permissions)
	reviewer := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "bundle-reviewer", permissions)
	otherTenant := taskGrantTestToken(t, fixture, "bundle-other-tenant", "bundle-other", permissions)
	status, body := fixture.request(http.MethodGet, "/api/v1/policy-bundles/current", maker, "")
	require.Equal(t, http.StatusOK, status, body)
	var current struct {
		Data policyBundleSnapshot `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &current))
	desired := current.Data.Bundle
	desired.Policies = append(desired.Policies, policybundle.Entry{
		ID: "reviewed-allow", AgentID: fixture.agent.ID, DatasourceID: fixture.datasource.ID,
		ObjectType: "table", ObjectName: "public.reports", Action: "allow",
	})
	encoded, err := json.Marshal(policyBundlePlanInput{BaseDigest: current.Data.Digest, Desired: desired})
	require.NoError(t, err)
	status, body = fixture.request(http.MethodPost, "/api/v1/policy-bundles/proposals", maker, string(encoded))
	require.Equal(t, http.StatusOK, status, body)
	var proposal struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &proposal))
	require.NotEmpty(t, proposal.Data.ID)
	path := "/api/v1/policy-bundles/proposals/" + proposal.Data.ID
	status, _ = fixture.request(http.MethodGet, path, otherTenant, "")
	require.Equal(t, http.StatusNotFound, status)
	status, _ = fixture.request(http.MethodPost, path+"/approve", maker, "")
	require.Equal(t, http.StatusForbidden, status)
	status, body = fixture.request(http.MethodPost, path+"/approve", reviewer, "")
	require.Equal(t, http.StatusOK, status, body)
	status, _ = fixture.request(http.MethodPost, path+"/apply", maker, "")
	require.Equal(t, http.StatusForbidden, status)
	status, _ = fixture.request(http.MethodPost, path+"/apply", otherTenant, "")
	require.Equal(t, http.StatusNotFound, status)
	status, body = fixture.request(http.MethodPost, path+"/apply", reviewer, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"state":"approved"`)
	status, body = fixture.request(http.MethodGet, "/api/v1/policy-bundles/status", reviewer, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"state":"approved"`)
	status, body = fixture.request(http.MethodGet, "/api/v1/policy-bundles/current", reviewer, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"id":"reviewed-allow"`)
	require.NotContains(t, body, "database-password")
}
