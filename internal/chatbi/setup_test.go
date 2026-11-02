package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestNativeInstallationAndAtomicConnectionAudit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	installation, err := NewInstallation("native-admin", "Native-install-password-42")
	if err != nil {
		t.Fatal(err)
	}
	if installation.Users[0].AllowLegacyTokens || installation.SourceVersion != "native" {
		t.Fatal("native install impersonated a migration")
	}
	if err := s.ImportSnapshot(ctx, installation); err != nil {
		t.Fatal(err)
	}
	identity, err := NewIdentity(s, []byte(installation.Keys.SigningKey), []byte(installation.Keys.LoginPEM))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Login(ctx, "native-admin", "Native-install-password-42"); err != nil {
		t.Fatal(err)
	}
	config := ConnectionConfig{Models: []WorkspaceModel{{Workspace: "1", Model: ModelConfig{ID: "1", Model: "local-model", Endpoint: "http://127.0.0.1:8080/v1"}}}, Bindings: []Binding{{Datasource: "10", Workspace: "1", User: "1", Name: "sales", Dialect: "pg", BindingID: "fixture", ConnectorToken: "private-connector", AgentKey: "private-agent"}}}
	if err := s.ConfigureConnections(ctx, config); err != nil {
		t.Fatal(err)
	}
	audits, err := s.list(ctx, "management-audit", "system", "system")
	if err != nil || len(audits) != 1 {
		t.Fatal("configuration audit missing")
	}
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER reject_chatbi_config_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	config.Models[0].Model.Model = "replacement"
	if err := s.ConfigureConnections(ctx, config); err == nil {
		t.Fatal("configuration survived audit failure")
	}
	var stored ModelConfig
	if _, err := s.get(ctx, objectKey{"model", "1", "workspace", "default"}, &stored); err != nil || stored.Model != "local-model" {
		t.Fatal("partial configuration update escaped transaction")
	}
}

func TestConnectionConfigRejectsInvalidModelBeforeChanges(t *testing.T) {
	s, _, _, _, _ := serviceFixture(t)
	for _, tokens := range []int{-1, 1, 127, 16385} {
		config := ConnectionConfig{Models: []WorkspaceModel{{Workspace: "1", Model: ModelConfig{ID: "1", Model: "invalid", Endpoint: "https://example.invalid/v1", MaxTokens: tokens}}}}
		if err := s.Store.ConfigureConnections(context.Background(), config); err == nil {
			t.Fatal("invalid token budget accepted")
		}
	}
	var stored ModelConfig
	if _, err := s.Store.get(context.Background(), objectKey{"model", "1", "workspace", "default"}, &stored); err != nil || stored.Model != "fixture" {
		t.Fatal("invalid configuration changed the active model")
	}
}

func TestOperatorRevokeBindingRejectsStaleIDAndAuditFailure(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, _ := serviceFixture(t)
	store := service.Store
	key := bindingKey(principal, "10")
	var active Binding
	revision, err := store.get(ctx, key, &active)
	if err != nil || revision < 1 {
		t.Fatal("fixture binding missing", err)
	}
	if err := store.RevokeBinding(ctx, "1", principal.User.ID, "10", "stale-binding"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale mapping was not rejected: %v", err)
	}
	if _, err := store.get(ctx, key, &active); err != nil {
		t.Fatal("stale command changed the binding", err)
	}
	if _, err := store.db.ExecContext(ctx, "CREATE TRIGGER reject_binding_revoke_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeBinding(ctx, "1", principal.User.ID, "10", "binding"); err == nil {
		t.Fatal("binding revocation survived audit failure")
	}
	if _, err := store.get(ctx, key, &active); err != nil {
		t.Fatal("audit failure changed the binding", err)
	}
	if _, err := store.db.ExecContext(ctx, "DROP TRIGGER reject_binding_revoke_audit"); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeBinding(ctx, "1", principal.User.ID, "10", "binding"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.get(ctx, key, &active); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked mapping remained available: %v", err)
	}
	if _, err := service.binding(ctx, principal, "10"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked member could still resolve a source: %v", err)
	}
	if gateway.queries != 0 {
		t.Fatal("revocation issued a database query")
	}
	if err := store.RevokeBinding(ctx, "1", principal.User.ID, "10", "binding"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat revocation did not fail closed: %v", err)
	}
	events, err := store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(events) != 1 {
		t.Fatal("exactly one successful revocation audit expected", err)
	}
	var audit map[string]any
	if err := json.Unmarshal(events[0], &audit); err != nil ||
		audit["action"] != "connections.binding.revoke" ||
		audit["workspace"] != "1" || audit["owner"] != string(principal.User.ID) ||
		audit["id"] != "10" {
		t.Fatalf("revocation audit did not name the affected source and user: %v", err)
	}
}
