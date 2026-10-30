package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestMCPHelperTokenWorkspaceAndDatasourceListing(t *testing.T) {
	service, p, _, _, oldToken := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	login := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/access_token", `{"username":"analyst","password":"Synthetic-password-1"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("MCP login: %d %s", login.Code, login.Body.String())
	}
	var response struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(login.Body.Bytes(), &response) != nil || response.AccessToken == "" {
		t.Fatal("MCP login did not use the expected token response shape")
	}
	if _, err := service.Identity.Verify(context.Background(), response.AccessToken); err != nil {
		t.Fatalf("MCP token is not a native live token: %v", err)
	}
	workspaceBody, _ := json.Marshal(response.AccessToken)
	ws := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list", string(workspaceBody), "")
	if ws.Code != http.StatusOK || !strings.Contains(ws.Body.String(), `"name":"One"`) || !strings.Contains(ws.Body.String(), `"id":1`) {
		t.Fatalf("MCP workspace list: %d %s", ws.Code, ws.Body.String())
	}
	// The encrypted binding contains several server-only credentials. A helper
	// that describes available sources must never return any of them.
	var binding Binding
	_, err = service.Store.get(context.Background(), bindingKey(p, "10"), &binding)
	if err != nil {
		t.Fatal(err)
	}
	binding.Configuration = `{"password":"test-secret"}`
	if err := service.Store.put(context.Background(), bindingKey(p, "10"), binding, 1); err != nil {
		t.Fatal(err)
	}
	ds := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ds_list", `{"token":"`+response.AccessToken+`","oid":"1"}`, "")
	if ds.Code != http.StatusOK || !strings.Contains(ds.Body.String(), `"name":"fixture"`) || !strings.Contains(ds.Body.String(), `"id":"10"`) {
		t.Fatalf("MCP datasource list: %d %s", ds.Code, ds.Body.String())
	}
	for _, secret := range []string{"test-secret", "configuration", "fixture-connector", "fixture-agent", "binding_id", "connector_token"} {
		if strings.Contains(ds.Body.String(), secret) {
			t.Fatalf("MCP datasource list exposed %s", secret)
		}
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ds_list", `{"token":"`+response.AccessToken+`","oid":"2"}`, ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("unjoined workspace selected: %d", denied.Code)
	}
	if err := service.Identity.Logout(context.Background(), response.AccessToken); err != nil {
		t.Fatal(err)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list", string(workspaceBody), ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token listed workspaces: %d", denied.Code)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ds_list", `{"token":"`+response.AccessToken+`"}`, ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token listed datasources: %d", denied.Code)
	}
	if _, err := service.Identity.Verify(context.Background(), oldToken); err != nil {
		t.Fatal("independent session was accidentally revoked")
	}
}

func TestMCPModelListRequiresMembershipAndExcludesSecrets(t *testing.T) {
	service, _, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	key := objectKey{"model", "1", "workspace", "default"}
	var model ModelConfig
	revision, err := service.Store.get(context.Background(), key, &model)
	if err != nil {
		t.Fatal(err)
	}
	model.Name, model.APIKey = "Private local model", "never-expose-this-key"
	if err := service.Store.put(context.Background(), key, model, revision); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.put(context.Background(), additionalModelKey("1", "21"), ModelConfig{
		ID: "21", Name: "Private DeepSeek model", Model: "fixture-deepseek", Supplier: 3,
		Endpoint: "http://127.0.0.1:8001/v1", APIKey: "secondary-secret",
	}, 0); err != nil {
		t.Fatal(err)
	}
	listed := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_model_list", `{"token":"`+token+`","oid":1}`, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Private local model"`) ||
		!strings.Contains(listed.Body.String(), `"id":20`) || !strings.Contains(listed.Body.String(), `"id":21`) ||
		!strings.Contains(listed.Body.String(), `"supplier":3`) || !strings.Contains(listed.Body.String(), `"default_model":false`) {
		t.Fatalf("MCP model list: %d %s", listed.Code, listed.Body.String())
	}
	for _, secret := range []string{"never-expose-this-key", "secondary-secret", "api_key", "api_domain", "127.0.0.1"} {
		if strings.Contains(listed.Body.String(), secret) {
			t.Fatalf("model list leaked %s", secret)
		}
	}
	headerOnly := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_model_list", `{"oid":1}`, token)
	if headerOnly.Code != http.StatusOK || headerOnly.Body.String() != listed.Body.String() {
		t.Fatalf("original model-list body with authenticated header: %d %s", headerOnly.Code, headerOnly.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_model_list", `{"oid":2}`, token); denied.Code != http.StatusUnauthorized {
		t.Fatalf("header model list crossed workspace: %d", denied.Code)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_model_list", `{"token":"`+token+`","oid":1}`, token); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ambiguous model-list credentials accepted: %d", denied.Code)
	}
	withAsk := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/mcp_model_list", strings.NewReader(`{"token":"`+token+`","oid":1}`))
	withAsk.Header.Set("X-SQLBOT-ASK-TOKEN", "sk conflicting")
	withAskResponse := httptest.NewRecorder()
	handler.ServeHTTP(withAskResponse, withAsk)
	if withAskResponse.Code != http.StatusUnauthorized {
		t.Fatalf("ASK credential alongside body token accepted: %d", withAskResponse.Code)
	}
	for _, body := range []string{`{"oid":1}`, `{"token":"` + token + `","oid":2}`} {
		denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_model_list", body, "")
		if denied.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized model list succeeded: %d %s", denied.Code, denied.Body.String())
		}
	}
	if err := service.Identity.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_model_list", `{"token":"`+token+`","oid":1}`, ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token listed models: %d", denied.Code)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_model_list", `{"oid":1}`, token); denied.Code != http.StatusUnauthorized {
		t.Fatalf("revoked header token listed models: %d", denied.Code)
	}
}

func TestMCPHelperRejectsConflictingCredentialsAndInvalidBodies(t *testing.T) {
	service, _, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ path, body string }{
		{"/api/v1/mcp/mcp_ws_list", strconv.Quote(token)},
		{"/api/v1/mcp/mcp_ds_list", `{"token":"` + token + `"}`},
	} {
		if denied := accountRequest(t, handler, http.MethodPost, route.path, route.body, token); denied.Code != http.StatusUnauthorized {
			t.Fatalf("conflicting bearer on %s accepted: %d", route.path, denied.Code)
		}
	}
	if invalid := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ds_list", `{"token":"`+token+`","oid":"invalid"}`, ""); invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid workspace ID accepted: %d", invalid.Code)
	}
	if invalid := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list", `{"token":"`+token+`"}`, ""); invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("object workspace body accepted: %d", invalid.Code)
	}
}

func TestMCPStartKeepsSourceUnboundUntilAuthorizedSelection(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`","oid":"2"}`, ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("MCP start selected unjoined workspace: %d", denied.Code)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	if started.Code != http.StatusOK {
		t.Fatalf("MCP start: %d %s", started.Code, started.Body.String())
	}
	var output struct {
		Token string `json:"access_token"`
		Chat  ID     `json:"chat_id"`
	}
	if json.Unmarshal(started.Body.Bytes(), &output) != nil || output.Token != token || !output.Chat.Valid() {
		t.Fatalf("MCP start response shape: %s", started.Body.String())
	}
	chat, _, err := service.chat(context.Background(), p, output.Chat)
	if err != nil || chat.Datasource != "" || chat.Origin != 1 || gateway.schemaCalls != 0 {
		t.Fatalf("MCP start selected a datasource or queried schema before authorization: chat=%+v err=%v schema=%d", chat, err, gateway.schemaCalls)
	}
	if err := service.BindMCPSource(context.Background(), p, output.Chat, "11"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown datasource bound: %v", err)
	}
	if err := service.BindMCPSource(context.Background(), p, output.Chat, "10"); err != nil {
		t.Fatalf("owned datasource not bound: %v", err)
	}
	if err := service.BindMCPSource(context.Background(), p, output.Chat, "11"); !errors.Is(err, ErrConflict) {
		t.Fatalf("bound datasource changed: %v", err)
	}
	if _, err := service.Ask(context.Background(), p, output.Chat, "sales?", func(ctx context.Context) error {
		_, err := service.Identity.Verify(ctx, token)
		return err
	}, nil); err != nil {
		t.Fatalf("bound MCP chat could not use audited question path: %v", err)
	}
}

func TestMCPQuestionUsesControlledAuditedResultForJSONAndStream(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	start := func() ID {
		t.Helper()
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
		var output struct {
			Chat ID `json:"chat_id"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &output) != nil {
			t.Fatalf("MCP start: %d %s", response.Code, response.Body.String())
		}
		return output.Chat
	}
	chat := start()
	answer := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(chat)+`,"question":"sales?","stream":false}`, "")
	if answer.Code != http.StatusOK || !strings.Contains(answer.Body.String(), `"success":true`) ||
		!strings.Contains(answer.Body.String(), `"amount":"123"`) || gateway.queries != 1 {
		t.Fatalf("MCP JSON answer bypassed controlled query: %d %s queries=%d chat=%s", answer.Code, answer.Body.String(), gateway.queries, chat)
	}
	if strings.Contains(answer.Body.String(), "fixture-connector") || strings.Contains(answer.Body.String(), "fixture-agent") {
		t.Fatal("MCP answer exposed gateway binding credentials")
	}
	gateway.queryData = &QueryData{Fields: []string{"amount", "note"}, Data: []map[string]any{{
		"amount": "123", "note": "[open](https://attacker.invalid)|<img src=x>\nnext",
	}}, AuditID: 1}
	streamChat := start()
	stream := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(streamChat)+`,"question":"sales?"}`, "")
	if stream.Code != http.StatusOK || !strings.HasPrefix(stream.Header().Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(stream.Body.String(), "Record ID:") || !strings.Contains(stream.Body.String(), "| amount | note |") ||
		!strings.Contains(stream.Body.String(), `\[open\](https://attacker.invalid)\|&lt;img src=x&gt; next`) ||
		strings.Contains(stream.Body.String(), "<img") || strings.Contains(stream.Body.String(), `"fields":`) || gateway.queries != 2 {
		t.Fatalf("MCP stream answer did not use audited path: %d %s", stream.Code, stream.Body.String())
	}
	if err := service.Identity.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(chat)+`,"question":"again","stream":false}`, "")
	if denied.Code != http.StatusUnauthorized || gateway.queries != 2 {
		t.Fatalf("revoked token reached controlled query: %d", denied.Code)
	}
}

func TestMCPSelectedWorkspaceKeepsParentTokenRevocable(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	ctx := context.Background()
	user, revision, err := service.Store.User(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Memberships = append(user.Memberships, Membership{Workspace: "2", Name: "Two"})
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	other := p
	other.Workspace = "2"
	other.User = user
	binding := Binding{Datasource: "20", Workspace: "2", User: user.ID, Name: "other", Dialect: "pg", BindingID: "b2", ConnectorToken: "hidden", AgentKey: "hidden"}
	if err := service.Store.put(ctx, bindingKey(other, "20"), binding, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.put(ctx, objectKey{"model", "2", "workspace", "default"}, ModelConfig{ID: "30", Model: "fixture", Endpoint: "http://127.0.0.1/v1"}, 0); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`","oid":"2"}`, "")
	var output struct {
		Token string `json:"access_token"`
		Chat  ID     `json:"chat_id"`
	}
	if started.Code != http.StatusOK || json.Unmarshal(started.Body.Bytes(), &output) != nil || output.Token != token {
		t.Fatalf("workspace switch created an independent token: %d %s", started.Code, started.Body.String())
	}
	answer := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(output.Chat)+`,"question":"sales?","stream":false}`, "")
	if answer.Code != http.StatusOK || gateway.queries != 1 {
		t.Fatalf("selected workspace question failed: %d %s", answer.Code, answer.Body.String())
	}
	if err := service.Identity.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(output.Chat)+`,"question":"again","stream":false}`, "")
	if denied.Code != http.StatusUnauthorized || gateway.queries != 1 {
		t.Fatalf("parent token revoke left switched workspace accessible: %d", denied.Code)
	}
}

func TestMCPQuestionRejectsRevokedWorkspaceMembership(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	ctx := context.Background()
	user, revision, err := service.Store.User(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Memberships = append(user.Memberships, Membership{Workspace: "2", Name: "Two"})
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`","oid":"2"}`, "")
	var output struct {
		Chat ID `json:"chat_id"`
	}
	if started.Code != http.StatusOK || json.Unmarshal(started.Body.Bytes(), &output) != nil || !output.Chat.Valid() {
		t.Fatalf("workspace MCP start: %d %s", started.Code, started.Body.String())
	}
	user, revision, err = service.Store.User(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Memberships = user.Memberships[:1]
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	answer := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(output.Chat)+`,"question":"sales?","stream":false}`, "")
	if answer.Code != http.StatusNotFound || gateway.queries != 0 || gateway.schemaCalls != 0 {
		t.Fatalf("revoked workspace question reached data: %d %s", answer.Code, answer.Body.String())
	}
}

func TestMCPQuestionRejectsAnotherUsersChatBeforeGatewayAccess(t *testing.T) {
	service, _, gateway, _, ownerToken := serviceFixture(t)
	ctx := context.Background()
	other := testUser(t, "2", "other-analyst")
	if err := service.Store.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := service.Identity.Login(ctx, other.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+ownerToken+`"}`, "")
	var output struct {
		Chat ID `json:"chat_id"`
	}
	if started.Code != http.StatusOK || json.Unmarshal(started.Body.Bytes(), &output) != nil || !output.Chat.Valid() {
		t.Fatalf("owner MCP start: %d %s", started.Code, started.Body.String())
	}
	for _, stream := range []bool{false, true} {
		body := `{"token":"` + otherToken + `","chat_id":` + string(output.Chat) + `,"question":"sales?","stream":` + strconv.FormatBool(stream) + `}`
		denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", body, "")
		if denied.Code != http.StatusNotFound || gateway.queries != 0 || gateway.schemaCalls != 0 ||
			strings.Contains(denied.Body.String(), "sales?") || strings.Contains(denied.Body.String(), "123") {
			t.Fatalf("foreign MCP chat reached data, stream=%t: %d %s", stream, denied.Code, denied.Body.String())
		}
	}
}

func TestMCPQuestionReleasesNothingWithoutGatewayAudit(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	gateway.audit = 0
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var output struct {
		Chat ID `json:"chat_id"`
	}
	if started.Code != http.StatusOK || json.Unmarshal(started.Body.Bytes(), &output) != nil {
		t.Fatalf("MCP start: %d %s", started.Code, started.Body.String())
	}
	answer := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(output.Chat)+`,"question":"sales?","stream":false}`, "")
	if answer.Code != http.StatusForbidden || strings.Contains(answer.Body.String(), "123") || gateway.queries != 1 {
		t.Fatalf("unaudited MCP query released data: %d %s", answer.Code, answer.Body.String())
	}
}

func TestMCPQuestionRequiresDatasourceChoiceWhenAmbiguous(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	second := Binding{Datasource: "11", Workspace: p.Workspace, User: p.User.ID, Name: "second", Dialect: "pg", BindingID: "b2", ConnectorToken: "hidden", AgentKey: "hidden"}
	if err := service.Store.put(context.Background(), bindingKey(p, "11"), second, 0); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var output struct {
		Chat ID `json:"chat_id"`
	}
	if started.Code != http.StatusOK || json.Unmarshal(started.Body.Bytes(), &output) != nil {
		t.Fatalf("MCP start: %d %s", started.Code, started.Body.String())
	}
	answer := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(output.Chat)+`,"question":"sales?","stream":false}`, "")
	if answer.Code != http.StatusUnprocessableEntity || gateway.schemaCalls != 0 || gateway.queries != 0 {
		t.Fatalf("ambiguous datasource was guessed: %d %s", answer.Code, answer.Body.String())
	}
	selected := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(output.Chat)+`,"question":"sales?","datasource_id":11,"stream":false}`, "")
	if selected.Code != http.StatusOK || gateway.queries != 1 {
		t.Fatalf("explicit owned datasource was not used: %d %s", selected.Code, selected.Body.String())
	}
}
