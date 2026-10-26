package chatbi

import (
	"context"
	"errors"
	"testing"
)

func TestAssistantConfigurationIsScopedAuditedAndRevocable(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	base := AssistantApplication{Name: "Sales assistant", Type: 1, Domain: "https://example.com", DatasourceIDs: []ID{"10"}, Configuration: `{"hint":"sales"}`, Enabled: true}
	if _, err := service.SaveAssistant(ctx, principal, base); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("non-admin create: %v", err)
	}
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	principal.User = user
	app, err := service.SaveAssistant(ctx, principal, base)
	if err != nil || !app.ID.Valid() || app.Revision != 1 || app.Workspace != principal.Workspace {
		t.Fatalf("create: %+v, %v", app, err)
	}
	items, err := service.Assistants(ctx, principal)
	if err != nil || len(items) != 1 || items[0].ID != app.ID {
		t.Fatalf("list: %+v, %v", items, err)
	}
	if _, err := service.Assistant(ctx, Principal{User: user, Workspace: "2"}, app.ID); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("cross-workspace read: %v", err)
	}
	other := testUser(t, "2", "other")
	if err := service.Store.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Assistant(ctx, Principal{User: other, Workspace: "1"}, app.ID); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("non-admin read: %v", err)
	}
	stale := app
	stale.Revision = 2
	if _, err := service.SaveAssistant(ctx, principal, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	app.Enabled = false
	app, err = service.SaveAssistant(ctx, principal, app)
	if err != nil || app.Revision != 2 || app.Enabled {
		t.Fatalf("disable: %+v, %v", app, err)
	}
	bad := app
	bad.ID, bad.Revision = "", 0
	bad.Domain = "http://outside.example"
	if _, err := service.SaveAssistant(ctx, principal, bad); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("insecure origin: %v", err)
	}
	bad = app
	bad.ID, bad.Revision = "", 0
	bad.Domain = "https://example.com"
	bad.Configuration = `{"hint":`
	if _, err := service.SaveAssistant(ctx, principal, bad); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("invalid config: %v", err)
	}
	if err := service.DeleteAssistant(ctx, principal, app.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := service.DeleteAssistant(ctx, principal, app.ID, app.Revision); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := service.Assistant(ctx, principal, app.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted app read: %v", err)
	}
	logs, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(logs) != 3 {
		t.Fatalf("management audit count %d: %v", len(logs), err)
	}
}
