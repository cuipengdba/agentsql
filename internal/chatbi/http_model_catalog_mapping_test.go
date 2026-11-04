package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestActiveCatalogOriginalMappingRoutesShareModelAndKeepDefault(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	model := ModelConfig{ID: "30", Name: "Shared", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "private-secret"}
	activateModelCatalogFixture(t, service.Store, model)
	for _, workspace := range []workspaceOption{{ID: "2", Name: "Two"}, {ID: "3", Name: "Three"}} {
		if err := service.Store.put(ctx, objectKey{"workspace", "system", "system", string(workspace.ID)}, workspace, 0); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/system/aimodel/30/ws_mapping"
	request := func(method, body, token string, want int, data string) {
		t.Helper()
		response := accountRequest(t, handler, method, path, body, token)
		if response.Code != want || data != "" && !strings.Contains(response.Body.String(), data) ||
			strings.Contains(response.Body.String(), model.APIKey) {
			t.Fatalf("%s mapping: status=%d body=%s", method, response.Code, response.Body.String())
		}
	}
	request(http.MethodGet, "", memberToken, http.StatusUnauthorized, "")
	request(http.MethodGet, "", adminToken, http.StatusOK, `"data":[]`)
	request(http.MethodPut, `["1","2"]`, adminToken, http.StatusOK, `"data":["1","2"]`)
	request(http.MethodPost, `["3"]`, adminToken, http.StatusOK, `"data":["1","2","3"]`)
	request(http.MethodDelete, `["1","2","3"]`, adminToken, http.StatusOK, `"data":[]`)
	request(http.MethodGet, "", adminToken, http.StatusOK, `"data":[]`)
	request(http.MethodPost, `["2"]`, memberToken, http.StatusUnauthorized, "")
	request(http.MethodPost, `["999"]`, adminToken, http.StatusNotFound, "")
	if resolved, _, err := service.Store.modelForWorkspace(ctx, "3"); err != nil || resolved.ID != model.ID {
		t.Fatalf("global default lost after explicit mappings removed: %+v %v", resolved, err)
	}
}

func TestActiveCatalogOriginalAdminListAndDetail(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	defaultModel := ModelConfig{ID: "30", Name: "Default", Model: "fixture-default", Endpoint: "https://example.invalid/v1", APIKey: "default-secret"}
	activateModelCatalogFixture(t, service.Store, defaultModel)
	other := ModelConfig{ID: "31", Name: "Other", Model: "fixture-other", Endpoint: "https://example.invalid/v1", APIKey: "other-secret"}
	if err := service.Store.put(ctx, modelCatalogKey(other.ID), other, 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/system/aimodel", "/api/v1/system/aimodel/30"} {
		if denied := accountRequest(t, handler, http.MethodGet, path, "", memberToken); denied.Code != http.StatusUnauthorized {
			t.Fatalf("member read catalog %s: %d", path, denied.Code)
		}
	}
	list := accountRequest(t, handler, http.MethodGet, "/api/v1/system/aimodel", "", adminToken)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"ws_mapping_count":0`) ||
		!strings.Contains(list.Body.String(), `"default_model":true`) ||
		!strings.Contains(list.Body.String(), string(other.ID)) ||
		strings.Contains(list.Body.String(), defaultModel.APIKey) || strings.Contains(list.Body.String(), other.APIKey) {
		t.Fatalf("active global list: %d %s", list.Code, list.Body.String())
	}
	detail := accountRequest(t, handler, http.MethodGet, "/api/v1/system/aimodel/31", "", adminToken)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"default_model":false`) ||
		!strings.Contains(detail.Body.String(), `"has_api_key":true`) ||
		strings.Contains(detail.Body.String(), other.APIKey) {
		t.Fatalf("active model detail: %d %s", detail.Code, detail.Body.String())
	}
	missing := accountRequest(t, handler, http.MethodGet, "/api/v1/system/aimodel/999", "", adminToken)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown global model: %d %s", missing.Code, missing.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/aimodel/31", "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member deleted global model: %d", denied.Code)
	}
	if blocked := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/aimodel/30", "", adminToken); blocked.Code != http.StatusConflict {
		t.Fatalf("default global model deleted: %d", blocked.Code)
	}
	if deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/aimodel/31", "", adminToken); deleted.Code != http.StatusOK {
		t.Fatalf("secondary global model delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if gone := accountRequest(t, handler, http.MethodGet, "/api/v1/system/aimodel/31", "", adminToken); gone.Code != http.StatusNotFound {
		t.Fatalf("deleted global model still visible: %d", gone.Code)
	}
}

func TestActiveCatalogOriginalDefaultSwitchAndBootstrap(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	first := ModelConfig{ID: "30", Model: "first", Endpoint: "https://example.invalid/v1"}
	second := ModelConfig{ID: "31", Model: "second", Endpoint: "https://example.invalid/v1"}
	activateModelCatalogFixture(t, service.Store, first)
	if err := service.Store.put(ctx, modelCatalogKey(second.ID), second, 0); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/system/aimodel/default/31"
	if denied := accountRequest(t, handler, http.MethodPut, path, "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member switched global default: %d", denied.Code)
	}
	if missing := accountRequest(t, handler, http.MethodPut, "/api/v1/system/aimodel/default/999", "", adminToken); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown global default accepted: %d", missing.Code)
	}
	if switched := accountRequest(t, handler, http.MethodPut, path, "", adminToken); switched.Code != http.StatusOK {
		t.Fatalf("global default switch: %d %s", switched.Code, switched.Body.String())
	}
	if bootstrap := accountRequest(t, handler, http.MethodGet, "/api/v1/system/aimodel/default", "", memberToken); bootstrap.Code != http.StatusOK {
		t.Fatalf("browser failed to find global default: %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	if selected, _, err := service.Store.modelForWorkspace(ctx, "2"); err != nil || selected.ID != second.ID {
		t.Fatalf("other workspace did not inherit new default: %+v %v", selected, err)
	}
	status := accountRequest(t, handler, http.MethodPost, "/api/v1/system/aimodel/status", `{"id":"31"}`, adminToken)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "Model reachable") {
		t.Fatalf("active catalog model probe used a legacy local model: %d %s", status.Code, status.Body.String())
	}
}

func TestActiveCatalogOriginalModelEditRotatesSharedCredential(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	model := ModelConfig{ID: "30", Name: "Before", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "old-secret"}
	activateModelCatalogFixture(t, service.Store, model)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = privateKey
	encrypt := func(value string) string {
		t.Helper()
		ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &privateKey.PublicKey, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(ciphertext)
	}
	payload := map[string]any{"id": model.ID, "name": "After", "model_type": 0,
		"base_model": "fixture", "supplier": 15, "protocol": 1, "default_model": true,
		"api_domain": encrypt(model.Endpoint), "api_key": "", "config_list": []any{}}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/system/aimodel"
	if denied := accountRequest(t, handler, http.MethodPut, path, string(body), memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member edited shared model: %d", denied.Code)
	}
	if updated := accountRequest(t, handler, http.MethodPut, path, string(body), adminToken); updated.Code != http.StatusOK {
		t.Fatalf("original edit failed: %d %s", updated.Code, updated.Body.String())
	}
	payload["api_key"] = encrypt("new-secret")
	body, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if rotated := accountRequest(t, handler, http.MethodPut, path, string(body), adminToken); rotated.Code != http.StatusOK {
		t.Fatalf("original credential rotation failed: %d %s", rotated.Code, rotated.Body.String())
	}
	var saved ModelConfig
	if _, err := service.Store.get(context.Background(), modelCatalogKey(model.ID), &saved); err != nil ||
		saved.Name != "After" || saved.APIKey != "new-secret" {
		t.Fatalf("shared model not updated: %+v %v", saved, err)
	}
	read := accountRequest(t, handler, http.MethodGet, path+"/30", "", adminToken)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"has_api_key":true`) ||
		strings.Contains(read.Body.String(), "new-secret") {
		t.Fatalf("edited model response leaked key: %d %s", read.Code, read.Body.String())
	}
}

func TestActiveCatalogOriginalCreateStartsWithEmptyListAndFirstDefault(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	if err := service.Store.put(context.Background(), modelCatalogActiveKey(),
		modelCatalogActivation{Version: 1, SourceDigest: strings.Repeat("a", 64)}, 0); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/system/aimodel"
	if empty := accountRequest(t, handler, http.MethodGet, path, "", adminToken); empty.Code != http.StatusOK ||
		!strings.Contains(empty.Body.String(), `"data":[]`) {
		t.Fatalf("empty active catalog did not list: %d %s", empty.Code, empty.Body.String())
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = privateKey
	encrypt := func(value string) string {
		t.Helper()
		ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &privateKey.PublicKey, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(ciphertext)
	}
	create := func(name string, token string) (int, string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": "", "name": name, "model_type": 0,
			"base_model": "fixture", "supplier": 15, "protocol": 1,
			"api_domain": encrypt("https://example.invalid/v1"), "api_key": encrypt("private-secret"), "config_list": []any{}})
		if err != nil {
			t.Fatal(err)
		}
		response := accountRequest(t, handler, http.MethodPost, path, string(body), token)
		return response.Code, response.Body.String()
	}
	if code, _ := create("First", memberToken); code != http.StatusUnauthorized {
		t.Fatalf("member created global model: %d", code)
	}
	firstCode, firstBody := create("First", adminToken)
	if firstCode != http.StatusOK {
		t.Fatalf("first global model create: %d", firstCode)
	}
	secondCode, secondBody := create("Second", adminToken)
	if secondCode != http.StatusOK {
		t.Fatalf("second global model create: %d", secondCode)
	}
	var firstResult, secondResult struct {
		Data struct {
			ID           string `json:"id"`
			DefaultModel bool   `json:"default_model"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(firstBody), &firstResult) != nil ||
		json.Unmarshal([]byte(secondBody), &secondResult) != nil ||
		firstResult.Data.ID == "" || secondResult.Data.ID == "" ||
		firstResult.Data.ID == secondResult.Data.ID || !firstResult.Data.DefaultModel ||
		secondResult.Data.DefaultModel || strings.Contains(firstBody+secondBody, "private-secret") {
		t.Fatalf("global create responses lost distinct IDs or leaked a key")
	}
	list := accountRequest(t, handler, http.MethodGet, path, "", adminToken)
	if list.Code != http.StatusOK || strings.Count(list.Body.String(), `"ws_mapping_count":0`) != 2 ||
		strings.Count(list.Body.String(), `"default_model":true`) != 1 ||
		strings.Contains(list.Body.String(), "private-secret") {
		t.Fatalf("original model create parity: %d %s", list.Code, list.Body.String())
	}
}

func TestActiveCatalogWorkspacePickerUsesExplicitMappings(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	defaultModel := ModelConfig{ID: "30", Name: "Global", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	other := ModelConfig{ID: "31", Name: "Mapped", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	activateModelCatalogFixture(t, service.Store, defaultModel)
	if err := service.Store.put(ctx, modelCatalogKey(other.ID), other, 0); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/system/aimodel/list/by_ws"
	mcpPath := "/api/v1/mcp/mcp_model_list"
	mcpBody := `{"token":"` + adminToken + `","oid":1}`
	if denied := accountRequest(t, handler, http.MethodGet, path, "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member read admin model picker: %d", denied.Code)
	}
	if empty := accountRequest(t, handler, http.MethodGet, path, "", adminToken); empty.Code != http.StatusOK ||
		!strings.Contains(empty.Body.String(), `"data":[]`) {
		t.Fatalf("implicit global default leaked into explicit picker: %d %s", empty.Code, empty.Body.String())
	}
	if empty := accountRequest(t, handler, http.MethodPost, mcpPath, mcpBody, ""); empty.Code != http.StatusOK ||
		!strings.Contains(empty.Body.String(), `"data":[]`) {
		t.Fatalf("implicit global default leaked into MCP picker: %d %s", empty.Code, empty.Body.String())
	}
	for _, id := range []ID{defaultModel.ID, other.ID} {
		mapping := accountRequest(t, handler, http.MethodPost, "/api/v1/system/aimodel/"+string(id)+"/ws_mapping", `["1"]`, adminToken)
		if mapping.Code != http.StatusOK {
			t.Fatalf("map model %s: %d %s", id, mapping.Code, mapping.Body.String())
		}
	}
	list := accountRequest(t, handler, http.MethodGet, path, "", adminToken)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"30"`) ||
		!strings.Contains(list.Body.String(), `"id":"31"`) ||
		!strings.Contains(list.Body.String(), `"default_model":true`) {
		t.Fatalf("explicit model picker: %d %s", list.Code, list.Body.String())
	}
	mcpList := accountRequest(t, handler, http.MethodPost, mcpPath, mcpBody, "")
	if mcpList.Code != http.StatusOK || !strings.Contains(mcpList.Body.String(), `"id":30`) ||
		!strings.Contains(mcpList.Body.String(), `"id":31`) {
		t.Fatalf("explicit MCP model picker: %d %s", mcpList.Code, mcpList.Body.String())
	}
	removed := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/aimodel/30/ws_mapping", `["1"]`, adminToken)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove default explicit mapping: %d %s", removed.Code, removed.Body.String())
	}
	list = accountRequest(t, handler, http.MethodGet, path, "", adminToken)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), `"id":"30"`) ||
		!strings.Contains(list.Body.String(), `"id":"31"`) {
		t.Fatalf("removed mapping stayed in picker: %d %s", list.Code, list.Body.String())
	}
	mcpList = accountRequest(t, handler, http.MethodPost, mcpPath, mcpBody, "")
	if mcpList.Code != http.StatusOK || strings.Contains(mcpList.Body.String(), `"id":30`) ||
		!strings.Contains(mcpList.Body.String(), `"id":31`) {
		t.Fatalf("removed mapping stayed in MCP picker: %d %s", mcpList.Code, mcpList.Body.String())
	}
}

func TestActiveCatalogNativeModelPageUsesGlobalRecords(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	first := ModelConfig{ID: "30", Name: "First", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	second := ModelConfig{ID: "31", Name: "Second", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	if status := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model/catalog-status", "", adminToken); status.Code != http.StatusOK ||
		!strings.Contains(status.Body.String(), `"active":false`) {
		t.Fatalf("native page missed inactive catalog: %d %s", status.Code, status.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model/catalog-status", "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member inspected catalog status: %d", denied.Code)
	}
	activateModelCatalogFixture(t, service.Store, first)
	if status := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model/catalog-status", "", adminToken); status.Code != http.StatusOK ||
		!strings.Contains(status.Body.String(), `"active":true`) {
		t.Fatalf("native page missed active catalog: %d %s", status.Code, status.Body.String())
	}
	if err := service.Store.put(ctx, modelCatalogKey(second.ID), second, 0); err != nil {
		t.Fatal(err)
	}
	if denied := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/models", "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member read native global models: %d", denied.Code)
	}
	list := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/models", "", adminToken)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"30"`) ||
		!strings.Contains(list.Body.String(), `"id":"31"`) {
		t.Fatalf("native page still listed legacy models: %d %s", list.Code, list.Body.String())
	}
	if switched := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/models/31/default", "", adminToken); switched.Code != http.StatusOK {
		t.Fatalf("native global default switch: %d %s", switched.Code, switched.Body.String())
	}
	defaultRead := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model", "", adminToken)
	if defaultRead.Code != http.StatusOK || !strings.Contains(defaultRead.Body.String(), `"id":"31"`) {
		t.Fatalf("native default still read legacy model: %d %s", defaultRead.Code, defaultRead.Body.String())
	}
	update := `{"revision":1,"name":"Edited","base_model":"fixture","api_domain":"https://example.invalid/v1"}`
	if edited := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/model", update, adminToken); edited.Code != http.StatusOK {
		t.Fatalf("native global default edit: %d %s", edited.Code, edited.Body.String())
	}
	detail := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/models/31", "", adminToken)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"name":"Edited"`) ||
		!strings.Contains(detail.Body.String(), `"revision":2`) {
		t.Fatalf("native global model detail: %d %s", detail.Code, detail.Body.String())
	}
	if deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/agentsql/models/30", "", adminToken); deleted.Code != http.StatusOK {
		t.Fatalf("native global secondary delete: %d %s", deleted.Code, deleted.Body.String())
	}
}
