package columnauth

import (
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAuthorizeFrozenPriorityAndProof(t *testing.T) {
	base := authorizedInputFixture()
	requireDecision(t, base, true, ReasonAllow)

	tests := []struct {
		name   string
		mutate func(*Input)
		want   Reason
	}{
		{"agent before relation", func(input *Input) {
			input.Agent.Status = "revoked"
			input.Policies = append(input.Policies, model.Policy{ObjectType: "table", ObjectName: "*", Action: "deny"})
		}, ReasonAgentDenied},
		{"wide deny beats exact allow", func(input *Input) {
			input.Policies = append(input.Policies, model.Policy{ObjectType: "schema", ObjectName: "public.*", Action: "deny"})
		}, ReasonRelationDenied},
		{"missing relation before catalog", func(input *Input) {
			input.Policies = nil
			input.CatalogConsistent = false
		}, ReasonRelationGrantMissing},
		{"identity before column", func(input *Input) {
			input.CatalogConsistent = false
			input.Policies[1].ColumnPermissions = nil
		}, ReasonIdentityUnproven},
		{"persistent column deny is invalid metadata", func(input *Input) {
			invalid := input.Policies[1]
			invalid.ID = "invalid-deny"
			invalid.Action = "deny"
			bindingID := "invalid-deny"
			invalid.RelationBindingID = &bindingID
			invalid.RelationBinding.ID = bindingID
			invalid.RelationBinding.PolicyID = bindingID
			input.Policies = append(input.Policies, invalid)
		}, ReasonIdentityUnproven},
		{"column before mask", func(input *Input) {
			input.Policies[1].ColumnPermissions = input.Policies[1].ColumnPermissions[:1]
			input.Masks = []MaskCandidate{{OutputIndex: 0, Column: input.Uses[0].Column, Identity: MaskIdentity{AlgorithmID: "hash", InputType: "wrong"}, Capable: false}}
		}, ReasonColumnGrantMissing},
		{"mask capability", func(input *Input) {
			input.Masks[0].Capable = false
		}, ReasonMaskCapabilityMissing},
		{"mask identity mismatch", func(input *Input) {
			other := input.Masks[0]
			other.Identity.KeyVersion++
			input.Masks = append(input.Masks, other)
		}, ReasonMaskMeetUndefined},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := authorizedInputFixture()
			test.mutate(&input)
			requireDecision(t, input, false, test.want)
		})
	}

	plan := Authorize(base)
	require.True(t, plan.Verify(base))
	changed := base
	changed.Nonce = []byte("different-private-request-nonce")
	require.False(t, plan.Verify(changed))
	changed = base
	changed.Policies = append([]model.Policy(nil), base.Policies...)
	changed.Policies[1].Revision++
	require.False(t, plan.Verify(changed))
	changed = base
	changed.Masks = append([]MaskCandidate(nil), base.Masks...)
	changed.Masks[0].Capable = false
	require.False(t, plan.Verify(changed))

	otherAgent := base
	otherAgent.Policies = append(otherAgent.Policies, model.Policy{AgentID: "other", DatasourceID: "ds", ObjectType: "schema", ObjectName: "public.*", Action: "deny"})
	requireDecision(t, otherAgent, true, ReasonAllow)
}

func TestAuthorizeViewAndSelfJoinRequireEveryIdentityUsage(t *testing.T) {
	input := authorizedInputFixture()
	view := RelationIdentity{DatabaseID: "1", StableObjectID: "pg:1:12", Schema: "public", Name: "customer_view", CatalogFingerprint: "catalog"}
	input.Relations = append(input.Relations, view)
	viewColumn := ColumnIdentity{Relation: view, Ordinal: 1, Name: "phone", TypeDigest: "text"}
	input.Uses = append(input.Uses, BoundColumnUse{Column: viewColumn, Usage: UsageOutput, Site: "target.1", OutputIndex: 0, ViewPath: "root.view"})
	input.Policies = append(input.Policies, boundPolicy("view", view, nil))
	requireDecision(t, input, false, ReasonColumnGrantMissing)

	input.Policies[len(input.Policies)-1].ColumnPermissions = []model.PolicyColumnPermission{
		permission("view", 1, "phone", "text", UsageOutput, 2),
	}
	requireDecision(t, input, true, ReasonAllow)

	// Two aliases of one physical relation retain one identity but two bound
	// sites; removing the reference usage grant still denies the self join.
	input.Uses = append(input.Uses, BoundColumnUse{Column: input.Uses[0].Column, Usage: UsageReference, Site: "join_where", BindingAlias: "right"})
	input.Policies[1].ColumnPermissions = input.Policies[1].ColumnPermissions[:1]
	requireDecision(t, input, false, ReasonColumnGrantMissing)
}

func authorizedInputFixture() Input {
	relation := RelationIdentity{DatabaseID: "1", StableObjectID: "pg:1:11", Schema: "public", Name: "customers", CatalogFingerprint: "catalog"}
	column := ColumnIdentity{Relation: relation, Ordinal: 2, Name: "phone", TypeDigest: "text"}
	identity := MaskIdentity{AlgorithmID: "mask", SemanticVersion: "1", CanonicalParameters: "type=phone", InputType: "text", OutputType: "text"}
	return Input{
		Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, DatasourceID: "ds", Statement: model.StmtType("SELECT"),
		PreliminaryAllowed: true, DatasourceSupported: true, CatalogConsistent: true,
		BinderDigest: "binder", CatalogDigest: "catalog", ControlRevisionDigest: "control",
		Relations: []RelationIdentity{relation},
		Uses: []BoundColumnUse{
			{Column: column, Usage: UsageOutput, Site: "target.1", OutputIndex: 0},
			{Column: column, Usage: UsageReference, Site: "join_where", OutputIndex: -1},
		},
		Policies: []model.Policy{
			{ID: "broad", ObjectType: "schema", ObjectName: "public.*", Action: "allow", Revision: 1},
			boundPolicy("exact", relation, []model.PolicyColumnPermission{
				permission("exact", 2, "phone", "text", UsageOutput, 2),
				permission("exact", 2, "phone", "text", UsageReference, 2),
			}),
		},
		Masks: []MaskCandidate{{OutputIndex: 0, Column: column, Identity: identity, Capable: true}},
		Nonce: []byte("0123456789abcdef0123456789abcdef"), Now: time.Unix(10, 0),
	}
}

func boundPolicy(id string, relation RelationIdentity, permissions []model.PolicyColumnPermission) model.Policy {
	stable, catalog := relation.StableObjectID, relation.CatalogFingerprint
	bindingID := id
	return model.Policy{
		ID: id, DatasourceID: "ds", ObjectType: "column", ObjectName: relation.Schema + "." + relation.Name,
		Action: "allow", Revision: 2, RelationBindingID: &bindingID,
		RelationBinding: &model.RelationPolicyBinding{ID: id, PolicyID: id, DatasourceID: "ds", SchemaName: relation.Schema,
			RelationName: relation.Name, StableObjectID: &stable, CatalogFingerprint: &catalog, Status: "healthy", Revision: 1},
		ColumnPermissions: permissions,
	}
}

func permission(policy string, ordinal int, name, typeDigest string, usage Usage, revision int64) model.PolicyColumnPermission {
	return model.PolicyColumnPermission{PolicyID: policy, RelationEnrollmentID: policy, ColumnOrdinal: ordinal,
		ColumnName: name, ColumnTypeDigest: typeDigest, Usage: string(usage), ParentRevision: revision}
}

func requireDecision(t *testing.T, input Input, allowed bool, reason Reason) {
	t.Helper()
	plan := Authorize(input)
	require.Equal(t, allowed, plan.Allowed())
	require.Equal(t, reason, plan.Reason())
}
