package chatbi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type recordingPinnedEmbedding struct {
	*LocalEmbeddingModel
	calls     []int
	durations []time.Duration
}

func (model *recordingPinnedEmbedding) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	model.calls = append(model.calls, len(texts))
	start := time.Now()
	vectors, err := model.LocalEmbeddingModel.Encode(ctx, texts)
	model.durations = append(model.durations, time.Since(start))
	return vectors, err
}

func (model *recordingPinnedEmbedding) persistentEmbeddingIdentity() string {
	return model.LocalEmbeddingModel.persistentEmbeddingIdentity()
}

type sharedPinnedEmbedding struct {
	*LocalEmbeddingModel
	queryDone  chan struct{}
	started    chan struct{}
	release    chan struct{}
	tableCalls atomic.Int32
}

func (model *sharedPinnedEmbedding) persistentEmbeddingIdentity() string {
	return model.LocalEmbeddingModel.persistentEmbeddingIdentity()
}

func (model *sharedPinnedEmbedding) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 1 {
		vectors, err := model.LocalEmbeddingModel.Encode(ctx, texts)
		model.queryDone <- struct{}{}
		return vectors, err
	}
	if model.tableCalls.Add(1) == 1 {
		close(model.started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-model.release:
		}
	}
	return model.LocalEmbeddingModel.Encode(ctx, texts)
}

func TestLocalEmbeddingModelSharesConcurrentAuthorizedTableBuild(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	local, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	model := &sharedPinnedEmbedding{LocalEmbeddingModel: local, queryDone: make(chan struct{}, 2),
		started: make(chan struct{}), release: make(chan struct{})}
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := make([]Column, 32)
	for index := range columns {
		columns[index] = Column{Schema: "public", Table: fmt.Sprintf("orders_%02d", index),
			Name: "revenue_amount", DataType: "numeric"}
	}
	columns[17] = Column{Schema: "public", Table: "weather", Name: "forecast_temperature", DataType: "text"}
	results := make(chan error, 2)
	ask := func() {
		selected, _, err := selectModelTablesIndexed(context.Background(), store, p, "10", model,
			"weather forecast", columns, nil, 1, 1, false)
		if err == nil && (len(selected) != 1 || selected[0].Table != "weather") {
			err = ErrUnavailable
		}
		results <- err
	}
	go ask()
	select {
	case <-model.started:
	case <-time.After(30 * time.Second):
		t.Fatal("first real-model table build did not start")
	}
	go ask()
	for i := 0; i < 2; i++ {
		select {
		case <-model.queryDone:
		case <-time.After(30 * time.Second):
			t.Fatal("real-model vector question did not complete")
		}
	}
	close(model.release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(90 * time.Second):
			t.Fatal("concurrent real-model table build did not finish")
		}
	}
	if calls := model.tableCalls.Load(); calls != 1 {
		t.Fatalf("real-model concurrent builds inferred the same table batch %d times", calls)
	}
}

// The real ONNX model is intentionally loaded twice so its process-local cache
// cannot explain a warm result from the encrypted persistent index.
func TestLocalEmbeddingModelPersistentSourceIndexAfterModelRestart(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	entries := make([]assistantCatalogEntry, 32)
	for index := range entries {
		id := ID(fmt.Sprintf("%d", index+10))
		entries[index] = assistantCatalogEntry{CatalogID: id, Datasource: id,
			Name: fmt.Sprintf("Source %02d", index), selectionText: fmt.Sprintf("public.orders_%02d revenue amount", index)}
	}
	entries[17].selectionText = "public.weather forecast temperature"
	coldLocal, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	cold := &recordingPinnedEmbedding{LocalEmbeddingModel: coldLocal}
	start := time.Now()
	coldSelected, err := selectAssistantCatalogSourcesIndexed(ctx, store, p, cold, "weather forecast", entries, 1)
	coldElapsed := time.Since(start)
	if closeErr := coldLocal.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || len(coldSelected) != 1 || len(cold.calls) != 2 || cold.calls[0] != 1 || cold.calls[1] != 32 {
		t.Fatalf("cold local source index: selected=%v calls=%v err=%v", coldSelected, cold.calls, err)
	}
	warmLocal, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer warmLocal.Close()
	warm := &recordingPinnedEmbedding{LocalEmbeddingModel: warmLocal}
	start = time.Now()
	warmSelected, err := selectAssistantCatalogSourcesIndexed(ctx, store, p, warm, "weather forecast", entries, 1)
	warmElapsed := time.Since(start)
	if err != nil || len(warmSelected) != 1 || warmSelected[0].CatalogID != coldSelected[0].CatalogID ||
		len(warm.calls) != 1 || warm.calls[0] != 1 {
		t.Fatalf("model restart recomputed source vectors: selected=%v calls=%v err=%v", warmSelected, warm.calls, err)
	}
	t.Logf("32 authorized sources: cold=%s warm-after-model-restart=%s", coldElapsed, warmElapsed)
}

func TestLocalEmbeddingModelPersistentTableIndexAfterModelRestart(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	count := 32
	if raw := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_TABLE_COUNT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 32 || parsed > 500 {
			t.Fatalf("invalid local table count %q", raw)
		}
		count = parsed
	}
	columns := make([]Column, count)
	for index := range columns {
		columns[index] = Column{Schema: "public", Table: fmt.Sprintf("orders_%02d", index),
			Name: "revenue_amount", DataType: "numeric"}
	}
	columns[17] = Column{Schema: "public", Table: "weather", Name: "forecast_temperature", DataType: "text"}
	coldLocal, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	cold := &recordingPinnedEmbedding{LocalEmbeddingModel: coldLocal}
	start := time.Now()
	coldSelected, _, err := selectModelTablesIndexed(ctx, store, p, "10", cold,
		"weather forecast", columns, nil, 1, 1, false)
	coldElapsed := time.Since(start)
	if closeErr := coldLocal.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(cold.calls) == 0 {
		t.Fatalf("local table index made no embedding call: %v", err)
	}
	encoded := 0
	for _, batch := range cold.calls[1:] {
		if batch < 1 || batch > 32 {
			t.Fatalf("local table index exceeded embedding batch limit: %v", cold.calls)
		}
		encoded += batch
	}
	if err != nil || len(coldSelected) != 1 || coldSelected[0].Table != "weather" ||
		len(cold.calls) != 1+(count+31)/32 || cold.calls[0] != 1 || encoded != count {
		t.Fatalf("cold local table index: selected=%v calls=%v err=%v", coldSelected, cold.calls, err)
	}
	warmLocal, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer warmLocal.Close()
	warm := &recordingPinnedEmbedding{LocalEmbeddingModel: warmLocal}
	start = time.Now()
	warmSelected, _, err := selectModelTablesIndexed(ctx, store, p, "10", warm,
		"weather forecast", columns, nil, 1, 1, false)
	warmElapsed := time.Since(start)
	if err != nil || len(warmSelected) != 1 || warmSelected[0].Table != coldSelected[0].Table ||
		len(warm.calls) != 1 || warm.calls[0] != 1 {
		t.Fatalf("model restart recomputed table vectors: selected=%v calls=%v err=%v", warmSelected, warm.calls, err)
	}
	t.Logf("%d authorized tables: cold=%s warm-after-model-restart=%s; cold-inference=%v warm-inference=%v",
		count, coldElapsed, warmElapsed, cold.durations, warm.durations)
}

func TestLocalEmbeddingModelAuthorizedPrewarmThenSelect(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	count := 32
	if raw := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_TABLE_COUNT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 32 || parsed > 500 {
			t.Fatalf("invalid local table count %q", raw)
		}
		count = parsed
	}
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	columns := make([]Column, count)
	for index := range columns {
		columns[index] = Column{Schema: "public", Table: fmt.Sprintf("orders_%02d", index),
			Name: "revenue_amount", DataType: "numeric"}
	}
	columns[17] = Column{Schema: "public", Table: "weather", Name: "forecast_temperature", DataType: "text"}
	gateway.columns = columns
	local, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	service.Embedding = local
	stop := service.EnableTableIndexPrewarm(ctx)
	defer stop()
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	start := time.Now()
	if !service.QueueTableIndexPrewarm(principal, "10", check) {
		t.Fatal("prewarm was not queued")
	}
	deadline := time.After(5 * time.Minute)
	for {
		status := service.TableIndexPrewarmStatus(principal, "10")
		if status.State == "ready" {
			break
		}
		if status.State == "failed" || status.State == "cancelled" {
			t.Fatalf("real model prewarm failed: %+v", status)
		}
		select {
		case <-deadline:
			t.Fatal("real model prewarm timed out")
		case <-time.After(100 * time.Millisecond):
		}
	}
	prewarmElapsed := time.Since(start)
	start = time.Now()
	selected, _, err := selectModelTablesIndexed(ctx, service.Store, principal, "10", local,
		"weather forecast", columns, nil, 1, 1, false)
	firstAfterReady := time.Since(start)
	if err != nil || len(selected) != 1 || selected[0].Table != "weather" {
		t.Fatalf("prewarmed schema selected wrong table: %v %v", selected, err)
	}
	t.Logf("%d authorized tables: prewarm=%s, first selection after ready=%s", count, prewarmElapsed, firstAfterReady)
}

func TestLocalEmbeddingModelColdForegroundBudget(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	columns := make([]Column, 128)
	for index := range columns {
		columns[index] = Column{Schema: "public", Table: fmt.Sprintf("orders_%03d", index),
			Name: "amount", DataType: "numeric"}
	}
	local, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	start := time.Now()
	selected, _, err := selectModelTablesForAsk(ctx, store, p, "10", local,
		"Count rows in orders_017", "Count rows in orders_017", columns, nil,
		1, 1, false, foregroundTableIndexBudget, func(context.Context) error { return nil })
	elapsed := time.Since(start)
	if err != nil || len(selected) != len(columns) {
		t.Fatalf("cold foreground selection did not preserve all authorized tables: count=%d err=%v", len(selected), err)
	}
	if elapsed > 45*time.Second {
		t.Fatalf("foreground index budget exceeded model batch bound: %s", elapsed)
	}
	var persisted int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("timed out cold selection persisted an incomplete index: count=%d err=%v", persisted, err)
	}
	t.Logf("128 authorized tables: cold foreground full-schema fallback=%s", elapsed)
}

// Run locally with the pinned model asset; CI has no model weights.
func TestLocalEmbeddingModelMatchesPinnedSQLBotScores(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	weights, err := os.Open(filepath.Join(path, "model.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, weights); err != nil {
		weights.Close()
		t.Fatal(err)
	}
	if err := weights.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != "716d380a65efde09842642540749bc0535b05f2c66737fea1106731b8b0d7ffb" {
		t.Fatalf("unexpected local model digest: %s", got)
	}
	model, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	vectors, err := model.Encode(context.Background(), []string{"营业额是多少", "销售收入是多少", "今天的天气如何"})
	if err != nil || len(vectors) != 3 {
		t.Fatalf("local vectors: %v", err)
	}
	for _, pair := range []struct {
		index int
		want  float64
	}{{1, 0.816024}, {2, 0.313403}} {
		var dot float64
		for index, value := range vectors[0] {
			dot += float64(value) * float64(vectors[pair.index][index])
		}
		if math.Abs(dot-pair.want) > 0.0005 {
			t.Fatalf("similarity 0/%d = %.6f; original = %.6f", pair.index, dot, pair.want)
		}
	}
	sources := []assistantCatalogEntry{
		{CatalogID: "81", Datasource: "10", Name: "销售收入"},
		{CatalogID: "82", Datasource: "11", Name: "天气预报"},
		{CatalogID: "83", Datasource: "12", Name: "库存管理"},
	}
	chosen, err := selectAssistantCatalogSources(context.Background(), model, "天气预报", sources, 1)
	if err != nil || len(chosen) != 1 || chosen[0].CatalogID != "82" {
		t.Fatalf("real local model selected unexpected datasource: %#v %v", chosen, err)
	}
	indexed, err := selectAssistantCatalogSourcesIndexed(context.Background(), testStore(t),
		Principal{Workspace: "1", User: User{ID: "1"}}, model, "天气预报", sources, 1)
	if err != nil || len(indexed) != 1 || indexed[0].CatalogID != "82" {
		t.Fatalf("real local model and persistent index selected unexpected datasource: %#v %v", indexed, err)
	}
	columns := []Column{
		{Schema: "public", Table: "orders", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "meteo", Name: "temperature", DataType: "numeric"},
	}
	remarks := []RemarkHint{
		{Schema: "public", Table: "orders", Remark: "销售收入"},
		{Schema: "public", Table: "meteo", Remark: "天气预报"},
	}
	selected, _, err := selectModelTables(context.Background(), model, "天气预报", columns, remarks, 1, 1, false)
	if err != nil || len(selected) != 1 || selected[0].Table != "meteo" {
		t.Fatalf("real local model selected unexpected table: %#v %v", selected, err)
	}
}

func TestLocalEmbeddingModelConcurrentCacheAndClose(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	model, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() {
			vectors, err := model.Encode(context.Background(), []string{"营业额是多少", "销售收入是多少"})
			if err == nil && len(vectors) != 2 {
				err = ErrUnavailable
			}
			results <- err
		}()
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := model.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Encode(context.Background(), []string{"营业额是多少"}); err != ErrUnavailable {
		t.Fatalf("closed model accepted inference: %v", err)
	}
}

func TestLocalEmbeddingModelRejectsIncompleteOrChangedAssets(t *testing.T) {
	path := t.TempDir()
	if _, err := NewLocalEmbeddingModel(path); err != ErrUnavailable {
		t.Fatalf("empty model directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(path, "1_Pooling"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("changed model card"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalEmbeddingModel(path); err != ErrUnavailable {
		t.Fatalf("changed model asset: %v", err)
	}
	if err := os.Remove(filepath.Join(path, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(path, "1_Pooling"), filepath.Join(path, "README.md")); err == nil {
		if _, err := NewLocalEmbeddingModel(path); err != ErrUnavailable {
			t.Fatalf("symlinked model asset: %v", err)
		}
	}
}
