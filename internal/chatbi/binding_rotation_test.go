package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixtureRotation() BindingRotation {
	return BindingRotation{
		Workspace: "1", User: "1", Datasource: "10", ExpectedBindingID: "binding",
		BindingID: "replacement-binding", ConnectorToken: "replacement-connector",
		AgentKey: "replacement-agent", ResolveName: "replacement-name",
	}
}

func TestRotateBindingPreflightAndAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, _ := serviceFixture(t)
	before, revision, digest, err := service.bindingSnapshot(ctx, principal, "10")
	if err != nil {
		t.Fatal(err)
	}
	rotation := fixtureRotation()
	if err := service.RotateBinding(ctx, rotation); err != nil {
		t.Fatal(err)
	}
	if gateway.schemaCalls != 1 || gateway.queries != 0 {
		t.Fatalf("rotation should preflight schema without querying data: schemas=%d queries=%d", gateway.schemaCalls, gateway.queries)
	}
	if err := service.bindingUnchanged(ctx, principal, before, revision, digest); !errors.Is(err, ErrGateway) {
		t.Fatalf("in-flight old binding remained valid: %v", err)
	}
	after, err := service.binding(ctx, principal, "10")
	if err != nil || after.BindingID != rotation.BindingID || after.ConnectorToken != rotation.ConnectorToken ||
		after.AgentKey != rotation.AgentKey || after.ResolveName != rotation.ResolveName || after.Name != before.Name ||
		after.Dialect != before.Dialect || after.SelectedTables != before.SelectedTables {
		t.Fatalf("rotation changed the wrong fields: %v", err)
	}
	events, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(events) != 1 {
		t.Fatalf("rotation audit missing: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal(events[0], &event); err != nil || event["action"] != "connections.binding.rotate" ||
		event["workspace"] != "1" || event["owner"] != "1" || event["id"] != "10" {
		t.Fatalf("wrong rotation audit: %v %v", event, err)
	}
	if err := service.RotateBinding(ctx, rotation); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale rotation was accepted: %v", err)
	}
}

func TestRotateBindingFailsClosed(t *testing.T) {
	ctx := context.Background()
	t.Run("stale binding", func(t *testing.T) {
		service, principal, gateway, _, _ := serviceFixture(t)
		rotation := fixtureRotation()
		rotation.ExpectedBindingID = "stale"
		if err := service.RotateBinding(ctx, rotation); !errors.Is(err, ErrConflict) || gateway.schemaCalls != 0 {
			t.Fatalf("stale mapping reached gateway: %v", err)
		}
		unchangedRotation(t, service, principal)
	})
	t.Run("gateway rejection", func(t *testing.T) {
		service, principal, gateway, _, _ := serviceFixture(t)
		gateway.schemaErr = ErrGateway
		if err := service.RotateBinding(ctx, fixtureRotation()); !errors.Is(err, ErrGateway) {
			t.Fatalf("rejected gateway mapping was saved: %v", err)
		}
		unchangedRotation(t, service, principal)
	})
	t.Run("concurrent change", func(t *testing.T) {
		service, principal, gateway, _, _ := serviceFixture(t)
		gateway.afterSchema = func() {
			gateway.afterSchema = nil
			current, rev, _, err := service.bindingSnapshot(ctx, principal, "10")
			if err != nil {
				t.Fatal(err)
			}
			current.Name = "changed-concurrently"
			if err := service.Store.put(ctx, bindingKey(principal, "10"), current, rev); err != nil {
				t.Fatal(err)
			}
		}
		if err := service.RotateBinding(ctx, fixtureRotation()); !errors.Is(err, ErrGateway) {
			t.Fatalf("rotation overwrote concurrent change: %v", err)
		}
		current, err := service.binding(ctx, principal, "10")
		if err != nil || current.Name != "changed-concurrently" || current.BindingID != "binding" {
			t.Fatalf("concurrent change was lost: %v", err)
		}
	})
	t.Run("audit rollback", func(t *testing.T) {
		service, principal, _, _, _ := serviceFixture(t)
		if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_rotation_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`); err != nil {
			t.Fatal(err)
		}
		if err := service.RotateBinding(ctx, fixtureRotation()); err == nil {
			t.Fatal("rotation survived audit failure")
		}
		unchangedRotation(t, service, principal)
	})
}

func unchangedRotation(t *testing.T, service *Service, principal Principal) {
	t.Helper()
	current, err := service.binding(context.Background(), principal, "10")
	if err != nil || current.BindingID != "binding" || current.ConnectorToken != "fixture-connector" {
		t.Fatalf("failed rotation modified binding: %v", err)
	}
}

func TestReadBindingRotationRejectsUnexpectedInput(t *testing.T) {
	valid, err := json.Marshal(fixtureRotation())
	if err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(t.TempDir(), "valid.json")
	if err := os.WriteFile(validPath, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if rotation, err := ReadBindingRotation(validPath); err != nil || rotation != fixtureRotation() {
		t.Fatalf("valid private rotation was rejected: %v", err)
	}
	for _, body := range []string{
		`{}`, `{"workspace":"1","user":"1","datasource":"10","expected_binding_id":"binding","binding_id":"new","connector_token":"secret","agent_key":"key","extra":true}`,
		`{"workspace":"1","user":"1","datasource":"10","expected_binding_id":"binding","binding_id":"new","connector_token":"secret","agent_key":"key"} {}`,
	} {
		path := filepath.Join(t.TempDir(), "rotation.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadBindingRotation(path); err == nil {
			t.Fatal("invalid rotation file accepted")
		}
	}
	oversizedPath := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(oversizedPath, append(valid, make([]byte, 1<<20)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBindingRotation(oversizedPath); err == nil {
		t.Fatal("oversized private rotation was accepted")
	}
}
