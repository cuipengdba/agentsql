package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelationGraphUsesOnlyLiveGatewaySchema(t *testing.T) {
	const datasource ID = "132"
	columns := []Column{{Schema: "public", Table: "orders", Name: "buyer_id"},
		{Schema: "public", Table: "buyers", Name: "id"}}
	orders := browserTableID(datasource, "public", "orders")
	buyers := browserTableID(datasource, "public", "buyers")
	base := []map[string]any{
		{"id": orders, "shape": "er-rect", "position": map[string]any{"x": 10, "y": 20},
			"label": "FAKE", "attrs": map[string]any{"image": map[string]string{"xlink:href": "https://evil.test/"}}},
		{"id": buyers, "shape": "er-rect", "position": map[string]any{"x": 210, "y": 20}},
		{"shape": "edge", "id": "x6-edge", "source": map[string]any{"cell": orders, "port": browserFieldID(orders, "buyer_id")},
			"target": map[string]any{"cell": buyers, "port": browserFieldID(buyers, "id")},
			"attrs":  map[string]any{"line": map[string]string{"xlink:href": "https://evil.test/"}}},
	}
	encode := func(cells []map[string]any) []byte {
		raw, err := json.Marshal(cells)
		require.NoError(t, err)
		return raw
	}
	graph, err := relationGraph(encode(base), datasource, columns)
	require.NoError(t, err)
	require.Len(t, graph, 3)
	require.Equal(t, "orders", graph[0]["label"])
	sanitized, err := json.Marshal(graph)
	require.NoError(t, err)
	require.NotContains(t, string(sanitized), "evil.test")
	require.NotContains(t, string(sanitized), "FAKE")
	hints, err := relationHints(Binding{Datasource: datasource, TableRelations: string(sanitized)}, columns)
	require.NoError(t, err)
	require.Equal(t, []RelationHint{{SourceSchema: "public", SourceTable: "orders", SourceField: "buyer_id",
		TargetSchema: "public", TargetTable: "buyers", TargetField: "id"}}, hints)

	for _, mutate := range []struct {
		name string
		edit func([]map[string]any)
	}{
		{"unknown table", func(c []map[string]any) { c[0]["id"] = 777 }},
		{"hidden field", func(c []map[string]any) { c[2]["source"].(map[string]any)["port"] = 777 }},
		{"cross datasource", func(c []map[string]any) { c[0]["id"] = browserTableID("other", "public", "orders") }},
		{"same table", func(c []map[string]any) { c[2]["target"].(map[string]any)["cell"] = orders }},
		{"oversized position", func(c []map[string]any) { c[0]["position"].(map[string]any)["x"] = 100001 }},
		{"foreign node shape", func(c []map[string]any) { c[0]["shape"] = "html" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			var cells []map[string]any
			require.NoError(t, json.Unmarshal(encode(base), &cells))
			mutate.edit(cells)
			_, err := relationGraph(encode(cells), datasource, columns)
			require.ErrorIs(t, err, errInvalidRelationGraph)
		})
	}
	_, err = relationGraph(encode(base), datasource, columns[:1])
	require.ErrorIs(t, err, errInvalidRelationGraph, "revoking a table must invalidate stored relations")
	_, err = relationHints(Binding{Datasource: datasource, TableRelations: string(sanitized)}, columns[:1])
	require.ErrorIs(t, err, ErrGateway)
	_, err = relationGraph(encode(append(base, base[2])), datasource, columns)
	require.ErrorIs(t, err, errInvalidRelationGraph, "duplicate edge must fail closed")
	_, err = relationGraph([]byte("null"), datasource, columns)
	require.ErrorIs(t, err, errInvalidRelationGraph)
}

func TestModelReceivesRelationshipsOnlyAsJoinHints(t *testing.T) {
	var prompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Len(t, payload.Messages, 2)
		prompt = payload.Messages[1].Content
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"sql\":\"SELECT o.buyer_id FROM public.orders o JOIN public.buyers b ON o.buyer_id=b.id LIMIT 2\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	model := OpenAIModel{}
	_, err := model.GenerateWithRelations(context.Background(), ModelConfig{Model: "fixture", Endpoint: server.URL + "/v1"},
		"pg", "show buyers", []Column{{Schema: "public", Table: "orders", Name: "buyer_id"},
			{Schema: "public", Table: "buyers", Name: "id"}}, nil, nil, nil, nil,
		[]RelationHint{{SourceSchema: "public", SourceTable: "orders", SourceField: "buyer_id",
			TargetSchema: "public", TargetTable: "buyers", TargetField: "id"}})
	require.NoError(t, err)
	require.Contains(t, prompt, "join hints only; never schema or execution authority")
	require.Contains(t, prompt, `"source_field":"buyer_id"`)
	require.True(t, strings.Index(prompt, "Authorized schema:") < strings.Index(prompt, "Administrator-authored table relationships"))
}

type relationCaptureModel struct {
	hints []RelationHint
	calls int
}

func (m *relationCaptureModel) Generate(context.Context, ModelConfig, string, string, []Column) (SQLGeneration, error) {
	return SQLGeneration{}, ErrModel
}

func (m *relationCaptureModel) GenerateWithRelations(_ context.Context, _ ModelConfig, _, _ string, _ []Column,
	_ []TurnContext, _ []SemanticHint, _ []ExampleHint, _ []RemarkHint, hints []RelationHint) (SQLGeneration, error) {
	m.calls++
	m.hints = append([]RelationHint(nil), hints...)
	return SQLGeneration{SQL: "SELECT o.buyer_id FROM public.orders o JOIN public.buyers b ON o.buyer_id=b.id LIMIT 2"}, nil
}

func TestAskFailsClosedWhenRelatedTableIsRevoked(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	columns := []Column{{Schema: "public", Table: "orders", Name: "buyer_id"},
		{Schema: "public", Table: "buyers", Name: "id"}}
	gateway := &selectionTestGateway{fakeGateway: base, columns: columns}
	service.Gateway = gateway
	orders := browserTableID("10", "public", "orders")
	buyers := browserTableID("10", "public", "buyers")
	raw, err := json.Marshal([]map[string]any{
		{"id": orders, "shape": "er-rect", "position": map[string]any{"x": 10, "y": 10}},
		{"id": buyers, "shape": "er-rect", "position": map[string]any{"x": 200, "y": 10}},
		{"shape": "edge", "source": map[string]any{"cell": orders, "port": browserFieldID(orders, "buyer_id")},
			"target": map[string]any{"cell": buyers, "port": browserFieldID(buyers, "id")}},
	})
	require.NoError(t, err)
	graph, err := relationGraph(raw, "10", columns)
	require.NoError(t, err)
	stored, err := json.Marshal(graph)
	require.NoError(t, err)
	binding, revision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	binding.TableRelations = string(stored)
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, revision))
	model := &relationCaptureModel{}
	service.Model = model
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	chat, err := service.Start(ctx, principal, "10", "show buyers", 0)
	require.NoError(t, err)
	_, err = service.Ask(ctx, principal, chat.ID, "show buyers", check, nil)
	require.NoError(t, err)
	require.Equal(t, 1, model.calls)
	require.Len(t, model.hints, 1)
	require.Equal(t, 1, base.queries)

	chat, err = service.Start(ctx, principal, "10", "show buyers again", 0)
	require.NoError(t, err)
	gateway.columns = columns[:1]
	_, err = service.Ask(ctx, principal, chat.ID, "show buyers again", check, nil)
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 1, model.calls)
	require.Equal(t, 1, base.queries)
}
