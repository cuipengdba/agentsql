package chatbi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestActiveCatalogAssistantCustomModelRequiresCurrentMapping(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	principal.User = user
	activateModelCatalogFixture(t, service.Store, ModelConfig{ID: "30", Model: "default", Endpoint: "https://example.invalid/v1"})
	custom := ID("31")
	if err := service.Store.put(ctx, modelCatalogKey(custom),
		ModelConfig{ID: custom, Model: "specialized", Endpoint: "https://example.invalid/v1"}, 0); err != nil {
		t.Fatal(err)
	}
	app := AssistantApplication{Name: "Scoped assistant", Type: 1, Domain: "https://example.com", DatasourceIDs: []ID{"10"},
		UseCustomModel: true, CustomModel: &custom, Enabled: true}
	if _, err := service.SaveAssistant(ctx, principal, app); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("unmapped assistant model accepted: %v", err)
	}
	mapping := modelMappingKey(custom, principal.Workspace)
	if err := service.Store.put(ctx, mapping, catalogModelMapping{Model: custom, Workspace: principal.Workspace}, 0); err != nil {
		t.Fatal(err)
	}
	if saved, err := service.SaveAssistant(ctx, principal, app); err != nil || !saved.ID.Valid() {
		t.Fatalf("mapped assistant model rejected: %+v %v", saved, err)
	}
}

func TestEmbeddedAssistantUsesConfiguredMappedModelAndRevokesIt(t *testing.T) {
	ctx := context.Background()
	service, admin, gateway, generator, _ := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, admin.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	admin.User = user
	viewer := testUser(t, "2", "viewer")
	if err := service.Store.CreateUser(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	var binding Binding
	if _, err := service.Store.get(ctx, bindingKey(admin, "10"), &binding); err != nil {
		t.Fatal(err)
	}
	binding.User = viewer.ID
	viewerPrincipal := Principal{User: viewer, Workspace: admin.Workspace}
	if err := service.Store.put(ctx, bindingKey(viewerPrincipal, "10"), binding, 0); err != nil {
		t.Fatal(err)
	}
	activateModelCatalogFixture(t, service.Store, ModelConfig{ID: "30", Model: "global", Endpoint: "https://example.invalid/v1"})
	selectedID := ID("31")
	if err := service.Store.put(ctx, modelCatalogKey(selectedID),
		ModelConfig{ID: selectedID, Model: "specialized", Endpoint: "https://example.invalid/v1"}, 0); err != nil {
		t.Fatal(err)
	}
	mapping := modelMappingKey(selectedID, admin.Workspace)
	if err := service.Store.put(ctx, mapping, catalogModelMapping{Model: selectedID, Workspace: admin.Workspace}, 0); err != nil {
		t.Fatal(err)
	}
	app, err := service.SaveAssistant(ctx, admin, AssistantApplication{Name: "Embedded model", Type: 4,
		Domain: "https://example.com", DatasourceIDs: []ID{"10"}, UseCustomModel: true,
		CustomModel: &selectedID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	embedded := Principal{User: viewer, Workspace: admin.Workspace, EmbeddedApp: app.ID,
		EmbeddedRevision: app.Revision, EmbeddedType: 4}
	chat, err := service.Start(ctx, embedded, "10", "amount", 2)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { return service.live(ctx, embedded) }
	record, err := service.AskWithModel(ctx, embedded, chat.ID, "amount?", nil, check, nil)
	if err != nil || record.Data == nil || generator.last.ID != selectedID || gateway.queries != 1 {
		t.Fatalf("embedded app ignored configured model: %+v %v, selected=%s queries=%d", record, err, generator.last.ID, gateway.queries)
	}
	otherID := ID("30")
	if _, err := service.AskWithModel(ctx, embedded, chat.ID, "override?", &otherID, check, nil); !errors.Is(err, ErrModel) || gateway.queries != 1 {
		t.Fatalf("embedded caller overrode configured model: %v, queries=%d", err, gateway.queries)
	}
	if err := service.Store.delete(ctx, mapping, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AskWithModel(ctx, embedded, chat.ID, "after revoke?", nil, check, nil); !errors.Is(err, ErrModel) || gateway.queries != 1 {
		t.Fatalf("revoked embedded model reached gateway: %v, queries=%d", err, gateway.queries)
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
	issuedToken, err := service.issuePublicGuestToken(ctx, app)
	if err != nil {
		t.Fatalf("server guest issuance: %v", err)
	}
	issuedIdentity, err := service.Identity.VerifyEmbedded(ctx, issuedToken)
	if err != nil || !validVisitorScope(issuedIdentity.VisitorScope) || issuedIdentity.User.ID != guest.ID {
		t.Fatalf("issued guest identity: %v", err)
	}
	secondIssuedToken, err := service.issuePublicGuestToken(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	secondIssuedIdentity, err := service.Identity.VerifyEmbedded(ctx, secondIssuedToken)
	if err != nil || secondIssuedIdentity.VisitorScope == issuedIdentity.VisitorScope {
		t.Fatalf("reused guest visitor scope: %v", err)
	}
	claims := map[string]any{"account": guest.Account, "embeddedId": app.ID,
		"publicGuest": true, "visitor": strings.Repeat("c", 32),
		"appRevision": app.Revision, "authRevision": guest.AuthRevision,
		"iat": time.Now().Unix(), "exp": time.Now().Add(30 * time.Minute).Unix()}
	guestToken := signedEmbeddedFixture(t, app.AppSecret, claims)
	guestIdentity, err := service.Identity.VerifyEmbedded(ctx, guestToken)
	if err != nil || guestIdentity.EmbeddedType != 0 || guestIdentity.VisitorScope != strings.Repeat("c", 32) {
		t.Fatalf("scoped public guest token: %v", err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(prefix, token string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/datasource/list", nil)
		r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", prefix+" "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	if status := call("Assistant", issuedToken); status != http.StatusOK {
		t.Fatalf("original assistant header rejected: %d", status)
	}
	if status := call("Embedded", issuedToken); status != http.StatusUnauthorized {
		t.Fatalf("wrong embedded prefix accepted: %d", status)
	}
	claims["visitor"] = ""
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixture(t, app.AppSecret, claims)); err == nil {
		t.Fatal("unscoped public guest token accepted")
	}
	claims["visitor"] = strings.Repeat("c", 32)
	claims["exp"] = time.Now().Add(2 * time.Hour).Unix()
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixture(t, app.AppSecret, claims)); err == nil {
		t.Fatal("long-lived public guest token accepted")
	}
	claims["exp"] = time.Now().Add(30 * time.Minute).Unix()
	claims["appRevision"] = int64(0)
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixture(t, app.AppSecret, claims)); err == nil {
		t.Fatal("missing app revision accepted")
	}
	claims["appRevision"] = app.Revision
	claims["authRevision"] = int64(0)
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixture(t, app.AppSecret, claims)); err == nil {
		t.Fatal("missing guest auth revision accepted")
	}
	app.Name = "Updated public guest app"
	app, err = service.SaveAssistant(ctx, principal, app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Identity.VerifyEmbedded(ctx, guestToken); err == nil {
		t.Fatal("old guest token survived assistant revision")
	}
	if _, err := service.Identity.VerifyEmbedded(ctx, issuedToken); err == nil {
		t.Fatal("issued guest token survived assistant revision")
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
	if _, err := service.Identity.VerifyEmbedded(ctx, guestToken); err == nil {
		t.Fatal("disabled guest retained an assistant token")
	}
	app.Name = "Changed while guest disabled"
	if _, err := service.SaveAssistant(ctx, principal, app); !errors.Is(err, ErrInvalidAssistant) {
		t.Fatalf("disabled public guest accepted: %v", err)
	}
}
