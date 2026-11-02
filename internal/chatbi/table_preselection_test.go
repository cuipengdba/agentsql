package chatbi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type tableFixtureEmbedding struct{ fail bool }

type tableCaptureModel struct{ columns [][]Column }

func (model *tableCaptureModel) Generate(_ context.Context, _ ModelConfig, _, _ string, columns []Column) (SQLGeneration, error) {
	model.columns = append(model.columns, append([]Column(nil), columns...))
	return SQLGeneration{SQL: "SELECT amount FROM public.sales"}, nil
}

func (model tableFixtureEmbedding) Encode(_ context.Context, texts []string) ([][]float32, error) {
	if model.fail {
		return nil, errors.New("inference unavailable")
	}
	vectors := make([][]float32, len(texts))
	for index, value := range texts {
		vector := make([]float32, 768)
		if value == "revenue" || strings.Contains(value, "weather") {
			vector[1] = 1
		} else {
			vector[0] = 1
		}
		vectors[index] = vector
	}
	return vectors, nil
}

func TestTablePreselectionUsesAuthorizedSchemaAndEffectiveWeight(t *testing.T) {
	columns := []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "temperature", DataType: "numeric"},
	}
	remarks := []RemarkHint{{Schema: "public", Table: "sales", Remark: "revenue"}}
	for _, item := range []struct {
		alpha float64
		want  string
	}{{0, "sales"}, {1, "weather"}} {
		selected, hints, err := selectModelTables(context.Background(), tableFixtureEmbedding{}, "revenue", columns, remarks, 1, item.alpha, true)
		if err != nil || len(selected) != 1 || selected[0].Table != item.want || len(hints) != 1 && item.want == "sales" || len(hints) != 0 && item.want == "weather" {
			t.Fatalf("alpha %.1f selected %#v %#v: %v", item.alpha, selected, hints, err)
		}
	}
	selected, _, err := selectModelTables(context.Background(), tableFixtureEmbedding{}, "weather", columns, remarks, 1, 0, false)
	if err != nil || len(selected) != 1 || selected[0].Table != "weather" {
		t.Fatalf("disabled keywords did not use vector rank: %#v %v", selected, err)
	}
}

func TestTablePreselectionPreservesExplicitAndFallsBackOnInferenceFailure(t *testing.T) {
	columns := []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "temperature", DataType: "numeric"},
		{Schema: "public", Table: "other", Name: "id", DataType: "integer"},
	}
	selected, _, err := selectModelTables(context.Background(), tableFixtureEmbedding{}, "sales and weather", columns, nil, 1, 1, false)
	if err != nil || len(selected) != 2 || selected[0].Table != "sales" || selected[1].Table != "weather" {
		t.Fatalf("explicit table names were truncated: %#v %v", selected, err)
	}
	selected, _, err = selectModelTables(context.Background(), tableFixtureEmbedding{fail: true}, "weather", columns, nil, 1, 1, false)
	if err != nil || len(selected) != len(columns) {
		t.Fatalf("failed preselection dropped authorized schema: %#v %v", selected, err)
	}
	if mentionedIdentifier("preorders", "orders") || !mentionedIdentifier("orders表", "orders") {
		t.Fatal("table name boundary matching changed")
	}
}

func TestAskTableSettingsNarrowOnlyTheModelSchema(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "temperature", DataType: "numeric"},
		{Schema: "private", Table: "payroll", Name: "salary", DataType: "numeric"},
	}
	model := &tableCaptureModel{}
	service.Model = model
	service.Embedding = tableFixtureEmbedding{}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	chat, err := service.Start(ctx, principal, "10", "sales", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if _, err := service.Ask(ctx, principal, chat.ID, "sales", check, nil); err != nil {
		t.Fatal(err)
	}
	if len(model.columns) != 1 || len(model.columns[0]) != 1 || model.columns[0][0].Table != "sales" {
		t.Fatalf("table count did not narrow model schema: %#v", model.columns)
	}
	settings.Embedding.TableEnabled = false
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ask(ctx, principal, chat.ID, "sales again", check, nil); err != nil {
		t.Fatal(err)
	}
	if len(model.columns) != 2 || len(model.columns[1]) != len(gateway.columns) {
		t.Fatalf("disabled table selection still narrowed model schema: %#v", model.columns)
	}
}

func TestAskTablePreselectionClosesAfterPermissionRevocation(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "temperature", DataType: "numeric"},
	}
	rule := testActiveRowPermission(t, "east")
	if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0); err != nil {
		t.Fatal(err)
	}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	model := &tableCaptureModel{}
	service.Model = model
	service.Embedding = &permissionChangingEmbedding{change: func() {
		revoked := rule
		revoked.Enable = false
		if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), revoked, 1); err != nil {
			t.Fatal(err)
		}
	}}
	chat, err := service.Start(ctx, principal, "10", "sales", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if _, err := service.Ask(ctx, principal, chat.ID, "sales", check, nil); !errors.Is(err, ErrGateway) ||
		len(model.columns) != 0 || gateway.queries != 0 {
		t.Fatalf("revoked table context reached model or query: columns=%#v queries=%d err=%v", model.columns, gateway.queries, err)
	}
}
