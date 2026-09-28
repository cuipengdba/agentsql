package businessdb

import (
	"context"
	"testing"
)

func TestCanonicalB5ClosedManifestStableAndCatalogBound(t *testing.T) {
	t.Parallel()
	frame := closedDMLUnitFrame()
	parsed, err := parseClosedDMLForBinding(`UPDATE app.items SET n=n+1 WHERE id=1`, &unlimitedPostgresBudget{})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := resolvePostgresClosedDML(context.Background(), parsed, frame, "ds-1", closedDMLUnitOperator, &unlimitedPostgresBudget{})
	if err != nil {
		t.Fatal(err)
	}
	identity := SemanticIdentity{DatabaseOID: frame.DatabaseOID, SessionUser: "agent", CurrentUser: "agent", RoleOID: 10,
		FixedSearchPath: "pg_catalog", SearchPathDigest: "fixed-search-path"}
	facts, err = canonicalizeClosedFacts(facts, identity, frame)
	if err != nil {
		t.Fatal(err)
	}
	program := closedB5TestProgram(t, facts, frame, closedCapability(frame.ServerVersion, frame.DatabaseOID))
	want, err := canonicalB5ClosedManifest(program, frame)
	if err != nil {
		t.Fatal(err)
	}

	reordered := program
	reordered.Facts = cloneSemanticFacts(program.Facts)
	reordered.Capability.Capabilities = append([]string(nil), program.Capability.Capabilities...)
	reordered.Facts.ColumnUses[0], reordered.Facts.ColumnUses[1] = reordered.Facts.ColumnUses[1], reordered.Facts.ColumnUses[0]
	reordered.Capability.Capabilities[0], reordered.Capability.Capabilities[1] = reordered.Capability.Capabilities[1], reordered.Capability.Capabilities[0]
	got, err := canonicalB5ClosedManifest(closedB5TestProgram(t, reordered.Facts, frame, reordered.Capability), frame)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("canonical manifest changed after collection reorder: got %x want %x", got, want)
	}

	driftedFrame := frame
	driftedFrame.Fingerprint = "catalog-v2"
	if _, err := canonicalB5ClosedManifest(program, driftedFrame); err == nil {
		t.Fatal("stale sealed program accepted a changed catalog fingerprint")
	}
	driftedFacts := cloneSemanticFacts(facts)
	driftedFacts.Identity.CatalogDigest = driftedFrame.Fingerprint
	for i := range driftedFacts.Relations {
		driftedFacts.Relations[i].CatalogFingerprint = driftedFrame.Fingerprint
	}
	drifted := closedB5TestProgram(t, driftedFacts, driftedFrame, program.Capability)
	driftedDigest, err := canonicalB5ClosedManifest(drifted, driftedFrame)
	if err != nil {
		t.Fatal(err)
	}
	if driftedDigest == want {
		t.Fatal("catalog change did not change the sealed manifest")
	}
}

func closedB5TestProgram(t *testing.T, facts SemanticFacts, frame PostgresCatalogFrame, capability CapabilityAttestation) BoundProgram {
	t.Helper()
	factsDigest, err := facts.Digest()
	if err != nil {
		t.Fatal(err)
	}
	capability.Digest, err = capability.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	return BoundProgram{Mode: BinderModeCatalogClosedV1, Facts: facts, SemanticFactsDigest: factsDigest,
		EngineEvidenceDigest: closedEngineEvidenceDigest(factsDigest, frame.Fingerprint), Capability: capability}
}
