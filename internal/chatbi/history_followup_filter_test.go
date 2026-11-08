package chatbi

import (
	"context"
	"strings"
	"testing"
)

type droppedFollowupFilterModel struct{ calls int }

func (m *droppedFollowupFilterModel) Generate(context.Context, ModelConfig, string, string, []Column) (SQLGeneration, error) {
	return SQLGeneration{}, ErrModel
}

func (m *droppedFollowupFilterModel) GenerateWithHistory(_ context.Context, _ ModelConfig, _, _ string, _ []Column, _ []TurnContext) (SQLGeneration, error) {
	m.calls++
	if m.calls == 1 {
		return SQLGeneration{SQL: "SELECT region, amount FROM public.sales WHERE region = 'west' LIMIT 2"}, nil
	}
	return SQLGeneration{SQL: "SELECT pg_catalog.sum(amount) AS total_amount FROM public.sales"}, nil
}

func TestPreserveReferentialWhere(t *testing.T) {
	history := []TurnContext{{Question: "Show west orders", SQL: "SELECT region, amount FROM public.orders WHERE region = 'west' LIMIT 2"}}
	generated := "SELECT pg_catalog.sum(amount) AS total_amount FROM public.orders"
	got, err := preserveReferentialWhere("pg", "这些订单的 amount 合计是多少？", history, generated)
	if err != nil || !strings.Contains(got, "WHERE region = 'west'") {
		t.Fatalf("referential filter omitted: %q %v", got, err)
	}
	for _, question := range []string{"Show all orders", "这些订单改为 east 的合计"} {
		got, err = preserveReferentialWhere("pg", question, history, generated)
		if err != nil || got != generated {
			t.Fatalf("new filter request changed: %q -> %q %v", question, got, err)
		}
	}
	for _, previous := range []string{
		"SELECT amount FROM public.orders WHERE EXISTS (SELECT 1 FROM public.other)",
		"SELECT amount FROM public.other WHERE region = 'west'",
	} {
		_, err = preserveReferentialWhere("pg", "这些订单的 amount 合计是多少？", []TurnContext{{SQL: previous}}, generated)
		if err != ErrModel {
			t.Fatalf("unproven historical filter did not close: %q %v", previous, err)
		}
	}
}

func TestAskCarriesCurrentAuthorizedFollowupFilterBeforeGateway(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
	}
	model := &droppedFollowupFilterModel{}
	service.Model = model
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if _, err := service.Ask(ctx, principal, chat.ID, "Show west sales", check, nil); err != nil {
		t.Fatal(err)
	}
	second, err := service.Ask(ctx, principal, chat.ID, "这些订单的 amount 合计是多少？", check, nil)
	if err != nil || gateway.queries != 2 || !strings.Contains(gateway.lastQuery, "WHERE region = 'west'") || second.SQL != gateway.lastQuery {
		t.Fatalf("follow-up filter not carried through the normal gateway: record=%+v query=%q count=%d err=%v", second, gateway.lastQuery, gateway.queries, err)
	}
}
