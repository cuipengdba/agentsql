package b5dml

import "testing"

func TestGrantLatticePriorityAndComposition(t *testing.T) {
	t.Parallel()
	base := authorizationFixture()
	tests := []struct {
		name   string
		mutate func(*AuthorizationInput)
		reason AuthorizationReason
	}{
		{name: "allow-union-across-policies", reason: ReasonAllow},
		{name: "preliminary", mutate: func(in *AuthorizationInput) { in.PreliminaryAllowed = false }, reason: ReasonPreliminaryDenied},
		{name: "dialect", mutate: func(in *AuthorizationInput) { in.Dialect = DialectMySQL }, reason: ReasonDialectUnsupported},
		{name: "reserved", mutate: func(in *AuthorizationInput) { in.ReservedTarget = true }, reason: ReasonReservedTarget},
		{name: "action-deny-absorbs-allow", mutate: func(in *AuthorizationInput) {
			in.Policies = append(in.Policies, denyPolicy("deny-action", actionGrant(*in)))
		}, reason: ReasonActionDenied},
		{name: "action-missing", mutate: func(in *AuthorizationInput) { in.Policies = in.Policies[1:] }, reason: ReasonActionGrantMissing},
		{name: "identity", mutate: func(in *AuthorizationInput) { in.CatalogSnapshotDigest = "" }, reason: ReasonIdentityUnproven},
		{name: "closure", mutate: func(in *AuthorizationInput) { in.ClosureProven = false }, reason: ReasonClosureUnproven},
		{name: "write-deny-absorbs-allow", mutate: func(in *AuthorizationInput) {
			in.Policies = append(in.Policies, denyPolicy("deny-write", grantForWrite(in.Action, in.Writes[0])))
		}, reason: ReasonWriteTargetDenied},
		{name: "write-missing", mutate: func(in *AuthorizationInput) { in.Policies = append(in.Policies[:1], in.Policies[2:]...) }, reason: ReasonWriteTargetGrantMissing},
		{name: "reference-deny-absorbs-allow", mutate: func(in *AuthorizationInput) {
			in.Policies = append(in.Policies, denyPolicy("deny-ref", grantForReference(in.Action, in.References[0])))
		}, reason: ReasonReferenceDenied},
		{name: "reference-missing", mutate: func(in *AuthorizationInput) { in.Policies = in.Policies[:2] }, reason: ReasonReferenceGrantMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Policies = clonePolicies(base.Policies)
			if test.mutate != nil {
				test.mutate(&input)
			}
			decision := Authorize(input)
			if decision.Reason() != test.reason || decision.Allowed() != (test.reason == ReasonAllow) {
				t.Fatalf("decision = allowed=%v reason=%s", decision.Allowed(), decision.Reason())
			}
		})
	}
}

func TestSpecialReferenceFormsAreRepresentedAndRejected(t *testing.T) {
	t.Parallel()
	base := authorizationFixture()
	tests := []struct {
		name       string
		kind       ReferenceKind
		attnum     int16
		systemName string
	}{
		{name: "whole-row", kind: ReferenceWholeRow, attnum: 0},
		{name: "count-table", kind: ReferenceRowCount, attnum: 0},
		{name: "record", kind: ReferenceRecord, attnum: 0},
		{name: "composite", kind: ReferenceComposite, attnum: 0},
		{name: "ctid", kind: ReferenceSystemColumn, attnum: -1, systemName: "ctid"},
		{name: "xmin", kind: ReferenceSystemColumn, attnum: -3, systemName: "xmin"},
		{name: "tableoid", kind: ReferenceSystemColumn, attnum: -6, systemName: "tableoid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			column := base.References[0].Column
			column.Attnum = test.attnum
			input.References = []Reference{{Kind: test.kind, Site: ReferenceExpression,
				Relation: base.Target, Column: column, SystemName: test.systemName}}
			decision := Authorize(input)
			if decision.Reason() != ReasonReferenceFormUnsupported {
				t.Fatalf("reason = %s", decision.Reason())
			}
		})
	}
}

func TestReferenceSitesShareReferenceGrantWithoutBecomingWrites(t *testing.T) {
	t.Parallel()
	base := authorizationFixture()
	sites := []ReferenceSite{
		ReferenceWhere,
		ReferenceJoin,
		ReferenceUsing,
		ReferenceExpression,
		ReferenceSubquery,
		ReferenceForeignKey,
		ReferenceConstraint,
		ReferenceInternalRead,
		ReferenceConflictCheck,
	}
	for _, site := range sites {
		input := base
		input.Policies = clonePolicies(base.Policies)
		input.References = []Reference{ColumnReference(base.References[0].Column, site)}
		if decision := Authorize(input); !decision.Allowed() {
			t.Fatalf("site %d reason = %s", site, decision.Reason())
		}
		if len(input.Writes) != 1 || input.Writes[0].Column != base.Writes[0].Column {
			t.Fatalf("site %d changed write set", site)
		}
	}
}

func TestDeleteUsesExactRowWriteGrant(t *testing.T) {
	t.Parallel()
	input := authorizationFixture()
	input.Action = ActionDelete
	input.Writes = []WriteTarget{RowDelete(input.Target)}
	input.Policies = []Policy{
		allowPolicy("action", actionGrant(input)),
		allowPolicy("row-write", grantForWrite(input.Action, input.Writes[0])),
		allowPolicy("reference", grantForReference(input.Action, input.References[0])),
	}
	if decision := Authorize(input); !decision.Allowed() {
		t.Fatalf("delete reason = %s", decision.Reason())
	}
	input.Policies[1].Grants[0].WriteKind = WriteTargetColumn
	input.Policies[1].Grants[0].Column = input.References[0].Column
	if reason := Authorize(input).Reason(); reason != ReasonWriteTargetGrantMissing {
		t.Fatalf("column grant substituted for row delete: %s", reason)
	}
}

func TestProofDigestBindsPolicyRevisionCatalogIdentityAndFiveMajors(t *testing.T) {
	t.Parallel()
	base := authorizationFixture()
	original := Authorize(base)
	if !original.Allowed() || !original.Verify(base) {
		t.Fatal("fixture was not authorized and verifiable")
	}

	revision := base
	revision.Policies = clonePolicies(base.Policies)
	revision.Policies[0].Revision++
	if Authorize(revision).Digest() == original.Digest() {
		t.Fatal("policy revision did not change proof digest")
	}

	catalog := base
	catalog.Policies = clonePolicies(base.Policies)
	catalog.Target.CatalogFingerprint = "catalog-v2"
	for index := range catalog.Writes {
		catalog.Writes[index].Relation = catalog.Target
		catalog.Writes[index].Column.Relation = catalog.Target
	}
	for index := range catalog.References {
		catalog.References[index].Relation = catalog.Target
		catalog.References[index].Column.Relation = catalog.Target
	}
	for policyIndex := range catalog.Policies {
		catalog.Policies[policyIndex].Grants = append([]Grant(nil), catalog.Policies[policyIndex].Grants...)
		for grantIndex := range catalog.Policies[policyIndex].Grants {
			grant := &catalog.Policies[policyIndex].Grants[grantIndex]
			grant.Relation = catalog.Target
			if grant.Column != (ColumnIdentity{}) {
				grant.Column.Relation = catalog.Target
			}
		}
	}
	if decision := Authorize(catalog); !decision.Allowed() || decision.Digest() == original.Digest() {
		t.Fatalf("catalog-bound decision = allowed=%v digest-equal=%v", decision.Allowed(), decision.Digest() == original.Digest())
	}

	attestation := base
	attestation.Attestations = append([]BinderAttestation(nil), base.Attestations...)
	attestation.Attestations[4].ExtensionHash = "pg18-extension-v2"
	if Authorize(attestation).Digest() == original.Digest() {
		t.Fatal("non-current major attestation did not enter proof digest")
	}

	missingMajor := base
	missingMajor.Attestations = missingMajor.Attestations[:4]
	if got := Authorize(missingMajor).Reason(); got != ReasonIdentityUnproven {
		t.Fatalf("missing major reason = %s", got)
	}
}

func TestCatalogClosedAttestationBindsExactLowServerMajor(t *testing.T) {
	t.Parallel()
	input := authorizationFixture()
	input.CurrentServerMajor = 12
	input.Attestations = []BinderAttestation{{Mode: BinderAttestationCatalogClosed,
		ServerMajor: 12, ABI: BinderABI, BuildHash: "closed-build", ExtensionHash: "closed-grammar",
		NodeManifestHash: "closed-catalog", AllowlistHash: "closed-builtins"}}
	if decision := Authorize(input); !decision.Allowed() || !decision.Verify(input) {
		t.Fatalf("closed PG12 decision = allowed=%v reason=%s", decision.Allowed(), decision.Reason())
	}

	drifted := input
	drifted.Attestations = append([]BinderAttestation(nil), input.Attestations...)
	drifted.Attestations[0].ServerMajor = 10
	if reason := Authorize(drifted).Reason(); reason != ReasonIdentityUnproven {
		t.Fatalf("drifted closed major reason = %s", reason)
	}

	mixed := input
	mixed.Attestations = append(mixed.Attestations, testAttestations()[0])
	if reason := Authorize(mixed).Reason(); reason != ReasonIdentityUnproven {
		t.Fatalf("mixed attestation set reason = %s", reason)
	}
}

func authorizationFixture() AuthorizationInput {
	relation := testRelation("target", 100)
	writeColumn := testColumn(relation, 2, "value")
	referenceColumn := testColumn(relation, 1, "id")
	input := AuthorizationInput{
		PrincipalID: "agent-1", DatasourceID: "ds-1", Dialect: DialectPostgreSQL,
		CurrentServerMajor: 16, Action: ActionUpdate, Target: relation,
		Writes:             []WriteTarget{ColumnWrite(writeColumn, WriteSourceExplicit)},
		References:         []Reference{ColumnReference(referenceColumn, ReferenceWhere)},
		PreliminaryAllowed: true, DatasourceSupported: true, CatalogConsistent: true,
		ClosureProven: true, PolicySnapshotDigest: "policy-snapshot", CatalogSnapshotDigest: "catalog-snapshot",
		ClosureDigest: "closure", PlanDigest: "plan", Attestations: testAttestations(),
	}
	input.Policies = []Policy{
		allowPolicy("action", actionGrant(input)),
		allowPolicy("write", grantForWrite(input.Action, input.Writes[0])),
		allowPolicy("reference", grantForReference(input.Action, input.References[0])),
	}
	return input
}

func actionGrant(input AuthorizationInput) Grant {
	return Grant{Element: GrantAction, Action: input.Action, Relation: input.Target}
}

func allowPolicy(id string, grant Grant) Policy {
	return Policy{ID: id, Revision: 1, PrincipalID: "agent-1", DatasourceID: "ds-1", Effect: GrantAllow, Grants: []Grant{grant}}
}

func denyPolicy(id string, grant Grant) Policy {
	return Policy{ID: id, Revision: 1, PrincipalID: "agent-1", DatasourceID: "ds-1", Effect: GrantDeny, Grants: []Grant{grant}}
}

func clonePolicies(values []Policy) []Policy {
	result := append([]Policy(nil), values...)
	for index := range result {
		result[index].Grants = append([]Grant(nil), result[index].Grants...)
	}
	return result
}

func testRelation(name string, oid uint32) RelationIdentity {
	return RelationIdentity{DatasourceID: "ds-1", DatabaseOID: 9, RelationOID: oid,
		RelationKind: 'r', Schema: "public", Name: name, CatalogFingerprint: "catalog-v1"}
}

func testColumn(relation RelationIdentity, attnum int16, name string) ColumnIdentity {
	return ColumnIdentity{Relation: relation, Attnum: attnum, Name: name, TypeOID: 25, TypeModifier: -1}
}

func testAttestations() []BinderAttestation {
	result := make([]BinderAttestation, 0, 5)
	for major := 14; major <= 18; major++ {
		result = append(result, BinderAttestation{Mode: BinderAttestationNative, ServerMajor: major, ABI: BinderABI,
			BuildHash: "build-" + string(rune('A'+major-14)), ExtensionHash: "extension",
			NodeManifestHash: "nodes", AllowlistHash: "allowlist"})
	}
	return result
}
