package chatbi

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type indexedTableFixture struct {
	identity string
	calls    [][]string
	change   func()
}

type revokingTableIndexFixture struct {
	revoke func()
	calls  [][]string
}

type failingQueryAfterRevocation struct{ revoke func() }

func (model *failingQueryAfterRevocation) Encode(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 1 {
		model.revoke()
		return nil, ErrUnavailable
	}
	return (tableFixtureEmbedding{}).Encode(context.Background(), texts)
}

func (*revokingTableIndexFixture) persistentEmbeddingIdentity() string { return "revocation-test-v1" }

func (model *revokingTableIndexFixture) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	model.calls = append(model.calls, append([]string(nil), texts...))
	vectors, err := (tableFixtureEmbedding{}).Encode(ctx, texts)
	if len(texts) > 1 && model.revoke != nil {
		model.revoke()
		model.revoke = nil
	}
	return vectors, err
}

type blockedTableIndexFixture struct {
	started    chan struct{}
	release    chan struct{}
	queryCalls atomic.Int32
	tableCalls atomic.Int32
}

func (*blockedTableIndexFixture) persistentEmbeddingIdentity() string { return "fixed-concurrent-v1" }

func (model *blockedTableIndexFixture) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 1 {
		model.queryCalls.Add(1)
	} else if model.tableCalls.Add(1) == 1 {
		close(model.started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-model.release:
		}
	}
	return tableFixtureEmbedding{}.Encode(ctx, texts)
}

func TestForegroundColdIndexBudgetKeepsOnlyCurrentlyAuthorizedFullSchema(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	model := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	start := time.Now()
	selected, _, err := selectModelTablesForAsk(ctx, store, p, "10", model,
		"weather forecast", "weather forecast", columns, nil, 1, 1, false,
		50*time.Millisecond, func(context.Context) error { return nil })
	if err != nil || len(selected) != len(columns) || time.Since(start) > 2*time.Second {
		t.Fatalf("cold foreground selection did not keep the authorized full schema promptly: %v %v", selected, err)
	}
	var persisted int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("cancelled foreground build wrote vectors: count=%d err=%v", persisted, err)
	}
}

func TestForegroundColdIndexBudgetUsesExplicitAuthorizedTable(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{
		{Schema: "public", Table: "orders", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "customers", Name: "name", DataType: "text"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"},
	}
	remarks := []RemarkHint{
		{Schema: "public", Table: "orders", Remark: "Purchase orders"},
		{Schema: "public", Table: "customers", Remark: "Customers"},
	}
	model := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	selected, selectedRemarks, err := selectModelTablesForAsk(ctx, store, p, "10", model,
		"Count rows in orders", "orders", columns, remarks, 1, 1, true,
		50*time.Millisecond, func(context.Context) error { return nil })
	if err != nil || len(selected) != 1 || selected[0].Table != "orders" ||
		len(selectedRemarks) != 1 || selectedRemarks[0].Table != "orders" {
		t.Fatalf("explicit cold fallback did not keep only current authorized order table: %v %v %v", selected, selectedRemarks, err)
	}
	var persisted int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("cancelled foreground build wrote vectors: count=%d err=%v", persisted, err)
	}
}

func TestExplicitColdFallbackKeepsFullSchemaWhenQuestionIsAmbiguous(t *testing.T) {
	columns := []Column{
		{Schema: "public", Table: "orders", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "customers", Name: "name", DataType: "text"},
	}
	for _, question := range []string{"How many records are there?", `Count rows in "missing_orders"`} {
		selected, _, err := selectExplicitTableFallback(question, "orders", columns, nil, 1, true)
		if err != nil || len(selected) != len(columns) {
			t.Fatalf("ambiguous or unknown quoted table narrowed schema: %q %v %v", question, selected, err)
		}
	}
	selected, _, err := selectExplicitTableFallback("Count rows in orders", "orders", columns, nil, 1, false)
	if err != nil || len(selected) != len(columns) {
		t.Fatalf("disabled keyword selection narrowed schema: %v %v", selected, err)
	}
	selected, _, err = selectExplicitTableFallback("Compare orders and customers", "orders customers", columns, nil, 1, true)
	if err != nil || len(selected) != 2 || selected[0].Table != "orders" || selected[1].Table != "customers" {
		t.Fatalf("explicitly named join tables exceeded the candidate limit incorrectly: %v %v", selected, err)
	}
}

func TestForegroundColdIndexBudgetDoesNotHideRevocation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	model := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	var revoked atomic.Bool
	result := make(chan error, 1)
	go func() {
		_, _, err := selectModelTablesForAsk(ctx, store, p, "10", model,
			"weather forecast", "weather forecast", columns, nil, 1, 1, false,
			100*time.Millisecond, func(context.Context) error {
				if revoked.Load() {
					return ErrGateway
				}
				return nil
			})
		result <- err
	}()
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("cold index build did not start")
	}
	revoked.Store(true)
	select {
	case err := <-result:
		if !errors.Is(err, ErrGateway) {
			t.Fatalf("revocation was hidden by cold-index timeout: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked selection did not return")
	}
	var persisted int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("revoked cold index wrote vectors: count=%d err=%v", persisted, err)
	}
}

func TestBackgroundIndexRetriesAfterForegroundBudgetExpires(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	model := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	foreground := make(chan error, 1)
	go func() {
		selected, _, err := selectModelTablesForAsk(ctx, store, p, "10", model,
			"weather forecast", "weather forecast", columns, nil, 1, 1, false,
			500*time.Millisecond, func(context.Context) error { return nil })
		if err == nil && len(selected) != len(columns) {
			err = ErrUnavailable
		}
		foreground <- err
	}()
	select {
	case <-model.started:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground cold build did not start")
	}
	background := make(chan error, 1)
	go func() {
		selected, _, err := selectModelTablesIndexed(ctx, store, p, "10", model,
			"weather forecast", columns, nil, 1, 1, false)
		if err == nil && (len(selected) != 1 || selected[0].Table != "weather") {
			err = ErrUnavailable
		}
		background <- err
	}()
	select {
	case err := <-foreground:
		if err != nil {
			t.Fatalf("foreground fallback failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("foreground budget did not expire")
	}
	select {
	case err := <-background:
		if err != nil {
			t.Fatalf("background retry failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background did not retry cancelled foreground index flight")
	}
	if calls := model.tableCalls.Load(); calls != 2 {
		t.Fatalf("expected cancelled foreground and successful background batches, got %d", calls)
	}
}

func TestConcurrentAuthorizedTableIndexBuildIsSharedAndCancellable(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	model := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	selectTables := func(ctx context.Context) error {
		selected, _, err := selectModelTablesIndexed(ctx, store, p, "10", model,
			"weather forecast", columns, nil, 1, 1, false)
		if err == nil && (len(selected) != 1 || selected[0].Table != "weather") {
			return ErrUnavailable
		}
		return err
	}
	first := make(chan error, 1)
	go func() { first <- selectTables(ctx) }()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first authorized table build did not start")
	}
	secondCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := selectTables(secondCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting request did not cancel promptly: %v", err)
	}
	third := make(chan error, 1)
	go func() { third <- selectTables(ctx) }()
	deadline := time.After(5 * time.Second)
	for model.queryCalls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("concurrent authorized requests did not reach vector query")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-third:
		t.Fatalf("concurrent build completed before the first index was ready: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(model.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-third; err != nil {
		t.Fatal(err)
	}
	if calls := model.tableCalls.Load(); calls != 1 {
		t.Fatalf("concurrent requests rebuilt the same authorized index %d times", calls)
	}
}

func TestInFlightTableIndexDoesNotShareChangedAuthorizedColumns(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	model := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	first := make(chan error, 1)
	go func() {
		_, _, err := selectModelTablesIndexed(ctx, store, p, "10", model,
			"weather forecast", columns, nil, 1, 1, false)
		first <- err
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first index build did not start")
	}
	changed := append([]Column(nil), columns...)
	changed[1].Name = "temperature"
	second := make(chan error, 1)
	go func() {
		_, _, err := selectModelTablesIndexed(ctx, store, p, "10", model,
			"weather forecast", changed, nil, 1, 1, false)
		second <- err
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("changed authorized schema waited for the prior in-flight index")
	}
	close(model.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if calls := model.tableCalls.Load(); calls != 2 {
		t.Fatalf("changed authorized schema shared old table inference: %d calls", calls)
	}
}

func TestAuthorizedTableIndexWaiterRetriesAfterBuilderCancellation(t *testing.T) {
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"}}
	model := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first := make(chan error, 1)
	go func() {
		_, _, err := selectModelTablesIndexed(firstCtx, store, p, "10", model,
			"weather forecast", columns, nil, 1, 1, false)
		first <- err
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first table build did not start")
	}
	second := make(chan error, 1)
	go func() {
		selected, _, err := selectModelTablesIndexed(context.Background(), store, p, "10", model,
			"weather forecast", columns, nil, 1, 1, false)
		if err == nil && (len(selected) != 1 || selected[0].Table != "weather") {
			err = ErrUnavailable
		}
		second <- err
	}()
	deadline := time.After(5 * time.Second)
	for model.queryCalls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("waiting request did not encode its own question")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-second:
		t.Fatalf("waiting request completed before builder cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled builder: %v", err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("live waiter did not rebuild under its own context: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live waiter did not finish after builder cancellation")
	}
	if calls := model.tableCalls.Load(); calls != 2 {
		t.Fatalf("live waiter did not retry the cancelled table batch: %d calls", calls)
	}
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

func TestRevokedSchemaDuringTableInferenceCannotPersistIndex(t *testing.T) {
	for _, changed := range []string{"row_rule", "visible_column"} {
		t.Run(changed, func(t *testing.T) {
			ctx := context.Background()
			service, principal, gateway, _, token := serviceFixture(t)
			gateway.columns = []Column{
				{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
				{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
				{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"},
			}
			settings := defaultChatSettings()
			settings.Embedding.TableCount = 1
			if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
				t.Fatal(err)
			}
			var rule DataPermissionRule
			if changed == "row_rule" {
				rule = testActiveRowPermission(t, "east")
				if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0); err != nil {
					t.Fatal(err)
				}
			}
			embedding := &revokingTableIndexFixture{revoke: func() {
				if changed == "row_rule" {
					rule.Enable = false
					if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 1); err != nil {
						t.Fatal(err)
					}
				} else {
					gateway.columns[0].Name = "renamed_amount"
				}
			}}
			service.Embedding = embedding
			chat, err := service.Start(ctx, principal, "10", "weather", 0)
			if err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
			if _, err := service.Ask(ctx, principal, chat.ID, "weather forecast", check, nil); !errors.Is(err, ErrGateway) {
				t.Fatalf("changed authorization was not rejected: %v; calls=%q", err, embedding.calls)
			}
			var persisted int
			if err := service.Store.db.QueryRowContext(ctx,
				`SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 0 {
				t.Fatalf("revoked snapshot persisted index: count=%d err=%v", persisted, err)
			}
			if gateway.queries != 0 {
				t.Fatalf("revoked snapshot reached database: %d queries", gateway.queries)
			}
		})
	}
}

func TestInferenceFallbackCannotUseRevokedSchema(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, model, token := serviceFixture(t)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"},
	}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	service.Embedding = &failingQueryAfterRevocation{revoke: func() {
		gateway.columns[0].Name = "renamed_amount"
	}}
	chat, err := service.Start(ctx, principal, "10", "weather", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if _, err := service.Ask(ctx, principal, chat.ID, "weather forecast", check, nil); !errors.Is(err, ErrGateway) {
		t.Fatalf("fallback used a changed schema: %v", err)
	}
	if model.calls != 0 || gateway.queries != 0 {
		t.Fatalf("fallback reached model or database: model=%d queries=%d", model.calls, gateway.queries)
	}
}
