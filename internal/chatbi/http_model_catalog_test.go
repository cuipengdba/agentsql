package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOriginalSecondaryModelLifecycleIsScopedAndAudited(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
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
	request := func(method, path string, body map[string]any, token string) int {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response := accountRequest(t, handler, method, path, string(payload), token)
		return response.Code
	}
	path := "/api/v1/system/aimodel"
	create := map[string]any{"id": "", "name": "Second local model", "model_type": 0,
		"base_model": "fixture-second", "supplier": 15, "protocol": 1,
		"api_domain": encrypt("http://127.0.0.1:8001/v1"), "api_key": encrypt("second-private-key"), "config_list": []any{}}
	if code := request(http.MethodPost, path, create, memberToken); code != http.StatusUnauthorized {
		t.Fatalf("member created a second model: %d", code)
	}
	if code := request(http.MethodPost, path, create, adminToken); code != http.StatusOK {
		t.Fatalf("administrator second model create: %d", code)
	}
	models, err := service.Store.AdditionalWorkspaceModels(context.Background(), "1")
	if err != nil || len(models) != 1 || models[0].APIKey != "second-private-key" {
		t.Fatalf("second model not encrypted and persisted: %+v %v", models, err)
	}
	id := models[0].ID
	listed := accountRequest(t, handler, http.MethodGet, path, "", adminToken)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), string(id)) ||
		!strings.Contains(listed.Body.String(), `"default_model":false`) ||
		strings.Contains(listed.Body.String(), "second-private-key") || strings.Contains(listed.Body.String(), "127.0.0.1:8001") {
		t.Fatalf("secondary model catalog leaked or omitted: %d %s", listed.Code, listed.Body.String())
	}
	detail := accountRequest(t, handler, http.MethodGet, path+"/"+string(id), "", adminToken)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"has_api_key":true`) ||
		!strings.Contains(detail.Body.String(), `"default_model":false`) || strings.Contains(detail.Body.String(), "second-private-key") {
		t.Fatalf("secondary model detail leaked or omitted: %d %s", detail.Code, detail.Body.String())
	}
	update := map[string]any{"id": id, "name": "Second local model edited", "model_type": 0,
		"base_model": "fixture-second-v2", "supplier": 15, "protocol": 1, "default_model": false,
		"api_domain": encrypt("http://127.0.0.1:8002/v1"), "api_key": "", "config_list": []any{}}
	if code := request(http.MethodPut, path, update, adminToken); code != http.StatusOK {
		t.Fatalf("secondary model update: %d", code)
	}
	var stored ModelConfig
	if _, err := service.Store.get(context.Background(), additionalModelKey("1", id), &stored); err != nil ||
		stored.Name != "Second local model edited" || stored.Model != "fixture-second-v2" ||
		stored.Endpoint != "http://127.0.0.1:8002/v1" || stored.APIKey != "second-private-key" {
		t.Fatalf("secondary model update lost data or key: %+v %v", stored, err)
	}
	update["default_model"] = true
	if code := request(http.MethodPut, path, update, adminToken); code != http.StatusConflict {
		t.Fatalf("unsupported default switch was accepted: %d", code)
	}
	if code := request(http.MethodPost, path+"/status", map[string]any{"id": id}, adminToken); code != http.StatusOK {
		t.Fatalf("secondary saved-model probe: %d", code)
	}
	if response := accountRequest(t, handler, http.MethodDelete, path+"/"+string(id), "", memberToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("member deleted secondary model: %d", response.Code)
	}
	if response := accountRequest(t, handler, http.MethodDelete, path+"/"+string(id), "", adminToken); response.Code != http.StatusOK {
		t.Fatalf("secondary model delete: %d %s", response.Code, response.Body.String())
	}
	if _, err := service.Store.get(context.Background(), additionalModelKey("1", id), &stored); err != ErrNotFound {
		t.Fatalf("deleted secondary model remains: %v", err)
	}
	var defaultModel ModelConfig
	if _, err := service.Store.get(context.Background(), objectKey{"model", "1", "workspace", "default"}, &defaultModel); err != nil || !defaultModel.ID.Valid() {
		t.Fatalf("default model changed by catalog lifecycle: %+v %v", defaultModel, err)
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 3 {
		t.Fatalf("secondary model lifecycle audit count: %d %v", len(audits), err)
	}
}

func TestOriginalGenericOpenAITemperatureIsValidatedAndSent(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
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
	var received float64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Temperature float64 `json:"temperature"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid upstream model request")
		}
		received = payload.Temperature
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": `{"sql":"SELECT amount FROM public.sales"}`}}}})
	}))
	defer provider.Close()
	path := "/api/v1/system/aimodel"
	body := map[string]any{"id": "", "name": "Temperature model", "model_type": 0,
		"base_model": "fixture-temperature", "supplier": 15, "protocol": 1,
		"api_domain": encrypt(provider.URL + "/v1"), "api_key": "",
		"config_list": []any{map[string]any{"key": "temperature", "name": "temperature", "val": 0.6}}}
	create := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return accountRequest(t, handler, http.MethodPost, path, string(payload), token)
	}
	if denied := create(memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member created temperature model: %d", denied.Code)
	}
	for _, invalid := range []any{
		[]any{map[string]any{"key": "temperature", "name": "temperature", "val": 1.5}},
		[]any{map[string]any{"key": "temperature", "name": "temperature", "val": nil}},
		[]any{map[string]any{"key": "top_p", "name": "top_p", "val": 0.5}},
		[]any{map[string]any{"key": "temperature", "name": "temperature", "val": 0.6, "extra": true}},
	} {
		body["config_list"] = invalid
		if rejected := create(adminToken); rejected.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unsupported model parameter accepted: %d %s", rejected.Code, rejected.Body.String())
		}
	}
	body["config_list"] = []any{map[string]any{"key": "temperature", "name": "temperature", "val": 0.6}}
	if created := create(adminToken); created.Code != http.StatusOK {
		t.Fatalf("original default temperature create: %d %s", created.Code, created.Body.String())
	}
	models, err := service.Store.AdditionalWorkspaceModels(context.Background(), "1")
	if err != nil || len(models) != 1 || models[0].Temperature == nil || *models[0].Temperature != 0.6 {
		t.Fatalf("temperature was not saved: %+v %v", models, err)
	}
	id := models[0].ID
	detail := accountRequest(t, handler, http.MethodGet, path+"/"+string(id), "", adminToken)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"val":0.6`) {
		t.Fatalf("original temperature form did not reload: %d %s", detail.Code, detail.Body.String())
	}
	if _, err := (OpenAIModel{}).Generate(context.Background(), models[0], "pg", "show amount", []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}); err != nil || received != 0.6 {
		t.Fatalf("saved temperature did not reach local model: received=%v err=%v", received, err)
	}
	body["id"] = id
	body["default_model"] = false
	body["name"] = "Temperature model edited"
	update := func() *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return accountRequest(t, handler, http.MethodPut, path, string(payload), adminToken)
	}
	body["config_list"] = []any{map[string]any{"key": "temperature", "name": "temperature", "val": 0.25}}
	if changed := update(); changed.Code != http.StatusOK {
		t.Fatalf("original temperature edit: %d %s", changed.Code, changed.Body.String())
	}
	var stored ModelConfig
	if _, err := service.Store.get(context.Background(), additionalModelKey("1", id), &stored); err != nil ||
		stored.Temperature == nil || *stored.Temperature != 0.25 || stored.Name != "Temperature model edited" {
		t.Fatalf("temperature edit not persisted: %+v %v", stored, err)
	}
	if _, err := (OpenAIModel{}).Generate(context.Background(), stored, "pg", "show amount", []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}); err != nil || received != 0.25 {
		t.Fatalf("updated temperature did not reach local model: received=%v err=%v", received, err)
	}
	body["config_list"] = []any{}
	if cleared := update(); cleared.Code != http.StatusOK {
		t.Fatalf("original temperature clear: %d %s", cleared.Code, cleared.Body.String())
	}
	stored = ModelConfig{}
	if _, err := service.Store.get(context.Background(), additionalModelKey("1", id), &stored); err != nil || stored.Temperature != nil {
		t.Fatalf("temperature clear not persisted: %+v %v", stored, err)
	}
}

func TestOriginalOpenAICompatibleSupplierIdentity(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	var receivedTemperature float64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Temperature float64 `json:"temperature"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid upstream model request")
		}
		receivedTemperature = payload.Temperature
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "stop", "message": map[string]string{"content": `{"sql":"SELECT amount FROM public.sales"}`},
		}}})
	}))
	defer provider.Close()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = privateKey
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &privateKey.PublicKey, []byte(provider.URL+"/v1"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := base64.StdEncoding.EncodeToString(ciphertext)
	create := func(supplier int, config []any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"id": "", "name": "Supplier fixture " + string(rune('A'+supplier)),
			"model_type": 0, "base_model": "fixture-model", "supplier": supplier, "protocol": 1,
			"api_domain": endpoint, "api_key": "", "config_list": config})
		if err != nil {
			t.Fatal(err)
		}
		return accountRequest(t, handler, http.MethodPost, "/api/v1/system/aimodel", string(payload), adminToken)
	}
	for _, supplier := range []int{1, 5, 11, 12, 14} {
		if result := create(supplier, nil); result.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unsupported supplier %d accepted: %d", supplier, result.Code)
		}
	}
	for _, supplier := range []int{2, 3, 4, 6, 7, 8, 9, 10, 13} {
		config := []any{}
		if supplier == 4 || supplier == 7 {
			config = []any{map[string]any{"key": "temperature", "name": "temperature", "val": 1.5}}
		} else if supplier != 2 && supplier != 3 {
			config = []any{map[string]any{"key": "temperature", "name": "temperature", "val": 0.6}}
		}
		if result := create(supplier, config); result.Code != http.StatusOK {
			t.Fatalf("supplier %d create: %d %s", supplier, result.Code, result.Body.String())
		}
	}
	if rejected := create(3, []any{map[string]any{"key": "temperature", "name": "temperature", "val": 1.5}}); rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("out-of-range supplier temperature accepted: %d", rejected.Code)
	}
	models, err := service.Store.AdditionalWorkspaceModels(context.Background(), "1")
	if err != nil || len(models) != 9 {
		t.Fatalf("supplier models missing: %d %v", len(models), err)
	}
	for _, model := range models {
		if !supportedLegacySupplier(model.Supplier) {
			t.Fatalf("unsupported stored supplier %d", model.Supplier)
		}
		detail := accountRequest(t, handler, http.MethodGet, "/api/v1/system/aimodel/"+string(model.ID), "", adminToken)
		var response struct {
			Data struct {
				Supplier int `json:"supplier"`
			} `json:"data"`
		}
		if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &response) != nil || response.Data.Supplier != model.Supplier {
			t.Fatalf("supplier %d did not survive original read: %d %s", model.Supplier, detail.Code, detail.Body.String())
		}
		if model.Supplier == 7 {
			if _, err := (OpenAIModel{}).Generate(context.Background(), model, "pg", "show amount", []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}); err != nil || receivedTemperature != 1.5 {
				t.Fatalf("supplier 7 temperature not sent: received=%v err=%v", receivedTemperature, err)
			}
		}
	}
}

func TestOriginalDefaultModelSwitchIsAtomicAndReversible(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	principal, err := service.Identity.Verify(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	defaultKey := objectKey{"model", "1", "workspace", "default"}
	var previous ModelConfig
	if _, err := service.Store.get(ctx, defaultKey, &previous); err != nil {
		t.Fatal(err)
	}
	secret := "second-model-secret"
	if err := service.Store.CreateAdditionalWorkspaceModel(ctx, principal, WorkspaceModelUpdate{
		Name: "Switch candidate", Model: "fixture-switch", Endpoint: "http://127.0.0.1:8001/v1", APIKey: &secret,
	}); err != nil {
		t.Fatal(err)
	}
	models, err := service.Store.AdditionalWorkspaceModels(ctx, "1")
	if err != nil || len(models) != 1 {
		t.Fatalf("missing switch candidate: %+v %v", models, err)
	}
	id := models[0].ID
	path := "/api/v1/system/aimodel/default/" + string(id)
	if denied := accountRequest(t, handler, http.MethodPut, path, "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member switched default model: %d", denied.Code)
	}
	if missing := accountRequest(t, handler, http.MethodPut, "/api/v1/system/aimodel/default/999999", "", adminToken); missing.Code != http.StatusNotFound {
		t.Fatalf("foreign model became default: %d", missing.Code)
	}
	if changed := accountRequest(t, handler, http.MethodPut, path, "", adminToken); changed.Code != http.StatusOK {
		t.Fatalf("default model switch: %d %s", changed.Code, changed.Body.String())
	}
	var active ModelConfig
	if _, err := service.Store.get(ctx, defaultKey, &active); err != nil || active.ID != id || active.APIKey != secret {
		t.Fatalf("new default not active: %+v %v", active, err)
	}
	var parked ModelConfig
	if _, err := service.Store.get(ctx, additionalModelKey("1", previous.ID), &parked); err != nil || parked.ID != previous.ID || parked.Model != previous.Model {
		t.Fatalf("old default not parked: %+v %v", parked, err)
	}
	if repeat := accountRequest(t, handler, http.MethodPut, path, "", adminToken); repeat.Code != http.StatusOK {
		t.Fatalf("idempotent default selection: %d", repeat.Code)
	}
	if restored := accountRequest(t, handler, http.MethodPut, "/api/v1/system/aimodel/default/"+string(previous.ID), "", adminToken); restored.Code != http.StatusOK {
		t.Fatalf("restore default model: %d %s", restored.Code, restored.Body.String())
	}
	if _, err := service.Store.get(ctx, defaultKey, &active); err != nil || active.ID != previous.ID {
		t.Fatalf("previous default not restored: %+v %v", active, err)
	}
	if _, err := service.Store.get(ctx, additionalModelKey("1", id), &parked); err != nil || parked.ID != id || parked.APIKey != secret {
		t.Fatalf("candidate not parked after reverse switch: %+v %v", parked, err)
	}
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(audits) != 3 {
		t.Fatalf("switch audit count includes no-op: %d %v", len(audits), err)
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_default_switch_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if rejected := accountRequest(t, handler, http.MethodPut, path, "", adminToken); rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure allowed default switch: %d", rejected.Code)
	}
	if _, err := service.Store.get(ctx, defaultKey, &active); err != nil || active.ID != previous.ID {
		t.Fatalf("audit failure changed active model: %+v %v", active, err)
	}
	if _, err := service.Store.get(ctx, additionalModelKey("1", id), &parked); err != nil || parked.ID != id || parked.APIKey != secret {
		t.Fatalf("audit failure lost parked model: %+v %v", parked, err)
	}
}
