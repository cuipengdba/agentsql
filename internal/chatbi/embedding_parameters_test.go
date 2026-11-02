package chatbi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestEmbeddingParametersPersistAndRejectInvalidWrites(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	all := accountRequest(t, handler, http.MethodGet, "/api/v1/system/parameter", "", adminToken)
	if all.Code != http.StatusOK || !strings.Contains(all.Body.String(), `"pkey":"embedding.terminology_similarity"`) {
		t.Fatalf("embedding settings missing: %d %s", all.Code, all.Body.String())
	}
	if denied := chatParameterForm(t, handler, memberToken, `[{"pkey":"embedding.enabled","pval":false}]`); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member changed embedding settings: %d", denied.Code)
	}
	form := `[{"pkey":"embedding.enabled","pval":false},{"pkey":"embedding.terminology_similarity","pval":0.7},{"pkey":"embedding.data_training_top_count","pval":2}]`
	if saved := chatParameterForm(t, handler, adminToken, form); saved.Code != http.StatusOK {
		t.Fatalf("save embedding settings: %d %s", saved.Code, saved.Body.String())
	}
	settings, err := service.Store.ChatSettings(ctx)
	if err != nil || settings.Embedding.Enabled || settings.Embedding.TerminologySimilarity != 0.7 || settings.Embedding.TrainingTopCount != 2 {
		t.Fatalf("stored settings: %+v %v", settings.Embedding, err)
	}
	for _, invalid := range []string{
		`[{"pkey":"embedding.terminology_similarity","pval":1.1}]`,
		`[{"pkey":"embedding.data_training_top_count","pval":0}]`,
		`[{"pkey":"embedding.enabled","pval":"1"}]`,
		`[{"pkey":"embedding.terminology_similarity","pval":"NaN"}]`,
	} {
		if response := chatParameterForm(t, handler, adminToken, invalid); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid setting accepted: %d %s", response.Code, response.Body.String())
		}
	}
	settings, err = service.Store.ChatSettings(ctx)
	if err != nil || settings.Embedding.TerminologySimilarity != 0.7 {
		t.Fatalf("invalid write changed stored settings: %+v %v", settings.Embedding, err)
	}
}
