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
