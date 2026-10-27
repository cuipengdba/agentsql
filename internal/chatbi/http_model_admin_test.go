package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestWorkspaceModelUpdateKeepsSecretsAndAuditsAtomically(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	key := objectKey{"model", "1", "workspace", "default"}
	var original ModelConfig
	revision, err := service.Store.get(context.Background(), key, &original)
	if err != nil {
		t.Fatal(err)
	}
	original.APIKey = "private-model-key"
	if err := service.Store.put(context.Background(), key, original, revision); err != nil {
		t.Fatal(err)
	}
	if response := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model", "", memberToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("member read model config: %d", response.Code)
	}
	view := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model", "", adminToken)
	if view.Code != http.StatusOK || strings.Contains(view.Body.String(), original.APIKey) || strings.Contains(view.Body.String(), `"api_key"`) || !strings.Contains(view.Body.String(), `"has_api_key":true`) {
		t.Fatalf("unsafe admin model view: %d %s", view.Code, view.Body.String())
	}
	var envelope struct {
		Data struct {
			Revision int64 `json:"revision"`
		} `json:"data"`
	}
	if json.Unmarshal(view.Body.Bytes(), &envelope) != nil || envelope.Data.Revision != revision+1 {
		t.Fatalf("missing model revision: %s", view.Body.String())
	}
	change := `{"revision":` + strconv.FormatInt(envelope.Data.Revision, 10) + `,"name":"Local model","base_model":"fixture-v2","api_domain":"http://127.0.0.1:8000/v1","max_tokens":1024}`
	if response := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/model", change, memberToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("member changed model: %d", response.Code)
	}
	if response := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/model", change, adminToken); response.Code != http.StatusOK {
		t.Fatalf("admin model update: %d %s", response.Code, response.Body.String())
	}
	var updated ModelConfig
	if _, err := service.Store.get(context.Background(), key, &updated); err != nil || updated.ID != original.ID || updated.APIKey != original.APIKey || updated.Model != "fixture-v2" || updated.MaxTokens != 1024 {
		t.Fatalf("model update lost runtime configuration: %+v %v", updated, err)
	}
	if response := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/model", change, adminToken); response.Code != http.StatusConflict {
		t.Fatalf("stale revision accepted: %d", response.Code)
	}
	if response := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/model", `{"revision":`+strconv.FormatInt(envelope.Data.Revision+1, 10)+`,"name":"bad","base_model":"fixture","api_domain":"http://remote.invalid/v1","max_tokens":1024}`, adminToken); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe remote HTTP model accepted: %d %s", response.Code, response.Body.String())
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 1 {
		t.Fatalf("model update did not write one audit: %d %v", len(audits), err)
	}
	rotate := `{"revision":` + strconv.FormatInt(envelope.Data.Revision+1, 10) + `,"name":"Rotated","base_model":"fixture-v3","api_domain":"http://127.0.0.1:8000/v1","api_key":"replacement","max_tokens":1024}`
	if response := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/model", rotate, adminToken); response.Code != http.StatusOK {
		t.Fatalf("model key rotation: %d %s", response.Code, response.Body.String())
	}
	var rotated ModelConfig
	if _, err := service.Store.get(context.Background(), key, &rotated); err != nil || rotated.APIKey != "replacement" || rotated.Model != "fixture-v3" {
		t.Fatalf("model key rotation did not persist: %+v %v", rotated, err)
	}
	if view := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model", "", adminToken); strings.Contains(view.Body.String(), "replacement") {
		t.Fatalf("rotated model key leaked: %s", view.Body.String())
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_model_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	failing := `{"revision":` + strconv.FormatInt(envelope.Data.Revision+2, 10) + `,"name":"Audit failure","base_model":"fixture-v4","api_domain":"http://127.0.0.1:8000/v1","api_key":"must-not-persist","max_tokens":1024}`
	if response := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/model", failing, adminToken); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure allowed model rotation: %d %s", response.Code, response.Body.String())
	}
	var unchanged ModelConfig
	if _, err := service.Store.get(context.Background(), key, &unchanged); err != nil || unchanged.Model != "fixture-v3" || unchanged.APIKey != "replacement" {
		t.Fatalf("audit failure committed model: %+v %v", unchanged, err)
	}
}
