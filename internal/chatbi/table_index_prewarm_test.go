package chatbi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func waitTableIndexPrewarm(t *testing.T, service *Service, p Principal, datasource ID, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		status := service.TableIndexPrewarmStatus(p, datasource)
		if status.State == want {
			return
		}
		if status.State == "failed" || status.State == "cancelled" {
			t.Fatalf("prewarm ended before %s: %+v", want, status)
		}
		select {
		case <-deadline:
			t.Fatalf("prewarm did not reach %s: %+v", want, status)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAuthenticatedDatasourceListPrewarmsOnlyCurrentUserIndex(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"},
	}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	embedding := &indexedTableFixture{identity: "fixed-prewarm-test-v1"}
	service.Embedding = embedding
	stop := service.EnableTableIndexPrewarm(ctx)
	defer stop()
	handler, err := NewHandler(service, "v4.5.0")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/datasource/list", nil)
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated datasource list: %d %s", response.Code, response.Body.String())
	}
	waitTableIndexPrewarm(t, service, principal, "10", "ready")
	if len(embedding.calls) != 1 || len(embedding.calls[0]) != 2 {
		t.Fatalf("prewarm did not encode current authorized tables: %q", embedding.calls)
	}
	var persisted int
	if err := service.Store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 2 {
		t.Fatalf("expected two user-scoped encrypted vectors: count=%d err=%v", persisted, err)
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agentsql/table-index/10", nil)
	statusRequest.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), "ready") ||
		strings.Contains(statusResponse.Body.String(), "amount") || strings.Contains(statusResponse.Body.String(), "forecast") {
		t.Fatalf("index status leaked metadata or hid readiness: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	other := principal
	other.User.ID = "2"
	if got := service.TableIndexPrewarmStatus(other, "10").State; got != "idle" {
		t.Fatalf("another user observed index state: %s", got)
	}
}

func TestRevocationDuringPrewarmDiscardsOldVectors(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"},
	}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	embedding := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	service.Embedding = embedding
	stop := service.EnableTableIndexPrewarm(ctx)
	defer stop()
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if !service.QueueTableIndexPrewarm(principal, "10", check) || !service.QueueTableIndexPrewarm(principal, "10", check) {
		t.Fatal("a duplicate authorized job was not coalesced")
	}
	select {
	case <-embedding.started:
	case <-time.After(20 * time.Second):
		t.Fatal("prewarm did not reach local inference")
	}
	gateway.columns[0].Name = "renamed_amount"
	close(embedding.release)
	waitTableIndexPrewarm(t, service, principal, "10", "failed")
	if got := service.TableIndexPrewarmStatus(principal, "10").Reason; got != "snapshot_recheck_failed" {
		t.Fatalf("prewarm failure category = %q, want snapshot_recheck_failed", got)
	}
	if embedding.tableCalls.Load() != 1 {
		t.Fatalf("duplicate prewarm rebuilt index %d times", embedding.tableCalls.Load())
	}
	var persisted int
	if err := service.Store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("revoked prewarm persisted old vectors: count=%d err=%v", persisted, err)
	}
}

func TestPrewarmShutdownCancelsInferenceBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service, principal, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "weather", Name: "forecast", DataType: "text"},
	}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	embedding := &blockedTableIndexFixture{started: make(chan struct{}), release: make(chan struct{})}
	service.Embedding = embedding
	stop := service.EnableTableIndexPrewarm(ctx)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if !service.QueueTableIndexPrewarm(principal, "10", check) {
		t.Fatal("prewarm was not queued")
	}
	select {
	case <-embedding.started:
	case <-time.After(20 * time.Second):
		cancel()
		stop()
		t.Fatal("prewarm did not reach local inference")
	}
	cancel()
	stop()
	if status := service.TableIndexPrewarmStatus(principal, "10"); status.State != "cancelled" || status.Reason != "cancelled" {
		t.Fatalf("shutdown left a live job: %+v", status)
	}
	var persisted int
	if err := service.Store.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM chatbi_objects WHERE kind='table-vector-v1'`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("shutdown persisted vectors: count=%d err=%v", persisted, err)
	}
}
