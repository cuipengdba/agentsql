package businessdb

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/columnauth"
	"github.com/cuipengdba/agentsql/internal/model"
)

func TestSemanticFactsCanonicalAndDivergence(t *testing.T) {
	t.Parallel()
	left := semanticSelectFixture()
	right := cloneSemanticFacts(left)
	right.Relations[0], right.Relations[1] = right.Relations[1], right.Relations[0]
	right.ColumnUses[0], right.ColumnUses[1] = right.ColumnUses[1], right.ColumnUses[0]
	a, err := left.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := right.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical digest depends on order: %s != %s", a, b)
	}
	if err := CompareSemanticFacts(left, right); err != nil {
		t.Fatal(err)
	}
	right.ColumnUses[0].Site = "divergent-site"
	err = CompareSemanticFacts(left, right)
	var binderErr *BinderError
	if !errors.As(err, &binderErr) || binderErr.Class != BinderFailureDivergence || binderErr.Code != BinderCodeDivergence {
		t.Fatalf("divergence error = %#v", err)
	}
}

func TestCapabilityAndProofFailClosed(t *testing.T) {
	t.Parallel()
	unknown := CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion, Mode: "future"}
	if _, err := unknown.CanonicalDigest(); err == nil {
		t.Fatal("unknown mode accepted")
	}
	capability := closedCapability(160005, 9)
	facts := semanticSelectFixture()
	facts.Relations = facts.Relations[:1]
	digest, err := facts.Digest()
	if err != nil {
		t.Fatal(err)
	}
	program := BoundProgram{Mode: BinderModeCatalogClosedV1, Facts: facts, SemanticFactsDigest: digest, Capability: capability}
	locks := []uint32{200, 100}
	pre := PreSeal{Program: program, CatalogPreDigest: "catalog-v1", ActualLockDigest: digestOIDs(locks), IdentityDigest: "identity"}
	proof, err := NewFinalBinderProof(pre, PostgresCatalogFrame{Fingerprint: "catalog-v1"}, []uint32{100, 200})
	if err != nil || proof.AttestationDigest == "" {
		t.Fatalf("proof=%#v err=%v", proof, err)
	}
	if _, err = NewFinalBinderProof(pre, PostgresCatalogFrame{Fingerprint: "catalog-v2"}, locks); err == nil {
		t.Fatal("catalog drift accepted")
	}
	if _, err = NewFinalBinderProof(pre, PostgresCatalogFrame{Fingerprint: "catalog-v1"}, []uint32{100}); err == nil {
		t.Fatal("lock drift accepted")
	}
}

func TestSharedFactsReuseB2SevenStageAuthorizer(t *testing.T) {
	t.Parallel()
	facts := semanticSelectFixture()
	facts.Relations = facts.Relations[:1]
	stable := postgresStableObjectID(9, 100)
	catalog := "catalog-v1"
	bindingID := "binding-1"
	policy := model.Policy{ID: "policy-1", AgentID: "agent-1", DatasourceID: "ds-1", ObjectType: "table", ObjectName: "public.a", Action: "allow", RelationBindingID: &bindingID, Revision: 1,
		RelationBinding: &model.RelationPolicyBinding{ID: bindingID, PolicyID: "policy-1", DatasourceID: "ds-1", SchemaName: "public", RelationName: "a", StableObjectID: &stable, CatalogFingerprint: &catalog, Status: "healthy", Revision: 1}}
	for _, usage := range []string{"output", "reference"} {
		policy.ColumnPermissions = append(policy.ColumnPermissions, model.PolicyColumnPermission{PolicyID: "policy-1", RelationEnrollmentID: bindingID, ColumnOrdinal: 1, ColumnName: "id", ColumnTypeDigest: postgresColumnTypeDigest(23, -1, 0), Usage: usage, ParentRevision: 1})
	}
	base := columnauth.Input{Agent: model.Agent{ID: "agent-1", Status: "active", Level: "readonly"}, DatasourceID: "ds-1", Statement: model.StmtType("SELECT"), PreliminaryAllowed: true, DatasourceSupported: true, CatalogConsistent: true, ControlRevisionDigest: "control", Policies: []model.Policy{policy}, Nonce: []byte("0123456789abcdef"), Now: time.Unix(1, 0)}
	closed, err := AuthorizeB2(facts, base)
	if err != nil {
		t.Fatal(err)
	}
	native, err := AuthorizeB2(cloneSemanticFacts(facts), base)
	if err != nil {
		t.Fatal(err)
	}
	if !closed.Allowed() || closed.Reason() != native.Reason() || closed.Digest() != native.Digest() {
		t.Fatalf("closed=%s native=%s", closed.Reason(), native.Reason())
	}
}

func TestSharedFactsReuseB5GrantLattice(t *testing.T) {
	t.Parallel()
	facts := semanticUpdateFixture()
	relation := b5dml.RelationIdentity{DatasourceID: "ds-1", DatabaseOID: 9, RelationOID: 100, RelationKind: 'r', Schema: "public", Name: "a", CatalogFingerprint: "catalog-v1"}
	writeColumn := b5dml.ColumnIdentity{Relation: relation, Attnum: 2, Name: "value", TypeOID: 25, TypeModifier: -1}
	refColumn := b5dml.ColumnIdentity{Relation: relation, Attnum: 1, Name: "id", TypeOID: 23, TypeModifier: -1}
	actionGrant := b5dml.Grant{Element: b5dml.GrantAction, Action: b5dml.ActionUpdate, Relation: relation}
	writeGrant := b5dml.Grant{Element: b5dml.GrantWriteTarget, Action: b5dml.ActionUpdate, Relation: relation, WriteKind: b5dml.WriteTargetColumn, Column: writeColumn}
	refGrant := b5dml.Grant{Element: b5dml.GrantReference, Action: b5dml.ActionUpdate, Relation: relation, ReferenceKind: b5dml.ReferenceColumn, Column: refColumn}
	base := b5dml.AuthorizationInput{PrincipalID: "agent-1", DatasourceID: "ds-1", Dialect: b5dml.DialectPostgreSQL, CurrentServerMajor: 16,
		PreliminaryAllowed: true, DatasourceSupported: true, CatalogConsistent: true, ClosureProven: true, PolicySnapshotDigest: "policy", ClosureDigest: "closure", Attestations: testDualModeAttestations(),
		Policies: []b5dml.Policy{{ID: "action", Revision: 1, PrincipalID: "agent-1", DatasourceID: "ds-1", Effect: b5dml.GrantAllow, Grants: []b5dml.Grant{actionGrant}},
			{ID: "write", Revision: 1, PrincipalID: "agent-1", DatasourceID: "ds-1", Effect: b5dml.GrantAllow, Grants: []b5dml.Grant{writeGrant}},
			{ID: "reference", Revision: 1, PrincipalID: "agent-1", DatasourceID: "ds-1", Effect: b5dml.GrantAllow, Grants: []b5dml.Grant{refGrant}}}}
	closed, err := AuthorizeB5(facts, base)
	if err != nil {
		t.Fatal(err)
	}
	native, err := AuthorizeB5(cloneSemanticFacts(facts), base)
	if err != nil {
		t.Fatal(err)
	}
	if !closed.Allowed() || closed.Reason() != native.Reason() || closed.Digest() != native.Digest() {
		t.Fatalf("closed=%s native=%s", closed.Reason(), native.Reason())
	}
}

func TestLegacyFallbackIsOnlyPreclassificationAndNeverDML(t *testing.T) {
	t.Parallel()
	base := LegacyFallbackRequest{BeforeRequestClassification: true, LegacyTableProfileRequested: true, ControlProofComplete: true}
	if !LegacyFallbackAllowed(base) {
		t.Fatal("eligible legacy request rejected")
	}
	for _, mutate := range []func(*LegacyFallbackRequest){func(v *LegacyFallbackRequest) { v.BeforeRequestClassification = false }, func(v *LegacyFallbackRequest) { v.LegacyTableProfileRequested = false }, func(v *LegacyFallbackRequest) { v.ControlProofComplete = false }, func(v *LegacyFallbackRequest) { v.ActiveColumnPolicyCount = 1 }, func(v *LegacyFallbackRequest) { v.DML = true }} {
		value := base
		mutate(&value)
		if LegacyFallbackAllowed(value) {
			t.Fatalf("unsafe fallback accepted: %#v", value)
		}
	}
}

func TestClosedCacheIsVersionedClonedAndCatalogBound(t *testing.T) {
	t.Parallel()
	cache := NewClosedASTCache(1)
	candidate := ClosedCatalogCandidate{Digest: "catalog-v1", Refs: []ClosedRelationRef{{Schema: "public", Name: "a"}}, Frame: PostgresCatalogFrame{Fingerprint: "catalog-v1"}}
	key := ClosedCacheKey{DatasourceIdentity: "endpoint", DatabaseOID: 9, ServerMajor: 16, RoleOID: 10, SearchPathDigest: "path", SQLDigest: "sql", CapabilityDigest: "cap", CatalogFingerprint: "catalog-v1"}
	if !cache.Put(key, ClosedASTCacheEntry{ASTDigest: "ast", Candidate: candidate, Facts: semanticSelectFixture()}) {
		t.Fatal("cache put failed")
	}
	got, ok := cache.Get(key)
	if !ok || got.Version != closedCacheVersion {
		t.Fatal("cache miss")
	}
	got.Candidate.Refs[0].Name = "mutated"
	again, _ := cache.Get(key)
	if again.Candidate.Refs[0].Name != "a" {
		t.Fatal("cache alias escaped")
	}
	aba := key
	aba.CatalogFingerprint = "catalog-v2"
	if _, ok := cache.Get(aba); ok {
		t.Fatal("ABA catalog fingerprint hit cache")
	}
	cache.InvalidateCatalog("catalog-v1")
	if _, ok := cache.Get(key); ok {
		t.Fatal("catalog invalidation failed")
	}
}

func TestClosedCacheConcurrentAccess(t *testing.T) {
	cache := NewClosedASTCache(8)
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				fingerprint := "catalog-" + strconv.Itoa((worker+iteration)%4)
				key := ClosedCacheKey{DatasourceIdentity: "endpoint", DatabaseOID: 9, ServerMajor: 16, RoleOID: uint32(worker + 1), SearchPathDigest: "path", SQLDigest: "sql", CapabilityDigest: "cap", CatalogFingerprint: fingerprint}
				candidate := ClosedCatalogCandidate{Digest: fingerprint, Frame: PostgresCatalogFrame{Fingerprint: fingerprint}}
				cache.Put(key, ClosedASTCacheEntry{ASTDigest: "ast", Candidate: candidate})
				cache.Get(key)
				if iteration%17 == 0 {
					cache.InvalidateCatalog(fingerprint)
				}
			}
		}(worker)
	}
	workers.Wait()
}

func TestNativeCapabilityRequiresExactABIAndHashes(t *testing.T) {
	t.Parallel()
	expected, ok := PostgresBinderNativeExpectation(16)
	if !ok {
		t.Fatal("PG16 expectation unavailable")
	}
	value := PostgresBinderCapability{ABI: expected.ABI, ServerMajor: expected.ServerMajor, ExtensionVersion: expected.ExtensionVersion,
		BuildHash: expected.BuildHash, ExtensionHash: expected.ExtensionHash, NodeManifestHash: expected.NodeManifestHash, AllowlistHash: expected.AllowlistHash, Matview: true}
	if !nativeCapabilityMatches(value, expected) {
		t.Fatal("exact native capability rejected")
	}
	for _, mutate := range []func(*NativeCapabilityExpectation){func(v *NativeCapabilityExpectation) { v.ABI = "wrong" }, func(v *NativeCapabilityExpectation) { v.ServerMajor = 17 }, func(v *NativeCapabilityExpectation) { v.ExtensionVersion = "wrong" }, func(v *NativeCapabilityExpectation) { v.BuildHash = "wrong" }, func(v *NativeCapabilityExpectation) { v.ExtensionHash = "wrong" }, func(v *NativeCapabilityExpectation) { v.NodeManifestHash = "wrong" }, func(v *NativeCapabilityExpectation) { v.AllowlistHash = "wrong" }} {
		candidate := expected
		mutate(&candidate)
		if nativeCapabilityMatches(value, candidate) {
			t.Fatalf("native mismatch accepted: %#v", candidate)
		}
	}
}

func TestNativeCAdapterProducesUnifiedFactsAndSeparateEvidence(t *testing.T) {
	t.Parallel()
	expected, _ := PostgresBinderNativeExpectation(16)
	capability := PostgresBinderCapability{ABI: expected.ABI, ServerMajor: expected.ServerMajor, ExtensionVersion: expected.ExtensionVersion, BuildHash: expected.BuildHash, ExtensionHash: expected.ExtensionHash, NodeManifestHash: expected.NodeManifestHash, AllowlistHash: expected.AllowlistHash, Matview: true}
	manifest := PostgresPreparedManifest{StatementName: "agentsql_test", RoleOID: 10, RoleName: "agent", SearchPath: "pg_catalog", AnalyzedDigest: "analyzed", DependencyDigest: "dependencies", PlanGeneration: 1, CommandType: "SELECT", Capability: capability,
		Relations: []PostgresBoundRelation{{OID: 100, Kind: 'r', Path: "a"}}, Columns: []PostgresColumnUse{{Site: "target", RelationOID: 100, Attnum: 1, TypeOID: 23, Usage: "output", ContributorGroup: 1, ContributorComplete: true}, {Site: "where", RelationOID: 100, Attnum: 1, TypeOID: 23, Usage: "reference", ContributorComplete: true}}}
	frame := PostgresCatalogFrame{ServerVersion: 160005, DatabaseOID: 9, Fingerprint: "catalog-v1", Relations: []PostgresRelationIdentity{{DatabaseOID: 9, OID: 100, NamespaceOID: 11, Schema: "public", Name: "a", Kind: 'r', Persistence: 'p'}}, Columns: []PostgresColumnIdentity{{RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 23, Typmod: -1}}}
	facts, err := NativeSelectSemanticFacts("ds-1", SemanticIdentity{DatasourceIdentity: "endpoint", DatabaseOID: 9, SessionUser: "agent", CurrentUser: "agent", RoleOID: 10, FixedSearchPath: "pg_catalog"}, manifest, frame)
	if err != nil {
		t.Fatal(err)
	}
	if facts.StatementClass != BinderStatementSelect || facts.Identity.PlanGeneration != 1 || len(facts.ColumnUses) != 2 || facts.ColumnUses[0].OutputIndex != 0 {
		t.Fatalf("facts=%#v", facts)
	}
	program, err := NativeBoundProgram(facts, manifest, frame)
	if err != nil {
		t.Fatal(err)
	}
	if program.Mode != BinderModeNativeCV1 || program.SemanticFactsDigest == "" || program.EngineEvidenceDigest == "" || program.SemanticFactsDigest == program.EngineEvidenceDigest {
		t.Fatalf("program=%#v", program)
	}
	manifest.AnalyzedDigest = "different-analyzed-evidence"
	other, err := NativeBoundProgram(facts, manifest, frame)
	if err != nil {
		t.Fatal(err)
	}
	if other.SemanticFactsDigest != program.SemanticFactsDigest || other.EngineEvidenceDigest == program.EngineEvidenceDigest {
		t.Fatal("common facts and native evidence were not separated")
	}
}

func TestCanonicalizeClosedFactsDatasourceIsOptionalButFingerprintIsRequired(t *testing.T) {
	t.Parallel()
	identity := SemanticIdentity{DatabaseOID: 9, SessionUser: "agent", CurrentUser: "agent", RoleOID: 10,
		FixedSearchPath: "pg_catalog", SearchPathDigest: "path"}
	frame := PostgresCatalogFrame{DatabaseOID: 9, Fingerprint: "catalog-v1", Relations: []PostgresRelationIdentity{{
		DatabaseOID: 9, OID: 42, NamespaceOID: 11, Schema: "public", Name: "items", Kind: 'r', Persistence: 'p',
	}}}

	facts, err := canonicalizeClosedFacts(SemanticFacts{StatementClass: BinderStatementSelect}, identity, frame)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Identity.DatasourceIdentity != "" || facts.Identity.CatalogDigest != frame.Fingerprint ||
		facts.Identity.PlanGeneration != 1 || len(facts.Relations) != 1 ||
		facts.Relations[0].CatalogFingerprint != frame.Fingerprint {
		t.Fatalf("canonical facts=%#v", facts)
	}

	frame.Fingerprint = ""
	_, err = canonicalizeClosedFacts(SemanticFacts{StatementClass: BinderStatementSelect}, identity, frame)
	requireAuthorizationReason(t, err, "AUTH_CATALOG_INCOMPLETE")
}

func semanticSelectFixture() SemanticFacts {
	identity := SemanticIdentity{DatasourceIdentity: "endpoint", DatabaseOID: 9, SessionUser: "agent", CurrentUser: "agent", RoleOID: 10, FixedSearchPath: "pg_catalog", SearchPathDigest: "path", CatalogDigest: "catalog-v1"}
	return SemanticFacts{Schema: SemanticFactsSchemaID, SchemaVersion: SemanticFactsVersion, StatementClass: BinderStatementSelect, Identity: identity,
		Relations:  []SemanticRelation{{DatasourceID: "ds-1", DatabaseOID: 9, RelationOID: 100, NamespaceOID: 11, Schema: "public", Name: "a", Kind: 'r', Persistence: 'p', CatalogFingerprint: "catalog-v1"}, {DatasourceID: "ds-1", DatabaseOID: 9, RelationOID: 200, NamespaceOID: 11, Schema: "public", Name: "b", Kind: 'r', Persistence: 'p', CatalogFingerprint: "catalog-v1"}},
		ColumnUses: []SemanticColumnUse{{RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 23, TypeModifier: -1, Usage: SemanticUsageOutput, Site: "target", OutputIndex: 0}, {RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 23, TypeModifier: -1, Usage: SemanticUsageReference, Site: "where", OutputIndex: -1}}}
}

func semanticUpdateFixture() SemanticFacts {
	value := semanticSelectFixture()
	value.StatementClass = BinderStatementUpdate
	value.Action = b5dml.ActionUpdate
	value.Relations = value.Relations[:1]
	value.ColumnUses = []SemanticColumnUse{{RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 23, TypeModifier: -1, Usage: SemanticUsageReference, Site: "where", OutputIndex: -1}}
	value.WriteTargets = []SemanticWriteTarget{{RelationOID: 100, Attnum: 2, Name: "value", TypeOID: 25, TypeModifier: -1, Kind: b5dml.WriteTargetColumn, Source: b5dml.WriteSourceExplicit}}
	return value
}

func testDualModeAttestations() []b5dml.BinderAttestation {
	values := make([]b5dml.BinderAttestation, 0, 5)
	for major := 14; major <= 18; major++ {
		values = append(values, b5dml.BinderAttestation{ServerMajor: major, ABI: b5dml.BinderABI, BuildHash: "build", ExtensionHash: "extension", NodeManifestHash: "nodes", AllowlistHash: "allowlist"})
	}
	return values
}
