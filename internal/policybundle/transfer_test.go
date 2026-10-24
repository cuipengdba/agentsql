package policybundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestSignedTransferPlansExplicitIdentityMappingWithoutDeletingTargetRules(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)
	source := Bundle{SchemaVersion: SchemaVersion, TenantID: "source-tenant", Policies: []Entry{
		{ID: "source-policy", AgentID: "source-agent", DatasourceID: "source-db", ObjectType: "table", ObjectName: "public.orders", Action: "allow", Revision: 4},
	}}
	transfer, err := SignTransfer(source, "staging", now, private)
	if err != nil {
		t.Fatal(err)
	}
	current := Bundle{SchemaVersion: SchemaVersion, TenantID: "target-tenant", Policies: []Entry{
		{ID: "untouched", AgentID: "target-agent", DatasourceID: "target-db", ObjectType: "table", ObjectName: "public.secret", Action: "deny", Revision: 3},
	}}
	mapping := TransferMapping{TargetTenant: "target-tenant", Agents: map[string]string{"source-agent": "target-agent"},
		Datasources: map[string]string{"source-db": "target-db"}, Policies: map[string]string{"source-policy": "target-policy"},
		Objects: map[string]string{"source-policy": "public.orders"}}
	desired, changes, err := PlanTransfer(transfer, public, now.Add(time.Hour), current, mapping)
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Policies) != 2 || len(changes) != 1 || changes[0].Operation != "create" || !changes[0].BroadensGrant {
		t.Fatalf("unexpected mapped plan: %#v %#v", desired, changes)
	}
	if desired.Policies[0].ID != "untouched" || desired.Policies[1].ID != "target-policy" || desired.Policies[1].Revision != 0 {
		t.Fatalf("target identity or unrelated policy changed: %#v", desired)
	}
	if current.Policies[0].ID != "untouched" || source.Policies[0].ID != "source-policy" {
		t.Fatal("planning modified an input bundle")
	}
	if _, _, err := PlanTransfer(transfer, public, now.Add(time.Hour), current, TransferMapping{TargetTenant: "wrong"}); err == nil {
		t.Fatal("wrong target tenant was accepted")
	}
	delete(mapping.Agents, "source-agent")
	if _, _, err := PlanTransfer(transfer, public, now.Add(time.Hour), current, mapping); err == nil {
		t.Fatal("missing Agent mapping was accepted")
	}
	mapping.Agents["source-agent"] = "target-agent"
	mapping.Objects["source-policy"] = "public.*"
	if _, _, err := PlanTransfer(transfer, public, now.Add(time.Hour), current, mapping); err == nil {
		t.Fatal("wildcard target table was accepted")
	}
}

func TestSignedTransferRejectsTamperingExpiryAndUnsupportedPolicy(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)
	source := Bundle{SchemaVersion: SchemaVersion, TenantID: "tenant", Policies: []Entry{
		{ID: "p", AgentID: "a", DatasourceID: "d", ObjectType: "table", ObjectName: "public.t", Action: "deny", Revision: 1},
	}}
	transfer, err := SignTransfer(source, "staging", now, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransfer(transfer, public, now.Add(8*24*time.Hour)); err == nil || !strings.Contains(err.Error(), "validity") {
		t.Fatalf("expired transfer was accepted: %v", err)
	}
	transfer.Payload.SourceEnvironment = "production"
	if err := VerifyTransfer(transfer, public, now); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered source was accepted: %v", err)
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransfer(transfer, otherPublic, now); err == nil {
		t.Fatal("untrusted public key was accepted")
	}
	source.Policies[0].ObjectType = "column"
	if _, err := SignTransfer(source, "staging", now, private); err == nil || !strings.Contains(err.Error(), "not a transferable") {
		t.Fatalf("unsupported column policy was accepted: %v", err)
	}
	source.Policies[0].ObjectType = "table"
	source.Policies[0].ObjectName = "public.*"
	if _, err := SignTransfer(source, "staging", now, private); err == nil || !strings.Contains(err.Error(), "non-exact") {
		t.Fatalf("wildcard source policy was accepted: %v", err)
	}
}
