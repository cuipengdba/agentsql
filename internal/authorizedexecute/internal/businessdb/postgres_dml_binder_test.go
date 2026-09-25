package businessdb

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/b5dml"
)

func TestPostgresDMLFactsPreserveWriteReferenceSplit(t *testing.T) {
	manifest, frame := postgresDMLUnitFixture()
	facts, err := postgresDMLFacts(manifest, frame, "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Action != b5dml.ActionInsert || facts.Shape != b5dml.ShapeSimple || facts.Target.RelationOID != 42 {
		t.Fatalf("facts identity = %+v", facts)
	}
	if len(facts.Writes) != 3 || len(facts.InsertColumns) != 3 {
		t.Fatalf("writes=%+v insert=%+v", facts.Writes, facts.InsertColumns)
	}
	writes := map[int16]b5dml.WriteSource{}
	for _, write := range facts.Writes {
		writes[write.Column.Attnum] = write.Source
	}
	if writes[1] != b5dml.WriteSourceExplicit || writes[2] != b5dml.WriteSourceImplicitNull || writes[3] != b5dml.WriteSourceImplicitNull {
		t.Fatalf("write sources = %+v", writes)
	}
	if len(facts.References) != 1 || facts.References[0].Column.Attnum != 1 || facts.References[0].Site != b5dml.ReferenceWhere {
		t.Fatalf("references = %+v", facts.References)
	}
	decision := b5dml.AnalyzeStatement(facts)
	if !decision.Allowed || len(decision.Writes) != 3 {
		t.Fatalf("statement decision = %+v", decision)
	}
}

func TestPostgresDMLFactsRepresentUnsupportedSystemReference(t *testing.T) {
	manifest, frame := postgresDMLUnitFixture()
	manifest.CommandType = "UPDATE"
	manifest.Columns = append(manifest.Columns, PostgresColumnUse{
		Site: "join_where", RelationOID: 42, Attnum: -1, TypeOID: 27,
		Usage: "reference", ContributorComplete: true,
	})
	facts, err := postgresDMLFacts(manifest, frame, "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	last := facts.References[len(facts.References)-1]
	if last.Kind != b5dml.ReferenceSystemColumn || last.SystemName != "ctid" {
		t.Fatalf("system reference = %+v", last)
	}
}

func TestPostgresDMLFiveMajorAttestationMatchesCurrentCapability(t *testing.T) {
	values := postgresDMLUnitAttestations()
	capability := PostgresBinderCapability{
		ABI: b5dml.BinderABI, ServerMajor: 16,
		BuildHash: values[2].BuildHash, ExtensionHash: values[2].ExtensionHash,
		NodeManifestHash: values[2].NodeManifestHash, AllowlistHash: values[2].AllowlistHash,
	}
	if err := verifyDMLAttestations(capability, values); err != nil {
		t.Fatal(err)
	}
	values[4].AllowlistHash = "tampered"
	if err := verifyDMLAttestations(capability, values); err == nil {
		t.Fatal("non-current major artifact mismatch allowed")
	}
	values = postgresDMLUnitAttestations()
	values[2].BuildHash = "tampered"
	if err := verifyDMLAttestations(capability, values); err == nil {
		t.Fatal("current-major artifact mismatch allowed")
	}
}

func TestPostgresDMLAuthorizationUsesExactBinderFacts(t *testing.T) {
	manifest, frame := postgresDMLUnitFixture()
	facts, err := postgresDMLFacts(manifest, frame, "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	grants := []b5dml.Grant{{Element: b5dml.GrantAction, Action: facts.Action, Relation: facts.Target}}
	for _, write := range facts.Writes {
		grants = append(grants, b5dml.Grant{Element: b5dml.GrantWriteTarget, Action: facts.Action, Relation: write.Relation, WriteKind: write.Kind, Column: write.Column})
	}
	for _, reference := range facts.References {
		grants = append(grants, b5dml.Grant{Element: b5dml.GrantReference, Action: facts.Action, Relation: reference.Relation, Column: reference.Column, ReferenceKind: reference.Kind})
	}
	input := b5dml.AuthorizationInput{
		PrincipalID: "principal", DatasourceID: "ds-1", Dialect: b5dml.DialectPostgreSQL,
		CurrentServerMajor: 16, Action: facts.Action, Target: facts.Target,
		Writes: facts.Writes, References: facts.References,
		Policies:           []b5dml.Policy{{ID: "policy", Revision: 9, PrincipalID: "principal", DatasourceID: "ds-1", Effect: b5dml.GrantAllow, Grants: grants}},
		PreliminaryAllowed: true, DatasourceSupported: true, CatalogConsistent: true, ClosureProven: true,
		PolicySnapshotDigest: "policy-revision-9", CatalogSnapshotDigest: frame.Fingerprint,
		ClosureDigest: "closure", PlanDigest: "plan", Attestations: postgresDMLUnitAttestations(),
	}
	allowed := b5dml.Authorize(input)
	if !allowed.Allowed() || allowed.Digest() == "" {
		t.Fatalf("allowed = reason=%s digest=%s", allowed.Reason(), allowed.Digest())
	}
	input.Policies[0].Grants = input.Policies[0].Grants[:len(input.Policies[0].Grants)-1]
	denied := b5dml.Authorize(input)
	if denied.Allowed() || denied.Reason() != b5dml.ReasonReferenceGrantMissing || denied.Digest() == allowed.Digest() {
		t.Fatalf("missing reference decision = reason=%s digest=%s", denied.Reason(), denied.Digest())
	}
}

func TestPostgresDMLEnrollmentBindsIdentityAndClosure(t *testing.T) {
	manifest, frame := postgresDMLUnitFixture()
	facts, err := postgresDMLFacts(manifest, frame, "ds-1")
	if err != nil {
		t.Fatal(err)
	}
	first := dmlEnrollmentFrom(manifest, frame, facts)
	manifest.SearchPath = "pg_catalog"
	second := dmlEnrollmentFrom(manifest, frame, facts)
	if first.ClosureDigest != second.ClosureDigest || first.Fingerprint == second.Fingerprint {
		t.Fatalf("closure/identity binding first=%+v second=%+v", first, second)
	}
	manifest = first.Manifest
	manifest.Capability.NodeManifestHash = "changed"
	third := dmlEnrollmentFrom(manifest, frame, facts)
	if first.ClosureDigest == third.ClosureDigest {
		t.Fatal("binder artifact change did not alter closure digest")
	}
}

func postgresDMLUnitFixture() (PostgresDMLManifest, PostgresCatalogFrame) {
	attestation := postgresDMLBinderAttestation(16)
	capability := PostgresBinderCapability{ABI: b5dml.BinderABI, ServerMajor: 16, BuildHash: attestation.BuildHash, ExtensionHash: attestation.ExtensionHash, NodeManifestHash: attestation.NodeManifestHash, AllowlistHash: attestation.AllowlistHash}
	manifest := PostgresDMLManifest{
		PostgresPreparedManifest: PostgresPreparedManifest{
			StatementName: "agentsql_unit", BackendPID: 10, TransactionID: "1:1",
			RoleOID: 11, RoleName: "role", SearchPath: `"$user", public`,
			AnalyzedDigest: "analyzed", DependencyDigest: "dependencies", CommandType: "INSERT",
			Relations: []PostgresBoundRelation{{OID: 42, Kind: 'r'}}, Capability: capability,
			Columns: []PostgresColumnUse{
				{Site: "write.explicit", RelationOID: 42, Attnum: 1, TypeOID: 23, Usage: "write_target", ContributorComplete: true},
				{Site: "write.implicit_null", RelationOID: 42, Attnum: 2, TypeOID: 25, Usage: "write_target", ContributorComplete: true},
				{Site: "write.implicit_null", RelationOID: 42, Attnum: 3, TypeOID: 25, Usage: "write_target", ContributorComplete: true},
				{Site: "join_where", RelationOID: 42, Attnum: 1, TypeOID: 23, Usage: "reference", ContributorComplete: true},
			},
		},
		TargetRelationOID: 42, Shape: b5dml.ShapeSimple,
	}
	frame := PostgresCatalogFrame{
		ServerVersion: 160000, DatabaseOID: 7, Fingerprint: "catalog",
		Relations: []PostgresRelationIdentity{{DatabaseOID: 7, OID: 42, Schema: "app", Name: "items", Kind: 'r', Persistence: 'p'}},
		Columns: []PostgresColumnIdentity{
			{RelationOID: 42, Attnum: 1, Name: "id", TypeOID: 23, Typmod: -1},
			{RelationOID: 42, Attnum: 2, Name: "value", TypeOID: 25, Typmod: -1, Collation: 100},
			{RelationOID: 42, Attnum: 3, Name: "note", TypeOID: 25, Typmod: -1, Collation: 100},
		},
	}
	return manifest, frame
}

func postgresDMLUnitAttestations() []b5dml.BinderAttestation {
	return PostgresDMLBinderAttestations()
}
