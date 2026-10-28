package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestNativeWorkspaceModelCatalogLifecycle(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	path := "/api/v1/agentsql/models"
	create := `{"name":"Second native model","base_model":"fixture-second","api_domain":"http://127.0.0.1:8001/v1","api_key":"catalog-secret","temperature":0.4}`
	if denied := accountRequest(t, handler, http.MethodPost, path, create, memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member created model: %d", denied.Code)
	}
	if created := accountRequest(t, handler, http.MethodPost, path, create, adminToken); created.Code != http.StatusOK {
		t.Fatalf("native model create: %d %s", created.Code, created.Body.String())
	}
	models, err := service.Store.AdditionalWorkspaceModels(context.Background(), "1")
	if err != nil || len(models) != 1 {
		t.Fatalf("native model missing: %+v %v", models, err)
	}
	id := string(models[0].ID)
	if listed := accountRequest(t, handler, http.MethodGet, path, "", adminToken); listed.Code != http.StatusOK ||
		!strings.Contains(listed.Body.String(), id) || strings.Contains(listed.Body.String(), "catalog-secret") || strings.Contains(listed.Body.String(), "127.0.0.1") {
		t.Fatalf("native catalog leaked or omitted model: %d %s", listed.Code, listed.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodGet, path+"/"+id, "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member read model detail: %d", denied.Code)
	}
	view := accountRequest(t, handler, http.MethodGet, path+"/"+id, "", adminToken)
	if view.Code != http.StatusOK || !strings.Contains(view.Body.String(), `"has_api_key":true`) ||
		!strings.Contains(view.Body.String(), `"temperature":0.4`) || strings.Contains(view.Body.String(), "catalog-secret") {
		t.Fatalf("native model detail leaked or omitted configuration: %d %s", view.Code, view.Body.String())
	}
	var envelope struct {
		Data struct {
			Revision int64 `json:"revision"`
		} `json:"data"`
	}
	if json.Unmarshal(view.Body.Bytes(), &envelope) != nil || envelope.Data.Revision != 1 {
		t.Fatalf("native model revision missing: %s", view.Body.String())
	}
	change := `{"revision":1,"name":"Second model edited","base_model":"fixture-second","api_domain":"http://127.0.0.1:8002/v1","temperature":0.2}`
	if changed := accountRequest(t, handler, http.MethodPut, path+"/"+id, change, adminToken); changed.Code != http.StatusOK {
		t.Fatalf("native model edit: %d %s", changed.Code, changed.Body.String())
	}
	if stale := accountRequest(t, handler, http.MethodPut, path+"/"+id, change, adminToken); stale.Code != http.StatusConflict {
		t.Fatalf("stale native model edit accepted: %d", stale.Code)
	}
	if missing := accountRequest(t, handler, http.MethodPut, path+"/999999/default", "", adminToken); missing.Code != http.StatusNotFound {
		t.Fatalf("foreign model selected: %d", missing.Code)
	}
	if switched := accountRequest(t, handler, http.MethodPut, path+"/"+id+"/default", "", adminToken); switched.Code != http.StatusOK {
		t.Fatalf("native default switch: %d %s", switched.Code, switched.Body.String())
	}
	if protected := accountRequest(t, handler, http.MethodDelete, path+"/"+id, "", adminToken); protected.Code != http.StatusNotFound {
		t.Fatalf("active model deleted through secondary endpoint: %d", protected.Code)
	}
	var parked ModelConfig
	defaultKey := objectKey{"model", "1", "workspace", "default"}
	var active ModelConfig
	if _, err := service.Store.get(context.Background(), defaultKey, &active); err != nil || string(active.ID) != id || active.APIKey != "catalog-secret" {
		t.Fatalf("native default switch lost active secret: %+v %v", active, err)
	}
	others, err := service.Store.AdditionalWorkspaceModels(context.Background(), "1")
	if err != nil || len(others) != 1 || string(others[0].ID) == id {
		t.Fatalf("previous default was not parked: %+v %v", others, err)
	}
	parked = others[0]
	if restored := accountRequest(t, handler, http.MethodPut, path+"/"+string(parked.ID)+"/default", "", adminToken); restored.Code != http.StatusOK {
		t.Fatalf("native default restore: %d %s", restored.Code, restored.Body.String())
	}
	if deleted := accountRequest(t, handler, http.MethodDelete, path+"/"+id, "", adminToken); deleted.Code != http.StatusOK {
		t.Fatalf("native secondary delete: %d %s", deleted.Code, deleted.Body.String())
	}
}
