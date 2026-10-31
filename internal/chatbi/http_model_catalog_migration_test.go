package chatbi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAdminCatalogMigrationRoutesCutOverWithoutCredentialExposure(t *testing.T) {
	_, handler, adminToken, memberToken := adminUsersFixture(t)
	preflightPath := "/api/v1/agentsql/model/catalog/preflight"
	stagePath := "/api/v1/agentsql/model/catalog/stage"
	activatePath := "/api/v1/agentsql/model/catalog/activate"
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, preflightPath, ""},
		{http.MethodPost, stagePath, `{}`},
		{http.MethodPost, activatePath, `{"source_digest":"` + strings.Repeat("0", 64) + `"}`},
	} {
		denied := accountRequest(t, handler, route.method, route.path, route.body, memberToken)
		if denied.Code != http.StatusUnauthorized {
			t.Fatalf("member used migration route %s: %d %s", route.path, denied.Code, denied.Body.String())
		}
	}
	preflight := accountRequest(t, handler, http.MethodGet, preflightPath, "", adminToken)
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"models":1`) ||
		strings.Contains(preflight.Body.String(), "example.invalid") {
		t.Fatalf("catalog preflight: %d %s", preflight.Code, preflight.Body.String())
	}
	stage := accountRequest(t, handler, http.MethodPost, stagePath, `{}`, adminToken)
	var result struct {
		Data stagedModelCatalog `json:"data"`
	}
	if stage.Code != http.StatusOK || json.Unmarshal(stage.Body.Bytes(), &result) != nil ||
		result.Data.Default != "20" || len(result.Data.SourceDigest) != 64 ||
		strings.Contains(stage.Body.String(), "example.invalid") {
		t.Fatalf("catalog stage: %d %s", stage.Code, stage.Body.String())
	}
	wrong := accountRequest(t, handler, http.MethodPost, activatePath,
		`{"source_digest":"`+strings.Repeat("0", 64)+`"}`, adminToken)
	if wrong.Code == http.StatusOK {
		t.Fatalf("wrong digest activated catalog: %d %s", wrong.Code, wrong.Body.String())
	}
	activated := accountRequest(t, handler, http.MethodPost, activatePath,
		`{"source_digest":"`+result.Data.SourceDigest+`"}`, adminToken)
	if activated.Code != http.StatusOK || !strings.Contains(activated.Body.String(), `"active":true`) {
		t.Fatalf("catalog activation: %d %s", activated.Code, activated.Body.String())
	}
	status := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/model/catalog-status", "", adminToken)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"active":true`) {
		t.Fatalf("catalog status after cutover: %d %s", status.Code, status.Body.String())
	}
	if again := accountRequest(t, handler, http.MethodPost, activatePath,
		`{"source_digest":"`+result.Data.SourceDigest+`"}`, adminToken); again.Code == http.StatusOK {
		t.Fatalf("catalog activated twice: %d %s", again.Code, again.Body.String())
	}
}
