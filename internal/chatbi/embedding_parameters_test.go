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
	if all.Code != http.StatusOK || !strings.Contains(all.Body.String(), `"pkey":"embedding.terminology_similarity"`) ||
		!strings.Contains(all.Body.String(), `"pkey":"embedding.table_count"`) ||
		!strings.Contains(all.Body.String(), `"pkey":"embedding.keyword_enabled"`) {
		t.Fatalf("embedding settings missing: %d %s", all.Code, all.Body.String())
	}
	if denied := chatParameterForm(t, handler, memberToken, `[{"pkey":"embedding.enabled","pval":false}]`); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member changed embedding settings: %d", denied.Code)
	}
	form := `[{"pkey":"embedding.enabled","pval":false},{"pkey":"embedding.terminology_similarity","pval":0.7},{"pkey":"embedding.data_training_top_count","pval":2},{"pkey":"embedding.table_enabled","pval":true},{"pkey":"embedding.table_count","pval":2},{"pkey":"embedding.table_alpha","pval":0.6},{"pkey":"embedding.keyword_enabled","pval":false}]`
	if saved := chatParameterForm(t, handler, adminToken, form); saved.Code != http.StatusOK {
		t.Fatalf("save embedding settings: %d %s", saved.Code, saved.Body.String())
	}
	settings, err := service.Store.ChatSettings(ctx)
	if err != nil || settings.Embedding.Enabled || settings.Embedding.TerminologySimilarity != 0.7 || settings.Embedding.TrainingTopCount != 2 ||
		!settings.Embedding.TableEnabled || settings.Embedding.TableCount != 2 || settings.Embedding.TableAlpha != 0.6 || settings.Embedding.KeywordEnabled {
		t.Fatalf("stored settings: %+v %v", settings.Embedding, err)
	}
	for _, invalid := range []string{
		`[{"pkey":"embedding.terminology_similarity","pval":1.1}]`,
		`[{"pkey":"embedding.data_training_top_count","pval":0}]`,
		`[{"pkey":"embedding.enabled","pval":"1"}]`,
		`[{"pkey":"embedding.terminology_similarity","pval":"NaN"}]`,
		`[{"pkey":"embedding.table_count","pval":0}]`,
		`[{"pkey":"embedding.table_alpha","pval":1.1}]`,
		`[{"pkey":"embedding.keyword_enabled","pval":"yes"}]`,
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

func TestEmbeddingSettingsUpgradeRetainsOldSavedControls(t *testing.T) {
	service, _, _, _ := adminUsersFixture(t)
	ctx := context.Background()
	settings := defaultChatSettings()
	settings.Embedding.StrategyVersion = 0
	settings.Embedding.TerminologyTopCount = 3
	settings.Embedding.TableCount = 0
	settings.Embedding.TableEnabled = false
	settings.Embedding.KeywordEnabled = false
	settings.Embedding.TableAlpha = 0
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Store.ChatSettings(ctx)
	if err != nil || loaded.Embedding.StrategyVersion != 1 || loaded.Embedding.TerminologyTopCount != 3 ||
		loaded.Embedding.TableCount != 10 || !loaded.Embedding.TableEnabled || !loaded.Embedding.KeywordEnabled || loaded.Embedding.TableAlpha != 0.4 {
		t.Fatalf("legacy settings upgrade: %+v %v", loaded.Embedding, err)
	}
}
