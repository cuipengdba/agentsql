package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeSemanticAPIKeepsLargeIDsExactAndChecksAdmin(t *testing.T) {
	service, principal, _, _, token := serviceFixture(t)
	ctx := context.Background()
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	const largeID int64 = 9007199254740993
	seed := Terminology{ID: largeID, Word: "GMV", Description: "Gross merchandise value", Enabled: true,
		OtherWords: []string{}, DatasourceIDs: []int64{}}
	if err := service.Store.put(ctx, semanticKey("terminology", principal.Workspace, largeID), seed, 0); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, input any, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if input != nil {
			payload, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(payload))
		if authorized {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		}
		if input != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/agentsql/terms?q=GMV"
	if w := request(http.MethodGet, path, nil, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous term list returned %d", w.Code)
	}
	w := request(http.MethodGet, path, nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"9007199254740993"`) ||
		!strings.Contains(w.Body.String(), `"total_count":1`) {
		t.Fatalf("large term ID was rounded or missing: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPut, "/api/v1/agentsql/terms", map[string]any{
		"id": "9007199254740993", "word": "GMV", "description": "Updated meaning", "enabled": true,
		"other_words": []string{"Gross sales"}, "datasource_ids": []string{},
	}, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"9007199254740993"`) {
		t.Fatalf("large term update: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPost, "/api/v1/agentsql/terms/9007199254740993/enabled", map[string]any{"enabled": false}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable term: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodGet, "/api/v1/agentsql/terms?q=Updated", nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("updated term search: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodDelete, "/api/v1/agentsql/terms/9007199254740993", nil, true)
	if w.Code != http.StatusOK {
		t.Fatalf("delete term: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPut, "/api/v1/agentsql/examples", map[string]any{
		"datasource": "10", "question": "Revenue by region?", "description": "SELECT amount FROM sales", "enabled": true,
	}, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"`) {
		t.Fatalf("create example: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodGet, "/api/v1/agentsql/examples?q=Revenue", nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"datasource":"10"`) ||
		!strings.Contains(w.Body.String(), `"total_count":1`) {
		t.Fatalf("example list: %d %s", w.Code, w.Body.String())
	}
	user, revision, err = service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = false
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/v1/agentsql/terms", "/api/v1/agentsql/examples"} {
		if w := request(http.MethodGet, route, nil, true); w.Code != http.StatusUnauthorized {
			t.Fatalf("non-admin %s returned %d", route, w.Code)
		}
	}
}
