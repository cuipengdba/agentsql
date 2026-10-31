package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAccountAPIKeysArePrivateAuditedAndLimited(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	path := "/api/v1/system/apikey"
	created := accountRequest(t, handler, http.MethodPost, path, `{}`, adminToken)
	if created.Code != http.StatusOK {
		t.Fatalf("create key: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data ID `json:"data"`
	}
	if json.Unmarshal(created.Body.Bytes(), &envelope) != nil || !envelope.Data.Valid() {
		t.Fatalf("invalid key id: %s", created.Body.String())
	}
	ownerView := accountRequest(t, handler, http.MethodGet, path, "", adminToken)
	if ownerView.Code != http.StatusOK || !strings.Contains(ownerView.Body.String(), `"secret_key"`) ||
		!strings.Contains(ownerView.Body.String(), `"access_key"`) || !strings.Contains(ownerView.Body.String(), `"status":true`) {
		t.Fatalf("owner key list: %d %s", ownerView.Code, ownerView.Body.String())
	}
	memberView := accountRequest(t, handler, http.MethodGet, path, "", memberToken)
	if memberView.Code != http.StatusOK || strings.Contains(memberView.Body.String(), `"secret_key"`) {
		t.Fatalf("cross-user key exposure: %d %s", memberView.Code, memberView.Body.String())
	}
	id := envelope.Data
	if denied := accountRequest(t, handler, http.MethodPut, path+"/status", `{"id":`+string(id)+`,"status":false}`, memberToken); denied.Code != http.StatusNotFound {
		t.Fatalf("other user disabled key: %d", denied.Code)
	}
	if changed := accountRequest(t, handler, http.MethodPut, path+"/status", `{"id":`+string(id)+`,"status":false}`, adminToken); changed.Code != http.StatusOK {
		t.Fatalf("disable key: %d %s", changed.Code, changed.Body.String())
	}
	var key AccountAPIKey
	if _, err := service.Store.get(context.Background(), apiKeyObject(id, "1"), &key); err != nil || key.Status {
		t.Fatalf("key status did not persist: %+v %v", key, err)
	}
	if denied := accountRequest(t, handler, http.MethodDelete, path+"/"+string(id), "", memberToken); denied.Code != http.StatusNotFound {
		t.Fatalf("other user deleted key: %d", denied.Code)
	}
	if removed := accountRequest(t, handler, http.MethodDelete, path+"/"+string(id), "", adminToken); removed.Code != http.StatusOK {
		t.Fatalf("delete key: %d %s", removed.Code, removed.Body.String())
	}
	if _, err := service.Store.get(context.Background(), apiKeyObject(id, "1"), &key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted key still present: %v", err)
	}
	var claim map[string]ID
	if _, err := service.Store.get(context.Background(), service.Store.apiKeyAccessIndex(key.AccessKey), &claim); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted access index still present: %v", err)
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 3 {
		t.Fatalf("key lifecycle audits: %d %v", len(audits), err)
	}
	for i := 0; i < 5; i++ {
		if response := accountRequest(t, handler, http.MethodPost, path, `{}`, adminToken); response.Code != http.StatusOK {
			t.Fatalf("key %d create: %d", i, response.Code)
		}
	}
	if limit := accountRequest(t, handler, http.MethodPost, path, `{}`, adminToken); limit.Code != http.StatusConflict {
		t.Fatalf("sixth key accepted: %d", limit.Code)
	}
}

func TestAccountAPIKeyAuditFailureRollsBack(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_key_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if response := accountRequest(t, handler, http.MethodPost, "/api/v1/system/apikey", `{}`, adminToken); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure allowed key creation: %d", response.Code)
	}
	p, err := service.Identity.Verify(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := service.Store.ListAccountAPIKeys(ctx, p)
	if err != nil || len(keys) != 0 {
		t.Fatalf("audit failure persisted key: %d %v", len(keys), err)
	}
	var n int
	if err := service.Store.db.QueryRowContext(ctx, `SELECT count(*) FROM chatbi_objects WHERE kind='api-key-access'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit failure persisted key index: %d %v", n, err)
	}
}

func TestDeletingAccountAlsoRemovesItsAPIKeyIndex(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/system/apikey", `{}`, memberToken)
	if created.Code != http.StatusOK {
		t.Fatalf("member create key: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data ID `json:"data"`
	}
	if json.Unmarshal(created.Body.Bytes(), &envelope) != nil || !envelope.Data.Valid() {
		t.Fatalf("invalid key ID: %s", created.Body.String())
	}
	var item AccountAPIKey
	if _, err := service.Store.get(context.Background(), apiKeyObject(envelope.Data, "2"), &item); err != nil {
		t.Fatal(err)
	}
	access := item.AccessKey
	if removed := accountRequest(t, handler, http.MethodDelete, "/api/v1/user/2", "", adminToken); removed.Code != http.StatusOK {
		t.Fatalf("delete account with API key: %d %s", removed.Code, removed.Body.String())
	}
	if _, err := service.Store.get(context.Background(), apiKeyObject(item.ID, "2"), &item); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user key remained: %v", err)
	}
	var claim map[string]ID
	if _, err := service.Store.get(context.Background(), service.Store.apiKeyAccessIndex(access), &claim); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user access index remained: %v", err)
	}
}

func TestAccountAPIKeyStatusAndDeleteFailClosedWhenAuditFails(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/system/apikey", `{}`, adminToken)
	if created.Code != http.StatusOK {
		t.Fatalf("key creation: %d", created.Code)
	}
	var envelope struct {
		Data ID `json:"data"`
	}
	if json.Unmarshal(created.Body.Bytes(), &envelope) != nil || !envelope.Data.Valid() {
		t.Fatalf("invalid key ID: %s", created.Body.String())
	}
	var key AccountAPIKey
	if _, err := service.Store.get(ctx, apiKeyObject(envelope.Data, "1"), &key); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_key_mutation_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if response := accountRequest(t, handler, http.MethodPut, "/api/v1/system/apikey/status", `{"id":`+string(key.ID)+`,"status":false}`, adminToken); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure allowed key status change: %d", response.Code)
	}
	if response := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/apikey/"+string(key.ID), "", adminToken); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure allowed key deletion: %d", response.Code)
	}
	var unchanged AccountAPIKey
	if _, err := service.Store.get(ctx, apiKeyObject(key.ID, "1"), &unchanged); err != nil || !unchanged.Status {
		t.Fatalf("audit failure changed key: %+v %v", unchanged, err)
	}
	var claim map[string]ID
	if _, err := service.Store.get(ctx, service.Store.apiKeyAccessIndex(key.AccessKey), &claim); err != nil || claim["id"] != key.ID {
		t.Fatalf("audit failure removed key index: %v %v", claim, err)
	}
}
