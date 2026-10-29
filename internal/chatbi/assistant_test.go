package chatbi

import (
	"context"
	"errors"
	"testing"
)

func TestAssistantConfigurationIsScopedAuditedAndRevocable(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	modelID := ID("20")
	base := AssistantApplication{Name: "Sales assistant", Type: 1, Domain: "https://example.com", DatasourceIDs: []ID{"10"}, Configuration: `{"hint":"sales"}`, UseCustomModel: true, CustomModel: &modelID, Enabled: true}
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
	bad = app
	bad.ID, bad.Revision = "", 0
	otherModelID := ID("21")
	bad.CustomModel = &otherModelID
	if _, err := service.SaveAssistant(ctx, principal, bad); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("unconfigured model: %v", err)
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

func TestAssistantDomainsAcceptOriginalSemicolonListButRejectInsecureOrigins(t *testing.T) {
	if !validAssistantDomain("https://one.example;https://two.example") || !validAssistantDomain("http://localhost:3000") {
		t.Fatal("valid original-client domain list rejected")
	}
	for _, raw := range []string{"https://one.example;;https://two.example", "http://outside.example", "https://one.example/path", "https://one.example;https://one.example"} {
		if validAssistantDomain(raw) {
			t.Fatalf("invalid origin accepted: %q", raw)
		}
	}
}

func TestPublicAssistantGuestRequiresSeparateCurrentSourceBinding(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	guest := testUser(t, "2", "public-guest")
	if err := service.Store.CreateUser(ctx, guest); err != nil {
		t.Fatal(err)
	}
	admin, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin.Admin = true
	if err := service.Store.put(ctx, userKey(admin.ID), admin, revision); err != nil {
		t.Fatal(err)
	}
	principal.User = admin
	input := AssistantApplication{Name: "Published synthetic data", Type: 0,
		Domain: "https://app.example", DatasourceIDs: []ID{"10"}, PublicGuestUser: guest.ID, Enabled: true}
	if _, err := service.SaveAssistant(ctx, principal, input); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("guest without own datasource accepted: %v", err)
	}
	adminBinding, err := service.binding(ctx, principal, "10")
	if err != nil {
		t.Fatal(err)
	}
	guestBinding := adminBinding
	guestBinding.User = guest.ID
	guestPrincipal := Principal{User: guest, Workspace: principal.Workspace}
	if err := service.Store.put(ctx, bindingKey(guestPrincipal, "10"), guestBinding, 0); err != nil {
		t.Fatal(err)
	}
	app, err := service.SaveAssistant(ctx, principal, input)
	if err != nil || !app.ID.Valid() || app.AppID != "" || len(app.AppSecret) < 32 {
		t.Fatalf("configured public guest app: %+v, %v", app, err)
	}
	input.PublicGuestUser = admin.ID
	if _, err := service.SaveAssistant(ctx, principal, input); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("administrator as public guest accepted: %v", err)
	}
	input.PublicGuestUser = guest.ID
	input.DatasourceIDs = nil
	if _, err := service.SaveAssistant(ctx, principal, input); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("unscoped public guest app accepted: %v", err)
	}
	guest, revision, err = service.Store.User(ctx, guest.ID)
	if err != nil {
		t.Fatal(err)
	}
	guest.Status = 0
	if err := service.Store.put(ctx, userKey(guest.ID), guest, revision); err != nil {
		t.Fatal(err)
	}
	app.Name = "Changed while guest disabled"
	if _, err := service.SaveAssistant(ctx, principal, app); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("disabled public guest accepted: %v", err)
	}
}
