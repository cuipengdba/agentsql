package chatbi

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type indexedTableFixture struct {
	identity string
	calls    [][]string
	change   func()
}

func (model *indexedTableFixture) persistentEmbeddingIdentity() string { return model.identity }

func (model *indexedTableFixture) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	model.calls = append(model.calls, append([]string(nil), texts...))
	if model.change != nil {
		change := model.change
		model.change = nil
		change()
	}
	return tableFixtureEmbedding{}.Encode(ctx, texts)
}

func TestAuthorizedTableVectorsPersistAndInvalidate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat.db")
	secret := []byte(strings.Repeat("t", 32))
	store, err := OpenStore(ctx, path, secret)
	if err != nil {
		t.Fatal(err)
	}
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	selectTables := func(model *indexedTableFixture, datasource ID, visible []Column) []Column {
		t.Helper()
		selected, _, err := selectModelTablesIndexed(ctx, store, p, datasource, model,
			"weather forecast", visible, nil, 1, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		return selected
	}
	cold := &indexedTableFixture{identity: "fixed-v1"}
	if got := selectTables(cold, "10", columns); len(got) != 1 || got[0].Table != "weather" || len(cold.calls) != 2 {
		t.Fatalf("initial authorized table selection: got=%v calls=%v", got, cold.calls)
	}
	var ciphertext []byte
	if err := store.db.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind='table-vector-v1' LIMIT 1`).Scan(&ciphertext); err != nil ||
		bytes.Contains(ciphertext, []byte("public.sales")) || bytes.Contains(ciphertext, []byte("forecast")) {
		t.Fatalf("table text was not encrypted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, path, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	warm := &indexedTableFixture{identity: "fixed-v1"}
	if got := selectTables(warm, "10", columns); len(got) != 1 || got[0].Table != "weather" || len(warm.calls) != 1 {
		t.Fatalf("warm restart recomputed table vectors: got=%v calls=%v", got, warm.calls)
	}
	changed := append([]Column(nil), columns...)
	changed[1].Name = "new_forecast"
	modified := &indexedTableFixture{identity: "fixed-v1"}
	_ = selectTables(modified, "10", changed)
	if len(modified.calls) != 2 || len(modified.calls[1]) != 1 {
		t.Fatalf("changed authorized column reused old vector: calls=%v", modified.calls)
	}
	newSource := &indexedTableFixture{identity: "fixed-v1"}
	_ = selectTables(newSource, "11", columns)
	if len(newSource.calls) != 2 || len(newSource.calls[1]) != 2 {
		t.Fatalf("different datasource reused vectors: calls=%v", newSource.calls)
	}
	newModel := &indexedTableFixture{identity: "fixed-v2"}
	_ = selectTables(newModel, "10", columns)
	if len(newModel.calls) != 2 || len(newModel.calls[1]) != 2 {
		t.Fatalf("changed model reused vectors: calls=%v", newModel.calls)
	}
	other := p
	other.User.ID = "2"
	otherModel := &indexedTableFixture{identity: "fixed-v1"}
	if _, _, err := selectModelTablesIndexed(ctx, store, other, "10", otherModel,
		"weather forecast", columns, nil, 1, 1, false); err != nil || len(otherModel.calls) != 2 {
		t.Fatalf("other user reused vectors: calls=%v err=%v", otherModel.calls, err)
	}
}

func TestTableVectorInputUsesOriginalVisibleMetadataShape(t *testing.T) {
	model := &indexedTableFixture{identity: "fixed-v1"}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "weather", Name: "temperature", DataType: "numeric"}}
	remarks := []RemarkHint{{Schema: "public", Table: "sales", Remark: "销售"},
		{Schema: "public", Table: "sales", Column: "amount", Remark: "营收"}}
	if _, _, err := selectModelTables(context.Background(), model, "weather", columns, remarks, 1, 1, false); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 2 || len(model.calls[1]) != 2 ||
		model.calls[1][0] != "# Table: sales, 销售\n[\n(amount:numeric, 营收),\n(region:text)\n]\n" ||
		model.calls[1][1] != "# Table: weather\n[\n(temperature:numeric)\n]\n" {
		t.Fatalf("table vectors did not use the original metadata shape: %q", model.calls)
	}
}

func TestCorruptTableIndexKeepsCurrentAuthorizedSchema(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	model := &indexedTableFixture{identity: "fixed-v1"}
	if _, _, err := selectModelTablesIndexed(ctx, store, p, "10", model, "weather", columns, nil, 1, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE chatbi_objects SET body=? WHERE kind='table-vector-v1'`, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	selected, _, err := selectModelTablesIndexed(ctx, store, p, "10", model, "weather", columns, nil, 1, 1, false)
	if err != nil || len(selected) != len(columns) {
		t.Fatalf("corrupt index narrowed visible tables: selected=%v err=%v", selected, err)
	}
}

func TestWarmTableIndexDoesNotBypassPermissionRevocation(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	columns := []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "temperature", DataType: "numeric"}}
	gateway.columns = columns
	rule := testActiveRowPermission(t, "east")
	if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0); err != nil {
		t.Fatal(err)
	}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	warm := &indexedTableFixture{identity: "fixed-v1"}
	if _, _, err := selectModelTablesIndexed(ctx, service.Store, principal, "10", warm,
		"sales", columns, nil, 1, 1, false); err != nil {
		t.Fatal(err)
	}
	model := &tableCaptureModel{}
	service.Model = model
	service.Embedding = &indexedTableFixture{identity: "fixed-v1", change: func() {
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
		t.Fatalf("warm index let revoked schema reach model/query: columns=%v queries=%d err=%v", model.columns, gateway.queries, err)
	}
}
