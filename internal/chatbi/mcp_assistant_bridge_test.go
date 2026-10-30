package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAssistantCatalogBridgeBindsCredentialToLiveUserAndOwnedSource(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	registered := AssistantCatalogBridge{
		Endpoint: "https://catalog.example.test/sources", Workspace: principal.Workspace, User: principal.User.ID,
		Authorization: `[{"key":"X-Catalog-Token","value":"synthetic-private-token","target":"header"},{"key":"tenant","value":"east","target":"param"}]`,
		Sources:       []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs:  []string{"203.0.113.1/32"},
	}
	privateFile := filepath.Join(t.TempDir(), "connections.private.json")
	raw, err := json.Marshal(ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{registered}})
	if err != nil || os.WriteFile(privateFile, raw, 0600) != nil {
		t.Fatal("could not prepare private catalog registration fixture")
	}
	imported, err := ReadConnections(privateFile)
	if err != nil || len(imported.AssistantCatalogs) != 1 {
		t.Fatalf("private registration file could not be read: %v", err)
	}
	if err := service.Store.ConfigureConnections(ctx, imported); err != nil {
		t.Fatal(err)
	}
	// Array order and HTTP header case do not change the registered caller.
	reordered := `[{"key":"tenant","value":"east","target":"param"},{"key":"x-catalog-token","value":"synthetic-private-token","target":"header"}]`
	caller, sources, revision, err := service.resolveAssistantCatalogBridge(ctx, registered.Endpoint, reordered)
	if err != nil || revision != 1 || caller.User.ID != principal.User.ID || sources["81"] != "10" {
		t.Fatalf("registered bridge did not resolve to its live caller and source: %v", err)
	}
	for _, attempt := range []struct{ endpoint, auth string }{
		{registered.Endpoint, `[{"key":"X-Catalog-Token","value":"wrong","target":"header"},{"key":"tenant","value":"east","target":"param"}]`},
		{"https://catalog.example.test/other", registered.Authorization},
		{registered.Endpoint, `[]`},
	} {
		if _, _, _, err := service.resolveAssistantCatalogBridge(ctx, attempt.endpoint, attempt.auth); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("unregistered bridge resolved: %v", err)
		}
	}
	// The private registration credential is neither a stored object ID nor a
	// plaintext sealed body; the management audit records counts only.
	rows, err := service.Store.db.QueryContext(ctx, `SELECT id,body FROM chatbi_objects WHERE kind IN ('assistant-catalog','management-audit')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var body []byte
		if rows.Scan(&id, &body) != nil || bytes.Contains([]byte(id), []byte("synthetic-private-token")) ||
			bytes.Contains(body, []byte("synthetic-private-token")) {
			t.Fatal("assistant catalog credential persisted in plaintext")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Revoking the existing per-user binding denies the old catalog credential.
	key := bindingKey(principal, "10")
	var binding Binding
	bindingRevision, err := service.Store.get(ctx, key, &binding)
	if err != nil || service.Store.delete(ctx, key, bindingRevision) != nil {
		t.Fatal("could not revoke catalog source binding")
	}
	if _, _, _, err := service.resolveAssistantCatalogBridge(ctx, registered.Endpoint, registered.Authorization); !errors.Is(err, ErrUnauthenticated) || gateway.queries != 0 {
		t.Fatalf("revoked datasource retained catalog authority or reached gateway: %v", err)
	}
	if err := service.Store.put(ctx, key, binding, 0); err != nil {
		t.Fatal(err)
	}
	// Live user deactivation revokes the bridge without changing the request.
	user, userRevision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Status = 0
	if err := service.Store.put(ctx, userKey(user.ID), user, userRevision); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.resolveAssistantCatalogBridge(ctx, registered.Endpoint, registered.Authorization); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("deactivated caller retained catalog authority: %v", err)
	}
	// Operators can disable the bridge even after its caller is deactivated.
	disabled := false
	registered.Enabled = &disabled
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{registered}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.resolveAssistantCatalogBridge(ctx, registered.Endpoint, registered.Authorization); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled catalog bridge resolved: %v", err)
	}
}

func TestAssistantCatalogBridgeAuditFailureRollsBackRegistration(t *testing.T) {
	service, principal, _, _, _ := serviceFixture(t)
	ctx := context.Background()
	bridge := AssistantCatalogBridge{
		Endpoint: "https://catalog.example.test/sources", Workspace: principal.Workspace, User: principal.User.ID,
		Authorization: `[{"key":"token","value":"synthetic-private-token","target":"header"}]`,
		Sources:       []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs:  []string{"203.0.113.1/32"},
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_assistant_catalog_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err == nil {
		t.Fatal("catalog registration committed without durable management audit")
	}
	if _, _, _, err := service.resolveAssistantCatalogBridge(ctx, bridge.Endpoint, bridge.Authorization); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("audit rollback left catalog credential active: %v", err)
	}
}

func TestAssistantCatalogBridgeRejectsUnownedDatasourceBeforePersistence(t *testing.T) {
	service, principal, _, _, _ := serviceFixture(t)
	ctx := context.Background()
	bridge := AssistantCatalogBridge{
		Endpoint: "https://catalog.example.test/sources", Workspace: principal.Workspace, User: principal.User.ID,
		Authorization: `[{"key":"token","value":"synthetic-private-token","target":"header"}]`,
		Sources:       []AssistantCatalogSource{{CatalogID: "81", Datasource: "999"}},
		AllowedCIDRs:  []string{"203.0.113.1/32"},
	}
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err == nil {
		t.Fatal("unowned datasource registered")
	}
	if _, _, _, err := service.resolveAssistantCatalogBridge(ctx, bridge.Endpoint, bridge.Authorization); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("failed registration left authority: %v", err)
	}
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(audits) != 0 {
		t.Fatalf("failed registration committed an audit or other state: %v", err)
	}
}

func TestAssistantCatalogBridgeRequiresExplicitHTTPApproval(t *testing.T) {
	service, principal, _, _, _ := serviceFixture(t)
	ctx := context.Background()
	bridge := AssistantCatalogBridge{
		Endpoint: "http://127.0.0.1:18999/sources", Workspace: principal.Workspace, User: principal.User.ID,
		Authorization: `[{"key":"token","value":"synthetic-private-token","target":"header"}]`,
		Sources:       []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs:  []string{"127.0.0.1/32"},
	}
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err == nil {
		t.Fatal("HTTP catalog registration lacked explicit operator approval")
	}
	bridge.AllowHTTP = true
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatalf("explicit private HTTP registration failed: %v", err)
	}
	if _, sources, _, err := service.resolveAssistantCatalogBridge(ctx, bridge.Endpoint, bridge.Authorization); err != nil || sources["81"] != "10" {
		t.Fatalf("approved bridge did not resolve: %v", err)
	}
}
