package chatbi

import (
	"context"
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
