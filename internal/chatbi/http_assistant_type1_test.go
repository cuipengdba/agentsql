package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOriginalType1BootstrapRequiresRegisteredCertificateForControlledQueries(t *testing.T) {
	ctx := context.Background()
	service, admin, gateway, _, _ := serviceFixture(t)
	loginKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	service.Identity.privateKey = loginKey
	user, revision, err := service.Store.User(ctx, admin.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	admin.User = user
	member := testUser(t, "2", "type1-member")
	require.NoError(t, service.Store.CreateUser(ctx, member))
	binding, err := service.binding(ctx, admin, "10")
	require.NoError(t, err)
	binding.User = member.ID
	binding.BindingID = "type1-binding"
	binding.ConnectorToken = "type1-connector"
	require.NoError(t, service.Store.ConfigureConnections(ctx, ConnectionConfig{Bindings: []Binding{binding}}))
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders","host":"untrusted-host","password":"DO_NOT_USE"}]}`))
	}))
	defer catalog.Close()
	endpoint := catalog.URL + "/catalog"
	app, err := service.SaveAssistant(ctx, admin, AssistantApplication{Name: "Type 1", Type: 1,
		Domain: "https://host.example", Enabled: true,
		Configuration: `{"endpoint":"` + endpoint + `","welcome":"Hello"}`})
	require.NoError(t, err)
	auth := `[{"key":"X-Catalog-Token","value":"synthetic-secret","target":"header"}]`
	bridge := AssistantCatalogBridge{Endpoint: endpoint, Authorization: auth, Assistant: app.ID,
		Workspace: admin.Workspace, User: member.ID,
		Sources:      []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs: []string{"127.0.0.1/32"}, AllowHTTP: true}
	require.NoError(t, service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	claims := assistantSignPayload{AssistantID: app.ID, SignTime: time.Now().UTC().Format(time.RFC3339),
		Target: "https://host.example", UserAgent: "Original SDK browser", Timezone: "UTC"}
	info := httptest.NewRequest(http.MethodGet, "/api/v1/system/assistant/info/"+string(app.ID), nil)
	info.Header.Set("Origin", claims.Target)
	info.Header.Set("User-Agent", claims.UserAgent)
	info.Header.Set("SQLBOT-EMBEDDED-SIGN", originalSDKSignHeader(t, &loginKey.PublicKey, claims))
	infoResponse := httptest.NewRecorder()
	handler.ServeHTTP(infoResponse, info)
	require.Equal(t, http.StatusOK, infoResponse.Code, infoResponse.Body.String())
	var envelope struct {
		Data struct {
			Token         string `json:"token"`
			Configuration string `json:"configuration"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(infoResponse.Body.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Data.Token)
	require.NotContains(t, envelope.Data.Configuration, endpoint)
	request := func(method, path, body, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Assistant "+envelope.Data.Token)
		if credential != "" {
			r.Header.Set("X-SQLBOT-ASSISTANT-CERTIFICATE", base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(credential))))
			r.Header.Set("X-SQLBOT-HOST-ORIGIN", claims.Target)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	bootstrap := request(http.MethodGet, "/api/v1/chat/list", "", "")
	require.Equal(t, http.StatusOK, bootstrap.Code)
	require.JSONEq(t, `{"code":0,"data":[],"msg":null}`, bootstrap.Body.String())
	require.Equal(t, http.StatusUnauthorized, request(http.MethodGet, "/api/v1/system/assistant/ds", "", "").Code)
	require.Equal(t, http.StatusUnauthorized, request(http.MethodGet, "/api/v1/agentsql/assistant/context", "", "").Code)
	require.Equal(t, http.StatusUnauthorized, request(http.MethodGet, "/api/v1/system/assistant/ds", "", `[{"key":"X-Catalog-Token","value":"wrong","target":"header"}]`).Code)
	wrongOrigin := httptest.NewRequest(http.MethodGet, "/api/v1/system/assistant/ds", nil)
	wrongOrigin.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Assistant "+envelope.Data.Token)
	wrongOrigin.Header.Set("X-SQLBOT-ASSISTANT-CERTIFICATE", base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(auth))))
	wrongOrigin.Header.Set("X-SQLBOT-HOST-ORIGIN", "https://host.example.evil")
	wrongOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongOriginResponse, wrongOrigin)
	require.Equal(t, http.StatusUnauthorized, wrongOriginResponse.Code)
	ds := request(http.MethodGet, "/api/v1/system/assistant/ds", "", auth)
	require.Equal(t, http.StatusOK, ds.Code, ds.Body.String())
	require.Contains(t, ds.Body.String(), `"id":"10"`)
	require.NotContains(t, ds.Body.String(), "untrusted-host")
	require.NotContains(t, ds.Body.String(), "DO_NOT_USE")
	context := request(http.MethodGet, "/api/v1/agentsql/assistant/context", "", auth)
	require.Equal(t, http.StatusOK, context.Code, context.Body.String())
	require.Contains(t, context.Body.String(), `"app_id":"`+string(app.ID)+`"`)
	defaultModel := request(http.MethodGet, "/api/v1/system/aimodel/default", "", auth)
	require.Equal(t, http.StatusOK, defaultModel.Code, defaultModel.Body.String())
	start := request(http.MethodPost, "/api/v1/chat/assistant/start", `{"datasource":"10","origin":2}`, auth)
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	var started struct {
		Data struct {
			ID ID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(start.Body.Bytes(), &started))
	require.True(t, started.Data.ID.Valid())
	answer := request(http.MethodPost, "/api/v1/chat/question", `{"chat_id":"`+string(started.Data.ID)+`","question":"amount?"}`, auth)
	require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
	require.Contains(t, answer.Body.String(), `"type":"finish"`)
	require.Equal(t, 1, gateway.queries)
	disabled := false
	bridge.Enabled = &disabled
	require.NoError(t, service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}))
	denied := request(http.MethodPost, "/api/v1/chat/question", `{"chat_id":"`+string(started.Data.ID)+`","question":"again?"}`, auth)
	require.Equal(t, http.StatusUnauthorized, denied.Code, denied.Body.String())
	require.Equal(t, 1, gateway.queries)
}
