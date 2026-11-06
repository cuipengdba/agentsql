package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbeddedDatasourceListPrewarmsCurrentGuestTables(t *testing.T) {
	ctx := context.Background()
	service, admin, gateway, _, _ := serviceFixture(t)
	loginKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = loginKey
	guest := testUser(t, "2", "embedded-prewarm-guest")
	if err := service.Store.CreateUser(ctx, guest); err != nil {
		t.Fatal(err)
	}
	adminUser, revision, err := service.Store.User(ctx, admin.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	adminUser.Admin = true
	if err := service.Store.put(ctx, userKey(adminUser.ID), adminUser, revision); err != nil {
		t.Fatal(err)
	}
	admin.User = adminUser
	binding, err := service.binding(ctx, admin, "10")
	if err != nil {
		t.Fatal(err)
	}
	binding.User = guest.ID
	binding.BindingID = "embedded-prewarm-binding"
	binding.ConnectorToken = "embedded-prewarm-connector"
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{Bindings: []Binding{binding}}); err != nil {
		t.Fatal(err)
	}
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"},
	}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	embedding := &indexedTableFixture{identity: "fixed-embedded-prewarm-v1"}
	service.Embedding = embedding
	stop := service.EnableTableIndexPrewarm(ctx)
	defer stop()
	app, err := service.SaveAssistant(ctx, admin, AssistantApplication{
		Name: "Embedded prewarm", Type: 0, Domain: "https://app.example",
		DatasourceIDs: []ID{"10"}, PublicGuestUser: guest.ID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "v4.5.0")
	if err != nil {
		t.Fatal(err)
	}
	claims := assistantSignPayload{AssistantID: app.ID,
		SignTime: time.Now().UTC().Format(time.RFC3339), Target: "https://app.example",
		UserAgent: "Original SDK browser", Timezone: "UTC"}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/assistant/info/"+string(app.ID), nil)
	request.Header.Set("Origin", claims.Target)
	request.Header.Set("User-Agent", claims.UserAgent)
	request.Header.Set("SQLBOT-EMBEDDED-SIGN", originalSDKSignHeader(t, &loginKey.PublicKey, claims))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("embedded assistant info: %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.Token == "" {
		t.Fatalf("embedded token: %v %s", err, response.Body.String())
	}
	principal, err := service.Identity.VerifyEmbedded(ctx, envelope.Data.Token)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/system/assistant/ds", nil)
	request.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Assistant "+envelope.Data.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("embedded datasource list: %d %s", response.Code, response.Body.String())
	}
	waitTableIndexPrewarm(t, service, principal, "10", "ready")
	if len(embedding.calls) != 1 || len(embedding.calls[0]) != 2 {
		t.Fatalf("embedded guest index does not contain both authorized tables: %q", embedding.calls)
	}
}
