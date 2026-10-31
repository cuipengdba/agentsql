package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyModelReadIsScopedAndRedacted(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	key := objectKey{"model", "1", "workspace", "default"}
	var model ModelConfig
	revision, err := service.Store.get(context.Background(), key, &model)
	if err != nil {
		t.Fatal(err)
	}
	model.Name = "Finance local"
	model.APIKey = "private-model-secret"
	if err := service.Store.put(context.Background(), key, model, revision); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/system/aimodel"
	if denied := accountRequest(t, handler, http.MethodGet, base, "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member model catalog read: %d", denied.Code)
	}
	listed := accountRequest(t, handler, http.MethodGet, base+"?keyword=finance", "", adminToken)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), string(model.ID)) ||
		!strings.Contains(listed.Body.String(), `"supplier":15`) || !strings.Contains(listed.Body.String(), `"model_type":0`) ||
		strings.Contains(listed.Body.String(), model.APIKey) || strings.Contains(listed.Body.String(), model.Endpoint) {
		t.Fatalf("unsafe model catalog: %d %s", listed.Code, listed.Body.String())
	}
	missing := accountRequest(t, handler, http.MethodGet, base+"?keyword=other", "", adminToken)
	if missing.Code != http.StatusOK || !strings.Contains(missing.Body.String(), `"data":[]`) {
		t.Fatalf("keyword filter: %d %s", missing.Code, missing.Body.String())
	}
	if other := accountRequest(t, handler, http.MethodGet, base+"/999999", "", adminToken); other.Code != http.StatusNotFound {
		t.Fatalf("foreign model detail: %d", other.Code)
	}
	detail := accountRequest(t, handler, http.MethodGet, base+"/"+string(model.ID), "", adminToken)
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), model.APIKey) ||
		!strings.Contains(detail.Body.String(), `"supplier":15`) || !strings.Contains(detail.Body.String(), `"model_type":0`) ||
		!strings.Contains(detail.Body.String(), `"has_api_key":true`) || !strings.Contains(detail.Body.String(), model.Endpoint) {
		t.Fatalf("unsafe model detail: %d %s", detail.Code, detail.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodDelete, base+"/"+string(model.ID), "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member deleted model: %d", denied.Code)
	}
	if other := accountRequest(t, handler, http.MethodDelete, base+"/999999", "", adminToken); other.Code != http.StatusNotFound {
		t.Fatalf("foreign model deletion: %d", other.Code)
	}
	if blocked := accountRequest(t, handler, http.MethodDelete, base+"/"+string(model.ID), "", adminToken); blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), "Default model cannot be deleted") {
		t.Fatalf("default model deletion: %d %s", blocked.Code, blocked.Body.String())
	}
	var retained ModelConfig
	if _, err := service.Store.get(context.Background(), key, &retained); err != nil || retained.APIKey != model.APIKey {
		t.Fatalf("blocked delete lost model or key: %v", err)
	}
	mappingPath := base + "/" + string(model.ID) + "/ws_mapping"
	if denied := accountRequest(t, handler, http.MethodGet, mappingPath, "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member read model mappings: %d", denied.Code)
	}
	if other := accountRequest(t, handler, http.MethodGet, base+"/999999/ws_mapping", "", adminToken); other.Code != http.StatusNotFound {
		t.Fatalf("foreign model mapping read: %d", other.Code)
	}
	mappings := accountRequest(t, handler, http.MethodGet, mappingPath, "", adminToken)
	if mappings.Code != http.StatusOK || !strings.Contains(mappings.Body.String(), `"data":["1"]`) || strings.Contains(mappings.Body.String(), model.APIKey) {
		t.Fatalf("incorrect or unsafe model mapping: %d %s", mappings.Code, mappings.Body.String())
	}
	if same := accountRequest(t, handler, http.MethodPut, mappingPath, `["1"]`, adminToken); same.Code != http.StatusOK {
		t.Fatalf("unchanged model mapping: %d %s", same.Code, same.Body.String())
	}
	for _, body := range []string{`[]`, `["1","2"]`, `["2"]`, `["1","1"]`} {
		if blocked := accountRequest(t, handler, http.MethodPut, mappingPath, body, adminToken); blocked.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unsupported mapping accepted: %s %d %s", body, blocked.Code, blocked.Body.String())
		}
	}
	if duplicate := accountRequest(t, handler, http.MethodPost, mappingPath, `["1"]`, adminToken); duplicate.Code != http.StatusOK ||
		!strings.Contains(duplicate.Body.String(), `"data":["1"]`) {
		t.Fatalf("duplicate mapping add: %d %s", duplicate.Code, duplicate.Body.String())
	}
	if blocked := accountRequest(t, handler, http.MethodPost, mappingPath, `["2"]`, adminToken); blocked.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(blocked.Body.String(), "Cross-workspace model sharing is unavailable") {
		t.Fatalf("cross-workspace model sharing accepted: %d %s", blocked.Code, blocked.Body.String())
	}
	if blocked := accountRequest(t, handler, http.MethodDelete, mappingPath, `["1"]`, adminToken); blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), "Cannot remove the workspace's only model mapping") {
		t.Fatalf("sole model mapping removed: %d %s", blocked.Code, blocked.Body.String())
	}
	if other := accountRequest(t, handler, http.MethodPost, base+"/999999/ws_mapping", `["1"]`, adminToken); other.Code != http.StatusNotFound {
		t.Fatalf("foreign model mapping changed: %d", other.Code)
	}
	if denied := accountRequest(t, handler, http.MethodPost, mappingPath, `["1"]`, memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member changed model mapping: %d", denied.Code)
	}
	if still := accountRequest(t, handler, http.MethodGet, mappingPath, "", adminToken); still.Code != http.StatusOK ||
		!strings.Contains(still.Body.String(), `"data":["1"]`) {
		t.Fatalf("mapping mutation changed current workspace: %d %s", still.Code, still.Body.String())
	}
	if _, err := service.Store.db.ExecContext(context.Background(),
		`DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID); err != nil {
		t.Fatal(err)
	}
	empty := accountRequest(t, handler, http.MethodGet, base, "", adminToken)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"data":[]`) {
		t.Fatalf("empty model catalog: %d %s", empty.Code, empty.Body.String())
	}
}

func TestOriginalModelStatusProbesSavedModelWithoutQueryExecution(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	var current ModelConfig
	if _, err := service.Store.get(context.Background(), objectKey{"model", "1", "workspace", "default"}, &current); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + string(current.ID) + `","api_domain":"http://attacker.invalid/v1","api_key":"attacker","base_model":"forged"}`
	path := "/api/v1/system/aimodel/status"
	if denied := accountRequest(t, handler, http.MethodPost, path, body, memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member model probe: %d", denied.Code)
	}
	response := accountRequest(t, handler, http.MethodPost, path, body, adminToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "application/x-ndjson") ||
		!strings.Contains(response.Body.String(), `"content":"Model reachable"`) || strings.Contains(response.Body.String(), "SELECT") {
		t.Fatalf("original model status stream: %d %s", response.Code, response.Body.String())
	}
	model := service.Model.(*fakeModel)
	gateway := service.Gateway.(*fakeGateway)
	if model.calls != 1 || gateway.queries != 0 {
		t.Fatalf("unsafe model probe calls: model=%d business_queries=%d", model.calls, gateway.queries)
	}
	if missing := accountRequest(t, handler, http.MethodPost, path, `{"id":"999999"}`, adminToken); missing.Code != http.StatusNotFound || model.calls != 1 {
		t.Fatalf("foreign model probe reached model: %d calls=%d", missing.Code, model.calls)
	}
	model.err = errors.New("upstream failure with secret")
	failed := accountRequest(t, handler, http.MethodPost, path, body, adminToken)
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"error":"Model unavailable"`) ||
		strings.Contains(failed.Body.String(), "secret") || gateway.queries != 0 {
		t.Fatalf("unsafe model failure stream: %d %s", failed.Code, failed.Body.String())
	}
}

func TestOriginalModelEditUsesInstallationRSAAndAuditedWorkspaceModel(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = privateKey
	modelKey := objectKey{"model", "1", "workspace", "default"}
	var original ModelConfig
	revision, err := service.Store.get(context.Background(), modelKey, &original)
	if err != nil {
		t.Fatal(err)
	}
	original.APIKey = "keep-private-key"
	if err := service.Store.put(context.Background(), modelKey, original, revision); err != nil {
		t.Fatal(err)
	}
	encrypt := func(value string) string {
		t.Helper()
		ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &privateKey.PublicKey, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(ciphertext)
	}
	payload := map[string]any{"id": string(original.ID), "name": "Edited through original client",
		"model_type": 0, "base_model": "fixture-v2", "supplier": 15, "protocol": 1,
		"default_model": true, "api_domain": encrypt("http://127.0.0.1:8000/v1"),
		"api_key": "", "config_list": []any{}, "ws_mapping_count": 1}
	request := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return accountRequest(t, handler, http.MethodPut, "/api/v1/system/aimodel", string(body), token)
	}
	if response := request(memberToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("member edited original model: %d", response.Code)
	}
	payload["supplier"] = 7
	if response := request(adminToken); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported supplier edited model: %d", response.Code)
	}
	payload["supplier"] = 15
	payload["config_list"] = []any{map[string]any{"key": "temperature", "val": 1}}
	if response := request(adminToken); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported advanced model option accepted: %d", response.Code)
	}
	payload["config_list"] = []any{}
	payload["api_domain"] = "http://127.0.0.1:8000/v1"
	if response := request(adminToken); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("plaintext original endpoint accepted: %d", response.Code)
	}
	payload["api_domain"] = encrypt("http://127.0.0.1:8000/v1")
	payload["id"] = "999999"
	if response := request(adminToken); response.Code != http.StatusNotFound {
		t.Fatalf("foreign model edit accepted: %d", response.Code)
	}
	payload["id"] = string(original.ID)
	if response := request(adminToken); response.Code != http.StatusOK {
		t.Fatalf("original model edit: %d %s", response.Code, response.Body.String())
	}
	var edited ModelConfig
	newRevision, err := service.Store.get(context.Background(), modelKey, &edited)
	if err != nil || newRevision != revision+2 || edited.Name != "Edited through original client" ||
		edited.Model != "fixture-v2" || edited.Endpoint != "http://127.0.0.1:8000/v1" || edited.APIKey != "keep-private-key" {
		t.Fatalf("original model edit was not persisted safely: %+v revision=%d err=%v", edited, newRevision, err)
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 1 {
		t.Fatalf("original model edit audit: count=%d err=%v", len(audits), err)
	}
}

func TestOriginalModelFirstCreateAndSecondCatalogEntry(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = privateKey
	key := objectKey{"model", "1", "workspace", "default"}
	if _, err := service.Store.db.ExecContext(context.Background(),
		`DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID); err != nil {
		t.Fatal(err)
	}
	encrypt := func(value string) string {
		t.Helper()
		ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &privateKey.PublicKey, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(ciphertext)
	}
	requestBody := map[string]any{"id": "", "name": "Created through original client",
		"model_type": 0, "base_model": "fixture-v1", "supplier": 15, "protocol": 1,
		"api_domain": encrypt("http://127.0.0.1:8000/v1"), "api_key": encrypt("original-private-key"),
		"config_list": []any{}}
	create := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(requestBody)
		if err != nil {
			t.Fatal(err)
		}
		return accountRequest(t, handler, http.MethodPost, "/api/v1/system/aimodel", string(body), token)
	}
	if denied := create(memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member created original model: %d", denied.Code)
	}
	requestBody["supplier"] = 1
	if rejected := create(adminToken); rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("provider requiring unsupported extra arguments accepted: %d", rejected.Code)
	}
	requestBody["supplier"] = 15
	requestBody["api_domain"] = "http://127.0.0.1:8000/v1"
	if rejected := create(adminToken); rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("plaintext original endpoint accepted: %d", rejected.Code)
	}
	requestBody["api_domain"] = encrypt("http://127.0.0.1:8000/v1")
	if created := create(adminToken); created.Code != http.StatusOK || strings.Contains(created.Body.String(), "original-private-key") {
		t.Fatalf("original first model create: %d %s", created.Code, created.Body.String())
	}
	var model ModelConfig
	if revision, err := service.Store.get(context.Background(), key, &model); err != nil || revision != 1 ||
		model.Name != "Created through original client" || model.APIKey != "original-private-key" || !model.ID.Valid() {
		t.Fatalf("original create was not encrypted and persisted: %+v revision=%d err=%v", model, revision, err)
	}
	listed := accountRequest(t, handler, http.MethodGet, "/api/v1/system/aimodel", "", adminToken)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), string(model.ID)) ||
		strings.Contains(listed.Body.String(), model.APIKey) {
		t.Fatalf("original catalog after create: %d %s", listed.Code, listed.Body.String())
	}
	if second := create(adminToken); second.Code != http.StatusOK {
		t.Fatalf("second catalog create: %d %s", second.Code, second.Body.String())
	}
	var unchanged ModelConfig
	if revision, err := service.Store.get(context.Background(), key, &unchanged); err != nil || revision != 1 || unchanged.ID != model.ID {
		t.Fatalf("second create changed default model: %+v revision=%d err=%v", unchanged, revision, err)
	}
	additional, err := service.Store.AdditionalWorkspaceModels(context.Background(), "1")
	if err != nil || len(additional) != 1 || additional[0].ID == model.ID || additional[0].APIKey != "original-private-key" {
		t.Fatalf("second catalog entry was not stored: %+v err=%v", additional, err)
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 2 {
		t.Fatalf("original create audit: count=%d err=%v", len(audits), err)
	}
	probe := accountRequest(t, handler, http.MethodPost, "/api/v1/system/aimodel/status", `{"id":""}`, adminToken)
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"content":"Model reachable"`) {
		t.Fatalf("original post-create status: %d %s", probe.Code, probe.Body.String())
	}
}
