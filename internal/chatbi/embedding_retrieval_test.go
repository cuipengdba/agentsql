package chatbi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fixedEmbedding struct{ fail bool }

func (model fixedEmbedding) Encode(_ context.Context, texts []string) ([][]float32, error) {
	if model.fail {
		return nil, errors.New("synthetic inference failure")
	}
	result := make([][]float32, len(texts))
	for i, text := range texts {
		vector := make([]float32, 768)
		if strings.Contains(text, "weather") {
			vector[1] = 1
		} else {
			vector[0] = 1
		}
		result[i] = vector
	}
	return result, nil
}

func TestEmbeddingVocabularyRespectsDatasourceAndLexicalSwitch(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	admin := principal
	admin.User = user
	otherBinding := Binding{Datasource: "11", Workspace: "1", User: principal.User.ID, Name: "other", Dialect: "pg", BindingID: "binding-other", ConnectorToken: "fixture-connector", AgentKey: "fixture-agent"}
	if err := service.Store.put(ctx, bindingKey(principal, "11"), otherBinding, 0); err != nil {
		t.Fatal(err)
	}
	for _, term := range []Terminology{
		{Word: "revenue", Description: "approved sales measure", Enabled: true, SpecificDS: true, DatasourceIDs: []int64{10}},
		{Word: "weather", Description: "unrelated", Enabled: true, SpecificDS: true, DatasourceIDs: []int64{10}},
		{Word: "private payroll", Description: "other datasource", Enabled: true, SpecificDS: true, DatasourceIDs: []int64{11}},
		{Word: "disabled term", Description: "not enabled", Enabled: false},
	} {
		if _, err := service.SaveTerminology(ctx, admin, term); err != nil {
			t.Fatal(err)
		}
	}
	service.Embedding = fixedEmbedding{}
	settings := defaultChatSettings()
	settings.Embedding.Enabled = false
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	lexical, err := service.Vocabulary(ctx, principal, "10", "revenue")
	if err != nil || len(lexical) != 1 || lexical[0].Word != "revenue" {
		t.Fatalf("lexical disabled: %+v %v", lexical, err)
	}
	settings.Embedding.Enabled = true
	settings.Embedding.TerminologyTopCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 1); err != nil {
		t.Fatal(err)
	}
	lexical, err = service.Vocabulary(ctx, principal, "10", "revenue weather")
	if err != nil || len(lexical) != 2 {
		t.Fatalf("vector top count truncated lexical results: %+v %v", lexical, err)
	}
	vector, err := service.Vocabulary(ctx, principal, "10", "sales amount")
	if err != nil || len(vector) != 1 || vector[0].Word != "revenue" {
		t.Fatalf("filtered vector results: %+v %v", vector, err)
	}
	service.Embedding = fixedEmbedding{fail: true}
	if _, err := service.Vocabulary(ctx, principal, "10", "sales amount"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("inference failure was accepted: %v", err)
	}
}

func TestEmbeddingExamplesRequireVisibleSQLBeforeRanking(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	admin := principal
	admin.User = user
	otherBinding := Binding{Datasource: "11", Workspace: "1", User: principal.User.ID, Name: "other", Dialect: "pg", BindingID: "binding-other", ConnectorToken: "fixture-connector", AgentKey: "fixture-agent"}
	if err := service.Store.put(ctx, bindingKey(principal, "11"), otherBinding, 0); err != nil {
		t.Fatal(err)
	}
	for _, item := range []SQLExample{
		{Workspace: 1, Datasource: 10, Question: "revenue", Description: "SELECT amount FROM public.sales LIMIT 2", Enabled: true},
		{Workspace: 1, Datasource: 10, Question: "private payroll", Description: "SELECT secret FROM private.payroll", Enabled: true},
		{Workspace: 1, Datasource: 11, Question: "foreign datasource", Description: "SELECT amount FROM public.sales LIMIT 2", Enabled: true},
	} {
		if _, err := service.SaveExample(ctx, admin, item); err != nil {
			t.Fatal(err)
		}
	}
	service.Embedding = fixedEmbedding{}
	visible := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	hints, err := service.SafeExamples(ctx, principal, "10", "pg", visible, "sales amount")
	if err != nil || len(hints) != 1 || hints[0].Question != "revenue" {
		t.Fatalf("unsafe vector sample: %+v %v", hints, err)
	}
	if _, err := service.SafeExamples(ctx, principal, "10", "pg", nil, "sales amount"); err != nil {
		t.Fatal(err)
	}
}
