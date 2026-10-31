package chatbi

import (
	"context"
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
