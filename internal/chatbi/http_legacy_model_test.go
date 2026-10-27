package chatbi

import (
	"context"
	"net/http"
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
