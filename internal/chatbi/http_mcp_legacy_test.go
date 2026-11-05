package chatbi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func decodeMCPData(body []byte, target any) error {
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Msg  json.RawMessage `json:"msg"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if envelope.Code != 0 || len(envelope.Data) == 0 || string(envelope.Msg) != "null" {
		return errors.New("invalid original MCP success envelope")
	}
	return json.Unmarshal(envelope.Data, target)
}

func TestMCPAccessTokenFieldValidationDoesNotEchoPassword(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		wantFields []string
		wantType   string
	}{
		{"both missing", `{}`, []string{"username", "password"}, "missing"},
		{"password missing", `{"username":"analyst"}`, []string{"password"}, "missing"},
		{"username missing", `{"password":"Sensitive-password-1"}`, []string{"username"}, "missing"},
		{"username type", `{"username":1,"password":"Sensitive-password-1"}`, []string{"username"}, "string_type"},
		{"password type", `{"username":"analyst","password":["Sensitive-password-1"]}`, []string{"password"}, "string_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			var input mcpAccessTokenInput
			if readMCPAccessTokenInput(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/mcp/access_token", strings.NewReader(tc.body)), &input) {
				t.Fatal("malformed credentials accepted")
			}
			if recorder.Code != http.StatusUnprocessableEntity || strings.Contains(recorder.Body.String(), "Sensitive-password-1") {
				t.Fatalf("unsafe validation response: %d %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Detail []struct {
					Type string   `json:"type"`
					Loc  []string `json:"loc"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Detail) != len(tc.wantFields) {
				t.Fatalf("invalid validation response: %v %s", err, recorder.Body.String())
			}
			for i, field := range tc.wantFields {
				if response.Detail[i].Type != tc.wantType || !reflect.DeepEqual(response.Detail[i].Loc, []string{"body", field}) {
					t.Fatalf("field %d: %+v", i, response.Detail[i])
				}
			}
		})
	}
}

type mcpLanguageCaptureModel struct{ languages []string }

func (m *mcpLanguageCaptureModel) Generate(ctx context.Context, _ ModelConfig, _, _ string, _ []Column) (SQLGeneration, error) {
	m.languages = append(m.languages, questionLanguage(ctx))
	return SQLGeneration{SQL: "SELECT amount FROM public.sales", TotalTokens: 1}, nil
}

type mcpConcurrentLanguageModel struct {
	arrived chan struct{ question, language string }
	release map[string]chan struct{}
}

func (m *mcpConcurrentLanguageModel) Generate(ctx context.Context, _ ModelConfig, _, question string, _ []Column) (SQLGeneration, error) {
	m.arrived <- struct{ question, language string }{question, questionLanguage(ctx)}
	select {
	case <-m.release[question]:
		return SQLGeneration{SQL: "SELECT amount FROM public.sales", TotalTokens: 1}, nil
	case <-ctx.Done():
		return SQLGeneration{}, ctx.Err()
	}
}

type mcpSerialGateway struct {
	mu      sync.Mutex
	gateway Gateway
}

func (g *mcpSerialGateway) Schema(ctx context.Context, p Principal, b Binding) ([]Column, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gateway.Schema(ctx, p, b)
}

func (g *mcpSerialGateway) Query(ctx context.Context, p Principal, b Binding, sql string) (QueryData, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gateway.Query(ctx, p, b, sql)
}

func TestMCPQuestionConcurrentLanguageOverridesStayIsolated(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	if err := service.Identity.ChangeLanguage(context.Background(), p, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	service.Gateway = &mcpSerialGateway{gateway: gateway}
	model := &mcpConcurrentLanguageModel{
		arrived: make(chan struct{ question, language string }, 2),
		release: map[string]chan struct{}{"english": make(chan struct{}), "thai": make(chan struct{})},
	}
	service.Model = model
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	startChat := func() ID {
		t.Helper()
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
		var chat struct {
			Chat ID `json:"chat_id"`
		}
		if response.Code != http.StatusOK || decodeMCPData(response.Body.Bytes(), &chat) != nil {
			t.Fatalf("MCP start: %d %s", response.Code, response.Body.String())
		}
		return chat.Chat
	}
	chatEnglish, chatThai := startChat(), startChat()
	type result struct {
		question string
		response *httptest.ResponseRecorder
	}
	finished := make(chan result, 2)
	ask := func(question, language string, chat ID) {
		body := `{"token":"` + token + `","chat_id":` + string(chat) + `,"question":"` + question + `","lang":"` + language + `","stream":true}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/mcp_question", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		finished <- result{question, response}
	}
	go ask("english", "en", chatEnglish)
	go ask("thai", "th-TH", chatThai)
	seen := map[string]string{}
	for range 2 {
		select {
		case event := <-model.arrived:
			seen[event.question] = event.language
		case <-time.After(10 * time.Second):
			t.Fatal("parallel MCP questions did not both reach the model")
		}
	}
	if seen["english"] != "English" || seen["thai"] != "Thai" {
		t.Fatalf("concurrent model languages crossed: %#v", seen)
	}
	close(model.release["thai"])
	close(model.release["english"])
	responses := map[string]*httptest.ResponseRecorder{}
	for range 2 {
		select {
		case done := <-finished:
			responses[done.question] = done.response
		case <-time.After(10 * time.Second):
			t.Fatal("parallel MCP questions did not complete")
		}
	}
	for question, heading := range map[string]string{"english": "> Answer ID: ", "thai": "> รหัสการตอบกลับ: "} {
		response := responses[question]
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), heading) {
			t.Fatalf("%s response language: %d %s", question, response.Code, response.Body.String())
		}
	}
	current, err := service.Identity.Verify(context.Background(), token)
	if err != nil || current.User.Language != "zh-CN" || gateway.queries != 2 {
		t.Fatalf("concurrent language changed stored identity or skipped query: language=%s queries=%d err=%v", current.User.Language, gateway.queries, err)
	}
}

func TestMCPQuestionLanguageOverrideIsRequestScoped(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	if err := service.Identity.ChangeLanguage(context.Background(), p, "en"); err != nil {
		t.Fatal(err)
	}
	model := &mcpLanguageCaptureModel{}
	service.Model = model
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	start := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var chat struct {
		Chat ID `json:"chat_id"`
	}
	if start.Code != http.StatusOK || decodeMCPData(start.Body.Bytes(), &chat) != nil {
		t.Fatalf("MCP start: %d %s", start.Code, start.Body.String())
	}
	for _, tc := range []struct{ field, want, heading string }{
		{"", "Simplified Chinese", "> 响应ID: "},
		{`,"lang":"zh-CN"`, "Simplified Chinese", "> 响应ID: "},
		{`,"lang":"en"`, "English", "> Answer ID: "},
		{`,"lang":"zh-TW"`, "Traditional Chinese", "> 響應ID: "},
		{`,"lang":"ko-KR"`, "Korean", "> 응답 ID: "},
		{`,"lang":"th-TH"`, "Thai", "> รหัสการตอบกลับ: "},
		{`,"lang":null`, "Simplified Chinese", "> 响应ID: "},
		{`,"lang":"unsupported"`, "Simplified Chinese", "> 响应ID: "},
	} {
		body := `{"token":"` + token + `","chat_id":` + string(chat.Chat) + `,"question":"amount?","stream":true` + tc.field + `}`
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", body, "")
		if response.Code != http.StatusOK || len(model.languages) == 0 || model.languages[len(model.languages)-1] != tc.want || !strings.Contains(response.Body.String(), tc.heading) {
			t.Fatalf("language override %q: status=%d got=%v", tc.field, response.Code, model.languages)
		}
	}
	current, err := service.Identity.Verify(context.Background(), token)
	if err != nil || gateway.queries != len(model.languages) || current.User.Language != "en" {
		t.Fatalf("language requests changed identity or skipped query: calls=%d identity=%v", gateway.queries, err)
	}
	if response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", `{"token":"`+token+`","chat_id":`+string(chat.Chat)+`,"question":"amount?","lang":42,"stream":false}`, ""); response.Code != http.StatusUnprocessableEntity || gateway.queries != len(model.languages) {
		t.Fatalf("malformed language reached execution: %d", response.Code)
	}
}

func TestMCPQuestionLanguageDefaultAndUnsupportedFallback(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{"", "zh-CN"},
		{`null`, "zh-CN"},
		{`"invalid"`, "zh-CN"},
		{`"zh-TW"`, "zh-TW"},
	} {
		got, err := mcpQuestionLanguage(json.RawMessage(tc.raw))
		if err != nil || got != tc.want {
			t.Fatalf("raw=%s: got %s, %v", tc.raw, got, err)
		}
	}
	if _, err := mcpQuestionLanguage(json.RawMessage(`42`)); err == nil {
		t.Fatal("non-string language was accepted")
	}
}

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
	if decodeMCPData(login.Body.Bytes(), &response) != nil || response.AccessToken == "" {
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
	original := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list?token="+url.QueryEscape(response.AccessToken), "", "")
	if original.Code != http.StatusOK || original.Body.String() != ws.Body.String() ||
		original.Header().Get("Cache-Control") != "no-store" || original.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("original query-token MCP workspace list: %d %s", original.Code, original.Body.String())
	}
	withNull := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list?token="+url.QueryEscape(response.AccessToken), "null", "")
	if withNull.Code != http.StatusOK || withNull.Body.String() != ws.Body.String() {
		t.Fatalf("original query-token MCP workspace list with null body: %d %s", withNull.Code, withNull.Body.String())
	}
	var workspaceList []map[string]any
	if err := decodeMCPData(original.Body.Bytes(), &workspaceList); err != nil || len(workspaceList) != 1 {
		t.Fatalf("original MCP workspace envelope: %v", err)
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
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list?token="+url.QueryEscape(response.AccessToken), "", ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("revoked query token listed workspaces: %d", denied.Code)
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
	for _, path := range []string{
		"/api/v1/mcp/mcp_ws_list?token=" + url.QueryEscape(token),
		"/api/v1/mcp/mcp_ws_list?token=" + url.QueryEscape(token) + "&token=other",
		"/api/v1/mcp/mcp_ws_list?token=" + url.QueryEscape(token) + "&oid=2",
	} {
		body := `"` + token + `"`
		if denied := accountRequest(t, handler, http.MethodPost, path, body, ""); denied.Code != http.StatusUnprocessableEntity {
			t.Fatalf("ambiguous MCP workspace credentials accepted: %d %s", denied.Code, path)
		}
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list?token="+url.QueryEscape(token)+"&bad=%ZZ", "", ""); denied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed MCP query accepted: %d", denied.Code)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_ws_list?token="+url.QueryEscape(token), "", token); denied.Code != http.StatusUnauthorized {
		t.Fatalf("query and header credentials accepted: %d", denied.Code)
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
	if decodeMCPData(started.Body.Bytes(), &output) != nil || output.Token != token || !output.Chat.Valid() {
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
		if response.Code != http.StatusOK || decodeMCPData(response.Body.Bytes(), &output) != nil {
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
		!strings.Contains(stream.Body.String(), "响应ID:") || !strings.Contains(stream.Body.String(), "| amount | note |") ||
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

func TestMCPQuestionOriginalJSONDataShapeAndFirstTitle(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	gateway.queryData = &QueryData{
		Fields: []string{"amount"}, Data: []map[string]any{{"amount": 123}},
		FieldsInfo: []FieldInfo{{Name: "amount", Numeric: true}}, AuditID: 1,
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	start := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var started struct {
		Chat ID `json:"chat_id"`
	}
	if start.Code != http.StatusOK || decodeMCPData(start.Body.Bytes(), &started) != nil {
		t.Fatalf("MCP start failed: %d", start.Code)
	}
	question := "What is the total amount?"
	path := "/api/v1/mcp/mcp_question"
	first := accountRequest(t, handler, http.MethodPost, path, `{"token":"`+token+`","chat_id":`+string(started.Chat)+`,"question":"`+question+`","return_img":false,"stream":false}`, "")
	var result struct {
		Success bool   `json:"success"`
		Title   string `json:"title"`
		SQL     string `json:"sql"`
		Data    struct {
			Fields     []string         `json:"fields"`
			Rows       []map[string]any `json:"data"`
			FieldsInfo []FieldInfo      `json:"fields_info"`
			SQL        string           `json:"sql"`
			Datasource json.Number      `json:"datasource"`
		} `json:"data"`
		Chart struct {
			Type string `json:"type"`
		} `json:"chart"`
	}
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &result) != nil {
		t.Fatalf("MCP JSON response: %d %s", first.Code, first.Body.String())
	}
	decodedSQL, err := base64.StdEncoding.DecodeString(result.Data.SQL)
	if err != nil || !result.Success || result.Title != "What is the total am" ||
		result.Data.Datasource != "10" || string(decodedSQL) != result.SQL ||
		len(result.Data.Fields) != 1 || len(result.Data.Rows) != 1 || len(result.Data.FieldsInfo) != 1 || !result.Data.FieldsInfo[0].Numeric ||
		result.Chart.Type != "table" || gateway.queries != 1 {
		t.Fatalf("original MCP JSON shape or audited query mismatch: status=%d title=%q datasource=%q chart=%q queries=%d", first.Code, result.Title, result.Data.Datasource, result.Chart.Type, gateway.queries)
	}
	second := accountRequest(t, handler, http.MethodPost, path, `{"token":"`+token+`","chat_id":`+string(started.Chat)+`,"question":"again","stream":false}`, "")
	var followup map[string]any
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &followup) != nil || followup["title"] != nil || gateway.queries != 2 {
		t.Fatalf("follow-up title or query mismatch: %d %s", second.Code, second.Body.String())
	}
	chat, _, err := service.chat(context.Background(), p, started.Chat)
	if err != nil || chat.Brief != result.Title || len(chat.Records) != 2 {
		t.Fatalf("first MCP title did not persist safely: brief=%q records=%d err=%v", chat.Brief, len(chat.Records), err)
	}
}

func TestMCPQuestionOriginalInvalidReferenceStatuses(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	start := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var started struct {
		Chat ID `json:"chat_id"`
	}
	if start.Code != http.StatusOK || decodeMCPData(start.Body.Bytes(), &started) != nil {
		t.Fatalf("MCP start failed: %d", start.Code)
	}
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"inaccessible chat", `{"token":"` + token + `","chat_id":999999999999999999,"question":"synthetic invalid input","stream":false}`, "Chat ID is not exist", http.StatusBadRequest},
		{"invalid datasource string", `{"token":"` + token + `","chat_id":` + string(started.Chat) + `,"datasource_id":"not-a-number","question":"synthetic invalid input","stream":false}`, "Invalid datasource ID", http.StatusBadRequest},
	} {
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", tc.body, "")
		var message string
		if response.Code != tc.status || json.Unmarshal(response.Body.Bytes(), &message) != nil || message != tc.want || gateway.queries != 0 {
			t.Fatalf("%s: status=%d body=%s queries=%d", tc.name, response.Code, response.Body.String(), gateway.queries)
		}
	}
	for _, tc := range []struct{ name, body, want string }{
		{"invalid datasource list", `{"token":"` + token + `","chat_id":` + string(started.Chat) + `,"datasource_id":[],"question":"synthetic invalid input","stream":false}`, `{"detail":[{"type":"string_type","loc":["body","datasource_id","str"],"msg":"Input should be a valid string","input":[]},{"type":"int_type","loc":["body","datasource_id","int"],"msg":"Input should be a valid integer","input":[]}]}`},
		{"invalid chat string", `{"token":"` + token + `","chat_id":"not-a-number","question":"synthetic invalid input","stream":false}`, `{"detail":[{"type":"int_parsing","loc":["body","chat_id"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"not-a-number"}]}`},
	} {
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", tc.body, "")
		var got, want any
		if response.Code != http.StatusUnprocessableEntity || json.Unmarshal(response.Body.Bytes(), &got) != nil || json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(got, want) || gateway.queries != 0 {
			t.Fatalf("%s: status=%d body=%s queries=%d", tc.name, response.Code, response.Body.String(), gateway.queries)
		}
	}
}

func TestMCPQuestionInvalidBodyTokenUsesOriginalForbiddenResponse(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	start := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var started struct {
		Chat ID `json:"chat_id"`
	}
	if start.Code != http.StatusOK || decodeMCPData(start.Body.Bytes(), &started) != nil {
		t.Fatalf("MCP start failed: %d", start.Code)
	}
	for _, tc := range []struct {
		name, body, headerToken string
	}{
		{"invalid body token", `{"token":"invalid","chat_id":` + string(started.Chat) + `,"question":"synthetic invalid token","stream":false}`, ""},
		{"competing header token", `{"token":"` + token + `","chat_id":` + string(started.Chat) + `,"question":"synthetic competing token","stream":false}`, token},
	} {
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", tc.body, tc.headerToken)
		var message string
		if response.Code != http.StatusForbidden || json.Unmarshal(response.Body.Bytes(), &message) != nil ||
			message != "Could not validate credentials" || gateway.queries != 0 {
			t.Fatalf("%s: status=%d body=%s queries=%d", tc.name, response.Code, response.Body.String(), gateway.queries)
		}
	}
}

func TestMCPQuestionOriginalMalformedFieldValidation(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	start := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var started struct {
		Chat ID `json:"chat_id"`
	}
	if start.Code != http.StatusOK || decodeMCPData(start.Body.Bytes(), &started) != nil {
		t.Fatalf("MCP start failed: %d", start.Code)
	}
	base := `{"token":"` + token + `","chat_id":` + string(started.Chat) + `,`
	for _, tc := range []struct{ name, body, want string }{
		{"null question", base + `"question":null}`, `{"detail":[{"type":"string_type","loc":["body","question"],"msg":"Input should be a valid string","input":null}]}`},
		{"numeric question", base + `"question":77}`, `{"detail":[{"type":"string_type","loc":["body","question"],"msg":"Input should be a valid string","input":77}]}`},
		{"array question", base + `"question":["x"]}`, `{"detail":[{"type":"string_type","loc":["body","question"],"msg":"Input should be a valid string","input":["x"]}]}`},
		{"null chat", `{"token":"` + token + `","chat_id":null,"question":"synthetic invalid"}`, `{"detail":[{"type":"int_type","loc":["body","chat_id"],"msg":"Input should be a valid integer","input":null}]}`},
		{"array chat", `{"token":"` + token + `","chat_id":[],"question":"synthetic invalid"}`, `{"detail":[{"type":"int_type","loc":["body","chat_id"],"msg":"Input should be a valid integer","input":[]}]}`},
		{"object datasource", base + `"datasource_id":{},"question":"synthetic invalid"}`, `{"detail":[{"type":"string_type","loc":["body","datasource_id","str"],"msg":"Input should be a valid string","input":{}},{"type":"int_type","loc":["body","datasource_id","int"],"msg":"Input should be a valid integer","input":{}}]}`},
		{"array stream", base + `"question":"synthetic invalid","stream":[]}`, `{"detail":[{"type":"bool_type","loc":["body","stream"],"msg":"Input should be a valid boolean","input":[]}]}`},
		{"numeric token", `{"token":7,"chat_id":` + string(started.Chat) + `,"question":"synthetic invalid"}`, `{"detail":[{"type":"string_type","loc":["body","token"],"msg":"Input should be a valid string","input":7}]}`},
	} {
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", tc.body, "")
		var got, want any
		if response.Code != http.StatusUnprocessableEntity || json.Unmarshal(response.Body.Bytes(), &got) != nil || json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(got, want) || gateway.queries != 0 {
			t.Fatalf("%s: status=%d body=%s queries=%d", tc.name, response.Code, response.Body.String(), gateway.queries)
		}
	}
	missing := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", base+`"stream":false}`, "")
	if missing.Code != http.StatusUnprocessableEntity || !strings.Contains(missing.Body.String(), `"type":"missing"`) || strings.Contains(missing.Body.String(), token) || gateway.queries != 0 {
		t.Fatalf("missing question disclosed credentials or queried gateway: status=%d body=%s queries=%d", missing.Code, missing.Body.String(), gateway.queries)
	}
}

func TestMCPStartOriginalMalformedFieldValidation(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"null token", `{"token":null}`, `{"detail":[{"type":"string_type","loc":["body","token"],"msg":"Input should be a valid string","input":null}]}`},
		{"numeric token", `{"token":3}`, `{"detail":[{"type":"string_type","loc":["body","token"],"msg":"Input should be a valid string","input":3}]}`},
		{"array token", `{"token":["x"]}`, `{"detail":[{"type":"string_type","loc":["body","token"],"msg":"Input should be a valid string","input":["x"]}]}`},
		{"numeric oid", `{"token":"` + token + `","oid":3}`, `{"detail":[{"type":"string_type","loc":["body","oid"],"msg":"Input should be a valid string","input":3}]}`},
		{"boolean oid", `{"token":"` + token + `","oid":true}`, `{"detail":[{"type":"string_type","loc":["body","oid"],"msg":"Input should be a valid string","input":true}]}`},
		{"numeric username", `{"username":3,"password":"x"}`, `{"detail":[{"type":"string_type","loc":["body","username"],"msg":"Input should be a valid string","input":3}]}`},
		{"numeric password", `{"username":"x","password":3}`, `{"detail":[{"type":"string_type","loc":["body","password"],"msg":"Input should be a valid string","input":3}]}`},
	} {
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", tc.body, "")
		var got, want any
		if response.Code != http.StatusUnprocessableEntity || json.Unmarshal(response.Body.Bytes(), &got) != nil ||
			json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(got, want) || gateway.queries != 0 {
			t.Fatalf("%s: status=%d body did not match original field error; queries=%d", tc.name, response.Code, gateway.queries)
		}
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
	if started.Code != http.StatusOK || decodeMCPData(started.Body.Bytes(), &output) != nil || output.Token != token {
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
	if started.Code != http.StatusOK || decodeMCPData(started.Body.Bytes(), &output) != nil || !output.Chat.Valid() {
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
	for _, stream := range []bool{false, true} {
		body := `{"token":"` + token + `","chat_id":` + string(output.Chat) + `,"question":"sales?","stream":` + strconv.FormatBool(stream) + `}`
		answer := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", body, "")
		var message string
		if answer.Code != http.StatusBadRequest || json.Unmarshal(answer.Body.Bytes(), &message) != nil || message != "Chat ID is not exist" ||
			gateway.queries != 0 || gateway.schemaCalls != 0 ||
			strings.Contains(answer.Body.String(), "123") {
			t.Fatalf("revoked workspace question reached data, stream=%t: %d %s", stream, answer.Code, answer.Body.String())
		}
	}
}

func TestMCPQuestionRevokedWorkspaceDuringModelPreventsQuery(t *testing.T) {
	service, p, gateway, model, token := serviceFixture(t)
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
	binding := Binding{Datasource: "20", Workspace: "2", User: user.ID, Name: "other", Dialect: "pg",
		BindingID: "b2", ConnectorToken: "hidden", AgentKey: "hidden"}
	if err := service.Store.put(ctx, bindingKey(other, "20"), binding, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.put(ctx, objectKey{"model", "2", "workspace", "default"},
		ModelConfig{ID: "30", Model: "fixture", Endpoint: "http://127.0.0.1/v1"}, 0); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start",
		`{"token":"`+token+`","oid":"2"}`, "")
	var output struct {
		Chat ID `json:"chat_id"`
	}
	if started.Code != http.StatusOK || decodeMCPData(started.Body.Bytes(), &output) != nil || !output.Chat.Valid() {
		t.Fatalf("workspace MCP start: %d %s", started.Code, started.Body.String())
	}
	var revokeErr error
	model.after = func() {
		current, currentRevision, err := service.Store.User(ctx, p.User.ID)
		if err != nil {
			revokeErr = err
			return
		}
		current.Memberships = current.Memberships[:1]
		revokeErr = service.Store.put(ctx, userKey(current.ID), current, currentRevision)
	}
	answer := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question",
		`{"token":"`+token+`","chat_id":`+string(output.Chat)+`,"question":"sales?","stream":false}`, "")
	if revokeErr != nil || answer.Code == http.StatusOK || gateway.queries != 0 ||
		strings.Contains(answer.Body.String(), "123") {
		t.Fatalf("MCP model-time workspace revocation released data: %d %s, revoke=%v", answer.Code, answer.Body.String(), revokeErr)
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
	if started.Code != http.StatusOK || decodeMCPData(started.Body.Bytes(), &output) != nil || !output.Chat.Valid() {
		t.Fatalf("owner MCP start: %d %s", started.Code, started.Body.String())
	}
	for _, stream := range []bool{false, true} {
		body := `{"token":"` + otherToken + `","chat_id":` + string(output.Chat) + `,"question":"sales?","stream":` + strconv.FormatBool(stream) + `}`
		denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question", body, "")
		var message string
		if denied.Code != http.StatusBadRequest || json.Unmarshal(denied.Body.Bytes(), &message) != nil || message != "Chat ID is not exist" ||
			gateway.queries != 0 || gateway.schemaCalls != 0 ||
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
	if started.Code != http.StatusOK || decodeMCPData(started.Body.Bytes(), &output) != nil {
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
	if started.Code != http.StatusOK || decodeMCPData(started.Body.Bytes(), &output) != nil {
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
