package chatbi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func relationTestGraph(datasource ID) string {
	sales := browserTableID(datasource, "public", "sales")
	orders := browserTableID(datasource, "public", "orders")
	region := browserFieldID(sales, "region")
	salesRegion := browserFieldID(orders, "sales_region")
	return fmt.Sprintf(`[
		{"id":"%d","shape":"er-rect","position":{"x":10,"y":20},"attrs":{"label":{"text":"<script>forged</script>"}}},
		{"id":"%d","shape":"er-rect","position":{"x":250,"y":30}},
		{"id":"browser-edge","shape":"edge","source":{"cell":"%d","port":"%d"},"target":{"cell":"%d","port":"%d"},"attrs":{"line":{"stroke":"javascript:bad"}}}
	]`, sales, orders, sales, region, orders, salesRegion)
}

func TestOriginalTableRelationsAreGatewayBoundSanitizedAndAudited(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	gateway := &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "orders", Name: "sales_region", DataType: "text"},
	}}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	getPath, savePath := "/api/v1/table_relation/get/10", "/api/v1/table_relation/save/10"
	require.Equal(t, http.StatusUnauthorized,
		accountRequest(t, handler, http.MethodPost, getPath, `[]`, "").Code)
	empty := accountRequest(t, handler, http.MethodPost, getPath, `[]`, token)
	require.Equal(t, http.StatusOK, empty.Code)
	require.Contains(t, empty.Body.String(), `"data":[]`)
	graph := relationTestGraph("10")
	require.Equal(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, savePath, graph, token).Code)
	stored, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.NotContains(t, stored.TableRelations, "<script>")
	require.NotContains(t, stored.TableRelations, "javascript:")
	require.NotContains(t, stored.TableRelations, "browser-edge")
	read := accountRequest(t, handler, http.MethodPost, getPath, `[]`, token)
	require.Equal(t, http.StatusOK, read.Code)
	require.Contains(t, read.Body.String(), `"shape":"edge"`)
	require.Contains(t, read.Body.String(), `"sales_region"`)
	forged := strings.Replace(graph, fmt.Sprint(browserFieldID(browserTableID("10", "public", "orders"), "sales_region")), "999999", 1)
	require.NotEqual(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, savePath, forged, token).Code)
	unchanged, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.Equal(t, stored.TableRelations, unchanged.TableRelations)

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_relation_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, savePath, `[]`, token).Code)
	unchanged, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.Equal(t, stored.TableRelations, unchanged.TableRelations)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_relation_audit`)
	require.NoError(t, err)

	gateway.columns = gateway.columns[:2] // orders revoked by the gateway.
	require.NotEqual(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, getPath, `[]`, token).Code)
	require.Equal(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, savePath, `[]`, token).Code,
		"administrator can clear a stale relation graph")
}

type relationCaptureModel struct {
	hints []SemanticHint
	calls int
}

func (m *relationCaptureModel) Generate(context.Context, ModelConfig, string, string, []Column) (SQLGeneration, error) {
	return SQLGeneration{}, ErrModel
}

func (m *relationCaptureModel) GenerateWithDescriptions(_ context.Context, _ ModelConfig, _, _ string, _ []Column,
	_ []TurnContext, hints []SemanticHint, _ []ExampleHint, _ []RemarkHint) (SQLGeneration, error) {
	m.calls++
	m.hints = append([]SemanticHint(nil), hints...)
	return SQLGeneration{SQL: "SELECT amount FROM public.sales"}, nil
}

func TestAskUsesOnlyCurrentTableRelationsAsNonAuthoritativeHints(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	service.Gateway = &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "orders", Name: "sales_region", DataType: "text"},
	}}
	model := &relationCaptureModel{}
	service.Model = model
	binding, revision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	graph := relationTestGraph("10")
	var raw []relationInputCell
	require.NoError(t, json.Unmarshal([]byte(graph), &raw))
	canonical, _, err := relationGraph(binding, []Column{
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "orders", Name: "sales_region", DataType: "text"},
	}, raw)
	require.NoError(t, err)
	body, err := json.Marshal(canonical)
	require.NoError(t, err)
	binding.TableRelations = string(body)
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, revision))
	chat, err := service.Start(ctx, principal, "10", "sales", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "sales", check, nil)
	require.NoError(t, err)
	require.Equal(t, 1, model.calls)
	require.Len(t, model.hints, 1)
	require.Contains(t, model.hints[0].Description, "public.sales.region relates to public.orders.sales_region")
	require.Contains(t, model.hints[0].Description, "not authorization")
	require.Equal(t, 1, base.queries)
}
