package adminapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policybundle"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSignedPolicyTransferAcrossIndependentManagementEnvironments(t *testing.T) {
	source := newAdminFixture(t)
	target := newAdminFixture(t)
	status, body := source.request(http.MethodGet, "/api/v1/policy-bundles/current", source.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	var sourceSnapshot struct {
		Data policyBundleSnapshot `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &sourceSnapshot))
	require.Len(t, sourceSnapshot.Data.Bundle.Policies, 1)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	signed, err := policybundle.SignTransfer(sourceSnapshot.Data.Bundle, "source-environment", now, private)
	require.NoError(t, err)
	_, err = target.store.Agents().Create(context.Background(), model.Agent{
		ID: "target-agent", Name: "Mapped Agent", Status: "active", APIKeyHash: store.HashAPIKey("asql_target_transfer"), Level: "readonly",
	})
	require.NoError(t, err)
	_, err = target.store.Datasources().Create(context.Background(), model.Datasource{
		ID: "target-database", Name: "Mapped DB", DBType: "postgres", Host: "target-db.invalid",
		Port: 5432, Database: "app", Username: "reader", ConnLimit: 3, StmtTimeoutMS: 5000, RowLimit: 100,
	}, "synthetic-password")
	require.NoError(t, err)
	permissions := []string{rbac.PermissionStrategyManage, rbac.PermissionAuditView}
	maker := taskGrantTestToken(t, target, rbac.DefaultTenantID, "transfer-maker", permissions)
	reviewer := taskGrantTestToken(t, target, rbac.DefaultTenantID, "transfer-reviewer", permissions)
	status, body = target.request(http.MethodGet, "/api/v1/policy-bundles/current", maker, "")
	require.Equal(t, http.StatusOK, status, body)
	var targetSnapshot struct {
		Data policyBundleSnapshot `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &targetSnapshot))
	mapping := policybundle.TransferMapping{
		TargetTenant: targetSnapshot.Data.Bundle.TenantID,
		Agents:       map[string]string{source.agent.ID: "target-agent"},
		Datasources:  map[string]string{source.datasource.ID: "target-database"},
		Policies:     map[string]string{sourceSnapshot.Data.Bundle.Policies[0].ID: "transferred-allow"},
		Objects:      map[string]string{sourceSnapshot.Data.Bundle.Policies[0].ID: "public.mapped_orders"},
	}
	desired, changes, err := policybundle.PlanTransfer(signed, public, now.Add(time.Minute), targetSnapshot.Data.Bundle, mapping)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "target-agent", desired.Policies[len(desired.Policies)-1].AgentID)
	input := policyBundlePlanInput{BaseDigest: targetSnapshot.Data.Digest, Desired: desired}
	status, body = target.request(http.MethodPost, "/api/v1/policy-bundles/plan", maker, mustJSON(t, input))
	require.Equal(t, http.StatusOK, status, body)
	status, body = target.request(http.MethodPost, "/api/v1/policy-bundles/proposals", maker, mustJSON(t, input))
	require.Equal(t, http.StatusOK, status, body)
	var proposal struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &proposal))
	require.NotEmpty(t, proposal.Data.ID)
	path := "/api/v1/policy-bundles/proposals/" + proposal.Data.ID
	status, _ = target.request(http.MethodPost, path+"/approve", maker, "")
	require.Equal(t, http.StatusForbidden, status)
	status, body = target.request(http.MethodPost, path+"/approve", reviewer, "")
	require.Equal(t, http.StatusOK, status, body)
	status, body = target.request(http.MethodPost, path+"/apply", reviewer, "")
	require.Equal(t, http.StatusOK, status, body)
	status, body = target.request(http.MethodGet, "/api/v1/policy-bundles/current", reviewer, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"id":"transferred-allow"`)
	require.Contains(t, body, `"agent_id":"target-agent"`)
	require.Contains(t, body, `"datasource_id":"target-database"`)
	status, body = source.request(http.MethodGet, "/api/v1/policy-bundles/current", source.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.NotContains(t, body, "transferred-allow", "target apply must not change source environment")
}
