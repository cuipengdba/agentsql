package policybundle

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestCanonicalAndDigestIgnoreRowOrderButTrackChildChanges(t *testing.T) {
	rows := []model.Policy{
		{ID: "b", AgentID: "agent", DatasourceID: "db", ObjectType: "table", ObjectName: "public.b", Action: "allow", Revision: 2},
		{ID: "a", AgentID: "agent", DatasourceID: "db", ObjectType: "column", ObjectName: "public.a", Action: "allow", Revision: 5,
			ColumnPermissions: []model.PolicyColumnPermission{
				{RelationEnrollmentID: "rel", ColumnOrdinal: 2, ColumnName: "phone", ColumnTypeDigest: "type", Usage: "output"},
				{RelationEnrollmentID: "rel", ColumnOrdinal: 1, ColumnName: "id", ColumnTypeDigest: "type", Usage: "output"},
			}},
	}
	first := FromModels("tenant-a", rows)
	second := FromModels("tenant-a", []model.Policy{rows[1], rows[0]})
	second.Policies[0].ColumnPermissions[0], second.Policies[0].ColumnPermissions[1] = second.Policies[0].ColumnPermissions[1], second.Policies[0].ColumnPermissions[0]
	a, err := Digest(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Digest(second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("reordering changed digest: %s != %s", a, b)
	}
	changes, err := Diff(first, second)
	if err != nil || len(changes) != 0 {
		t.Fatalf("reordering changed plan: %#v %v", changes, err)
	}
	second.Policies[0].ColumnPermissions[0].ColumnTypeDigest = "changed"
	c, err := Digest(second)
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("child permission change did not change digest")
	}
}

func TestDiffRejectsTenantAndDuplicateAndFlagsGrantBroadening(t *testing.T) {
	current := Bundle{SchemaVersion: SchemaVersion, TenantID: "tenant-a", Policies: []Entry{
		{ID: "deny", AgentID: "a", DatasourceID: "d", ObjectType: "table", ObjectName: "public.secret", Action: "deny", Revision: 1},
	}}
	desired := Bundle{SchemaVersion: SchemaVersion, TenantID: "tenant-b", Policies: []Entry{}}
	if _, err := Diff(current, desired); err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("cross-tenant plan accepted: %v", err)
	}
	desired.TenantID = current.TenantID
	changes, err := Diff(current, desired)
	if err != nil || len(changes) != 1 || changes[0].Operation != "delete" || !changes[0].BroadensGrant {
		t.Fatalf("deleting a deny was not flagged: %#v %v", changes, err)
	}
	desired.Policies = []Entry{current.Policies[0], current.Policies[0]}
	if _, err := Canonical(desired); err == nil {
		t.Fatal("duplicate policy accepted")
	}
}

func TestReplayIsConservativeAndTenantBound(t *testing.T) {
	before := Bundle{SchemaVersion: SchemaVersion, TenantID: "tenant-a", Policies: []Entry{
		{ID: "deny", AgentID: "a", DatasourceID: "d", ObjectType: "table", ObjectName: "public.secret", Action: "deny", Revision: 1},
	}}
	after := Bundle{SchemaVersion: SchemaVersion, TenantID: "tenant-a", Policies: []Entry{
		{ID: "allow", AgentID: "a", DatasourceID: "d", ObjectType: "table", ObjectName: "public.secret", Action: "allow", Revision: 0},
	}}
	probe := ReplayCase{AgentID: "a", DatasourceID: "d", ObjectName: "public.secret"}
	result, err := Replay(before, after, []ReplayCase{probe})
	if err != nil || len(result) != 1 || result[0].Before != "deny" || result[0].After != "candidate" {
		t.Fatalf("unexpected replay: %#v %v", result, err)
	}
	if _, err := Replay(before, after, []ReplayCase{{AgentID: "a", DatasourceID: "d", ObjectName: "public.*"}}); err == nil {
		t.Fatal("wildcard probe accepted")
	}
	after.Policies = append(after.Policies, Entry{ID: "column", AgentID: "a", DatasourceID: "d", ObjectType: "column", ObjectName: "public.secret", Action: "allow", Revision: 1})
	result, err = Replay(before, after, []ReplayCase{probe})
	if err != nil || result[0].After != "unknown" {
		t.Fatalf("column scope was overclaimed: %#v %v", result, err)
	}
	after.TenantID = "tenant-b"
	if _, err := Replay(before, after, []ReplayCase{probe}); err == nil {
		t.Fatal("cross-tenant replay accepted")
	}
}
