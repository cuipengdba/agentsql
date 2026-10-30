package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These fakes exercise failure boundaries; they are never live model/database
// acceptance evidence. Release acceptance separately uses real services.
type fakeGateway struct {
	schemaErr, queryErr error
	columns             []Column
	schemaCalls         int
	queries             int
	audit               int64
	lastQuery           string
	queryData           *QueryData
	afterQuery          func()
	afterSchema         func()
}

func (g *fakeGateway) Schema(context.Context, Principal, Binding) ([]Column, error) {
	g.schemaCalls++
	if g.afterSchema != nil {
		g.afterSchema()
	}
	if g.schemaErr != nil {
		return nil, g.schemaErr
	}
	if g.columns != nil {
		return g.columns, nil
	}
	return []Column{{"public", "sales", "amount", "numeric"}}, nil
}
func (g *fakeGateway) Query(_ context.Context, _ Principal, _ Binding, query string) (QueryData, error) {
	g.queries++
	g.lastQuery = query
	if g.afterQuery != nil {
		g.afterQuery()
	}
	if g.queryData != nil {
		return *g.queryData, g.queryErr
	}
	return QueryData{Fields: []string{"amount"}, Data: []map[string]any{{"amount": "123"}}, AuditID: g.audit}, g.queryErr
}

type fakeModel struct {
	calls int
	err   error
	after func()
}

type historyModel struct {
	histories [][]TurnContext
}

func (m *historyModel) Generate(context.Context, ModelConfig, string, string, []Column) (SQLGeneration, error) {
	return SQLGeneration{}, errors.New("contextual generator was bypassed")
}

func (m *historyModel) GenerateWithHistory(_ context.Context, _ ModelConfig, _, _ string, _ []Column, history []TurnContext) (SQLGeneration, error) {
	m.histories = append(m.histories, append([]TurnContext(nil), history...))
	return SQLGeneration{SQL: "SELECT amount FROM public.sales"}, nil
}

func TestQuestionHistoryExcludesFailuresAndOtherChats(t *testing.T) {
	ctx := context.Background()
	s, p, gateway, _, token := serviceFixture(t)
	model := &historyModel{}
	s.Model = model
	chat, err := s.Start(ctx, p, "10", "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := s.Identity.Verify(ctx, token); return err }
	if _, err := s.Ask(ctx, p, chat.ID, "first", check, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, p, chat.ID, "second", check, nil); err != nil {
		t.Fatal(err)
	}
	gateway.queryErr = ErrGateway
	if _, err := s.Ask(ctx, p, chat.ID, "failed", check, nil); err == nil {
		t.Fatal("expected gateway failure")
	}
	gateway.queryErr = nil
	if _, err := s.Ask(ctx, p, chat.ID, "fourth", check, nil); err != nil {
		t.Fatal(err)
	}
	other, err := s.Start(ctx, p, "10", "separate", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, p, other.ID, "separate", check, nil); err != nil {
		t.Fatal(err)
	}
	if len(model.histories) != 5 || len(model.histories[0]) != 0 || len(model.histories[1]) != 1 || len(model.histories[3]) != 2 || len(model.histories[4]) != 0 {
		t.Fatalf("history scope mismatch: %#v", model.histories)
	}
	if model.histories[3][0].Question != "first" || model.histories[3][1].Question != "second" {
		t.Fatal("failed or out-of-order turn included in context")
	}
	if gateway.schemaCalls != 7 { // Two chat starts and all five questions.
		t.Fatal("current authorized schema was not fetched for every turn")
	}
	revision, err := s.Store.get(ctx, bindingKey(p, "10"), new(Binding))
	if err != nil || s.Store.delete(ctx, bindingKey(p, "10"), revision) != nil {
		t.Fatal("could not revoke binding")
	}
	if _, err := s.Ask(ctx, p, chat.ID, "after revocation", check, nil); err == nil || len(model.histories) != 5 {
		t.Fatal("revoked binding reached the model")
	}
}

func TestRecentQuestionsAreScopedDeduplicatedAndRevocable(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	first, err := service.Start(ctx, principal, "10", "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"first", "second"} {
		if _, err := service.Ask(ctx, principal, first.ID, question, check, nil); err != nil {
			t.Fatal(err)
		}
	}
	gateway.queryErr = ErrGateway
	if _, err := service.Ask(ctx, principal, first.ID, "failed", check, nil); err == nil {
		t.Fatal("expected query failure")
	}
	gateway.queryErr = nil
	second, err := service.Start(ctx, principal, "10", "second", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ask(ctx, principal, second.ID, "first", check, nil); err != nil {
		t.Fatal(err)
	}
	questions, err := service.RecentQuestions(ctx, principal, "10")
	if err != nil || len(questions) != 3 || questions[0] != "first" || questions[1] != "failed" || questions[2] != "second" {
		t.Fatalf("recent questions mismatch: %#v, %v", questions, err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/chat/recent_questions/10", nil)
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "123") || !strings.Contains(response.Body.String(), "failed") {
		t.Fatal("recent question route leaked result rows or lost the user's question")
	}
	gateway.schemaErr = ErrGateway
	if _, err := service.RecentQuestions(ctx, principal, "10"); !errors.Is(err, ErrGateway) {
		t.Fatal("gateway schema failure returned historical questions")
	}
	failureRequest := httptest.NewRequest(http.MethodGet, "/api/v1/chat/recent_questions/10", nil)
	failureRequest.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	failureResponse := httptest.NewRecorder()
	router.ServeHTTP(failureResponse, failureRequest)
	if failureResponse.Code == http.StatusOK || strings.Contains(failureResponse.Body.String(), "first") {
		t.Fatal("gateway schema failure exposed historical questions")
	}
	gateway.schemaErr = nil
	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke binding")
	}
	if _, err := service.RecentQuestions(ctx, principal, "10"); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked datasource returned history")
	}
	revokedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/chat/recent_questions/10", nil)
	revokedRequest.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	revokedResponse := httptest.NewRecorder()
	router.ServeHTTP(revokedResponse, revokedRequest)
	if revokedResponse.Code == http.StatusOK || strings.Contains(revokedResponse.Body.String(), "first") {
		t.Fatal("revoked datasource exposed historical questions")
	}
}

func (m *fakeModel) Generate(context.Context, ModelConfig, string, string, []Column) (SQLGeneration, error) {
	m.calls++
	if m.after != nil {
		m.after()
	}
	return SQLGeneration{SQL: "SELECT amount FROM public.sales", TotalTokens: 12}, m.err
}

func serviceFixture(t *testing.T) (*Service, Principal, *fakeGateway, *fakeModel, string) {
	t.Helper()
	ctx := context.Background()
	store := testStore(t)
	user := testUser(t, "1", "analyst")
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	identity := testIdentity(t, store)
	_, token, err := identity.Login(ctx, user.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := identity.Verify(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{Datasource: "10", Workspace: "1", User: "1", Name: "fixture", Dialect: "pg", BindingID: "binding", ConnectorToken: "fixture-connector", AgentKey: "fixture-agent"}
	if err := store.put(ctx, bindingKey(p, "10"), b, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, objectKey{"model", "1", "workspace", "default"}, ModelConfig{ID: "20", Model: "fixture", Endpoint: "http://127.0.0.1/v1"}, 0); err != nil {
		t.Fatal(err)
	}
	g := &fakeGateway{audit: 1}
	m := &fakeModel{}
	return &Service{Store: store, Identity: identity, Gateway: g, Model: m}, p, g, m, token
}

func TestHistoricalReadsShareQueryAdmissionLimit(t *testing.T) {
	s, _, gateway, _, token := serviceFixture(t)
	router, err := NewHandler(s, "test")
	if err != nil {
		t.Fatal(err)
	}
	h := router.(*Handler)
	for i := 0; i < cap(h.slots); i++ {
		h.slots <- struct{}{}
	}
	for _, path := range []string{"/api/v1/chat/1/with_data", "/api/v1/chat/record/1/data", "/api/v1/chat/record/1/data_live"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		cancel()
		if response.Code != http.StatusTooManyRequests || gateway.queries != 0 {
			t.Fatal("historical query bypassed bounded admission")
		}
	}
	// Lightweight health probes remain available while data slots are occupied.
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatal("data load blocked health probe")
	}
}

func TestHistoricalReadsReserveOneSlotForNewQuestion(t *testing.T) {
	s, _, _, _, token := serviceFixture(t)
	router, err := NewHandlerWithLimit(s, "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	h := router.(*Handler)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	history := h.boundedHistory(func(w http.ResponseWriter, _ *http.Request, _ Principal, _ string) {
		started <- struct{}{}
		<-release
		writeJSON(w, http.StatusOK, "history")
	})
	done := make(chan int, 2)
	for i := 0; i < 2; i++ {
		request := httptest.NewRequest(http.MethodGet, "/history", nil)
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		go func() { history(response, request); done <- response.Code }()
		if i == 0 {
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("first historical read did not start")
			}
		}
	}
	deadline := time.After(time.Second)
	for len(h.readQueue) != 2 {
		select {
		case <-deadline:
			t.Fatal("second historical read did not queue")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if len(h.historySeats) != 1 || len(h.slots) != 1 {
		t.Fatal("queued historical read consumed the reserved question slot")
	}
	question := httptest.NewRecorder()
	h.bounded(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, "question") })(question, httptest.NewRequest(http.MethodPost, "/question", nil))
	if question.Code != http.StatusOK {
		t.Fatalf("new question rejected behind historical reads: %d", question.Code)
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case status := <-done:
			if status != http.StatusOK {
				t.Fatalf("historical read status %d", status)
			}
		case <-time.After(time.Second):
			t.Fatal("historical read did not drain")
		}
	}
}

func TestHistoricalReadQueueDrainsAndRechecksSession(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued_read", true: "revoked_while_queued"}[revoked], func(t *testing.T) {
			s, _, _, _, token := serviceFixture(t)
			router, err := NewHandlerWithLimit(s, "test", 1)
			if err != nil {
				t.Fatal(err)
			}
			h := router.(*Handler)
			h.slots <- struct{}{}
			handler := h.boundedHistory(func(w http.ResponseWriter, _ *http.Request, _ Principal, _ string) {
				writeJSON(w, http.StatusOK, "complete")
			})
			request := httptest.NewRequest(http.MethodGet, "/history", nil)
			request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { handler(response, request); close(done) }()
			deadline := time.After(time.Second)
			for len(h.readQueue) == 0 {
				select {
				case <-deadline:
					t.Fatal("history request never entered the queue")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if revoked {
				if err := s.Identity.Logout(context.Background(), token); err != nil {
					t.Fatal(err)
				}
			}
			<-h.slots
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("queued history did not finish")
			}
			want := http.StatusOK
			if revoked {
				want = http.StatusUnauthorized
			}
			if response.Code != want || len(h.readQueue) != 0 || len(h.slots) != 0 {
				t.Fatalf("queued history status %d, expected %d", response.Code, want)
			}
		})
	}
}

func TestHistoricalHTTPReadRechecksLogoutBeforeReleasingResults(t *testing.T) {
	for _, route := range []string{"chat", "record", "live"} {
		t.Run(route, func(t *testing.T) {
			s, p, gateway, _, token := serviceFixture(t)
			ctx := context.Background()
			chat, err := s.Start(ctx, p, "10", "fixture", 0)
			if err != nil {
				t.Fatal(err)
			}
			record, err := s.Ask(ctx, p, chat.ID, "fixture", func(context.Context) error { return nil }, nil)
			if err != nil {
				t.Fatal(err)
			}
			gateway.afterQuery = func() {
				if err := s.Identity.Logout(ctx, token); err != nil {
					t.Fatal(err)
				}
			}
			path := "/api/v1/chat/" + string(chat.ID) + "/with_data"
			if route != "chat" {
				path = "/api/v1/chat/record/" + string(record.ID) + "/data"
				if route == "live" {
					path += "_live"
				}
			}
			router, err := NewHandler(s, "test")
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "123") {
				t.Fatal("logout during a historical query did not suppress the response")
			}
		})
	}
}

func TestBindingChangeDuringQueryNeverReleasesRows(t *testing.T) {
	for _, path := range []string{"ask", "chat_history", "record_history"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			s, p, gateway, _, token := serviceFixture(t)
			chat, err := s.Start(ctx, p, "10", "fixture", 0)
			if err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context) error { _, err := s.Identity.Verify(ctx, token); return err }
			var record Record
			if path != "ask" {
				record, err = s.Ask(ctx, p, chat.ID, "amount", check, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			gateway.afterQuery = func() {
				var binding Binding
				revision, err := s.Store.get(ctx, bindingKey(p, "10"), &binding)
				if err != nil {
					t.Fatal(err)
				}
				binding.ConnectorToken = "replacement-connector"
				if err := s.Store.put(ctx, bindingKey(p, "10"), binding, revision); err != nil {
					t.Fatal(err)
				}
			}
			switch path {
			case "ask":
				returned, err := s.Ask(ctx, p, chat.ID, "amount", check, nil)
				if !errors.Is(err, ErrGateway) || returned.Data != nil {
					t.Fatalf("binding changed during ask: %v, %#v", err, returned.Data)
				}
				stored, _, err := s.chat(ctx, p, chat.ID)
				if err != nil || len(stored.Records) != 1 || stored.Records[0].Error == "" || stored.Records[0].Data != nil {
					t.Fatal("binding replacement did not persist a safe terminal state")
				}
			case "chat_history":
				returned, err := s.ReadChat(ctx, p, chat.ID, true)
				if !errors.Is(err, ErrGateway) || len(returned.Records) != 0 {
					t.Fatalf("binding changed during history: %v, %#v", err, returned.Records)
				}
			case "record_history":
				returned, err := s.Record(ctx, p, record.ID, true)
				if !errors.Is(err, ErrGateway) || returned.Data != nil {
					t.Fatalf("binding changed during record replay: %v, %#v", err, returned.Data)
				}
			}
		})
	}
}

func TestBindingRevokedDuringSchemaStopsBeforeModel(t *testing.T) {
	ctx := context.Background()
	s, p, gateway, model, token := serviceFixture(t)
	chat, err := s.Start(ctx, p, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	gateway.afterSchema = func() {
		var binding Binding
		revision, err := s.Store.get(ctx, bindingKey(p, "10"), &binding)
		if err != nil || s.Store.delete(ctx, bindingKey(p, "10"), revision) != nil {
			t.Fatal("could not revoke binding during schema")
		}
	}
	check := func(ctx context.Context) error { _, err := s.Identity.Verify(ctx, token); return err }
	record, err := s.Ask(ctx, p, chat.ID, "amount", check, nil)
	if !errors.Is(err, ErrNotFound) || record.Data != nil || model.calls != 0 || gateway.queries != 0 {
		t.Fatalf("revoked schema reached model or query: %v", err)
	}
}

func TestBindingDeleteAndRecreateAtSameRevisionCannotReleaseRows(t *testing.T) {
	ctx := context.Background()
	s, p, gateway, _, token := serviceFixture(t)
	chat, err := s.Start(ctx, p, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	gateway.afterQuery = func() {
		var binding Binding
		revision, err := s.Store.get(ctx, bindingKey(p, "10"), &binding)
		if err != nil || s.Store.delete(ctx, bindingKey(p, "10"), revision) != nil || s.Store.put(ctx, bindingKey(p, "10"), binding, 0) != nil {
			t.Fatal("could not recreate the identical binding at revision one")
		}
	}
	check := func(ctx context.Context) error { _, err := s.Identity.Verify(ctx, token); return err }
	record, err := s.Ask(ctx, p, chat.ID, "amount", check, nil)
	if !errors.Is(err, ErrGateway) || record.Data != nil {
		t.Fatalf("same-revision binding recreation released rows: %v", err)
	}
}

func TestConfiguredAdmissionLimitAndStaticAssetBoundary(t *testing.T) {
	s, _, _, _, _ := serviceFixture(t)
	for _, limit := range []int{0, -1, 17} {
		if _, err := NewHandlerWithLimit(s, "test", limit); err == nil {
			t.Fatal("invalid concurrency limit accepted")
		}
	}
	router, err := NewHandlerWithLimit(s, "test", 1)
	if err != nil || cap(router.(*Handler).slots) != 1 {
		t.Fatal("configured admission budget not applied")
	}
	for path, expected := range map[string]int{"/": 200, "/ui/app.js": 200, "/ui/chart.js": 200, "/ui/dashboard.js": 200, "/ui/user-variables.js": 200, "/ui/source-relations.js": 200, "/ui/app.css": 200, "/ui/mark.svg": 200, "/ui/index.html": 404, "/ui/": 404} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != expected {
			t.Fatalf("asset %s: %d, expected %d", path, response.Code, expected)
		}
	}
}

func TestQuestionFailureBoundariesAndTerminalPersistence(t *testing.T) {
	for _, stage := range []string{"schema", "model", "gateway", "missing-audit", "revoked-after-model", "revoked-after-query", "storage"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			s, p, g, m, token := serviceFixture(t)
			chat, err := s.Start(ctx, p, "10", "amount", 0)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "schema":
				g.schemaErr = ErrGateway
			case "model":
				m.err = ErrModel
			case "gateway":
				g.queryErr = ErrGateway
			case "missing-audit":
				g.audit = 0
			case "revoked-after-model":
				m.after = func() {
					if err := s.Identity.Logout(ctx, token); err != nil {
						t.Fatal(err)
					}
				}
			case "revoked-after-query":
				g.afterQuery = func() {
					if err := s.Identity.Logout(ctx, token); err != nil {
						t.Fatal(err)
					}
				}
			case "storage":
				g.afterQuery = func() { _ = s.Store.Close() }
			}
			check := func(ctx context.Context) error { _, err := s.Identity.Verify(ctx, token); return err }
			record, err := s.Ask(ctx, p, chat.ID, "amount", check, nil)
			if err == nil || record.Data != nil {
				t.Fatal("failed operation released data")
			}
			if stage == "schema" && m.calls != 0 {
				t.Fatal("model saw unauthorized schema")
			}
			if (stage == "schema" || stage == "model" || stage == "revoked-after-model") && g.queries != 0 {
				t.Fatal("database query ran after failed prerequisite")
			}
			if stage != "storage" {
				stored, _, err := s.chat(ctx, p, chat.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(stored.Records) != 1 || !stored.Records[0].Finish || stored.Records[0].Error == "" || stored.Records[0].Data != nil || !stored.LeaseUntil.IsZero() {
					t.Fatal("failure state was not durable")
				}
			}
		})
	}
}

func TestQuestionPersistsBeforeDeliveryAndProtectsHistory(t *testing.T) {
	ctx := context.Background()
	s, p, g, _, token := serviceFixture(t)
	chat, err := s.Start(ctx, p, "10", "amount", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := s.Identity.Verify(ctx, token); return err }
	record, err := s.Ask(ctx, p, chat.ID, "amount", check, nil)
	if err != nil || record.Data == nil || record.Data.AuditID != 1 {
		t.Fatalf("question: %v", err)
	}
	var chart struct {
		Type    string `json:"type"`
		Columns []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"columns"`
	}
	if json.Unmarshal([]byte(record.Chart), &chart) != nil || chart.Type != "table" || len(chart.Columns) != 1 || chart.Columns[0].Value != "amount" || strings.Contains(record.Chart, "123") {
		t.Fatalf("controlled result did not produce metadata-only table chart: %s", record.Chart)
	}
	stored, _, err := s.chat(ctx, p, chat.ID)
	if err != nil || stored.Records[0].ID != record.ID || stored.Records[0].Data == nil || stored.Records[0].Chart != record.Chart {
		t.Fatal("result delivered before persistence")
	}
	without, err := s.ReadChat(ctx, p, chat.ID, false)
	if err != nil || without.Records[0].Data != nil {
		t.Fatal("metadata history leaked cached rows")
	}
	g.queryErr = ErrGateway
	if _, err := s.ReadChat(ctx, p, chat.ID, true); !errors.Is(err, ErrGateway) {
		t.Fatal("cached rows bypassed current permissions")
	}
	g.queryErr = nil
	g.audit = 0
	if _, err := s.ReadChat(ctx, p, chat.ID, true); !errors.Is(err, ErrGateway) {
		t.Fatal("historical read accepted a result without durable query audit")
	}
	other := testUser(t, "2", "other")
	if err := s.Store.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadChat(ctx, Principal{User: other, Workspace: "1"}, chat.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-user history read")
	}
}

func TestQuestionLeaseRejectsConcurrentWork(t *testing.T) {
	ctx := context.Background()
	s, p, _, _, token := serviceFixture(t)
	chat, err := s.Start(ctx, p, "10", "amount", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := s.Identity.Verify(ctx, token); return err }
	_, err = s.Ask(ctx, p, chat.ID, "amount", check, func(ID) error {
		if _, err := s.Ask(ctx, p, chat.ID, "concurrent", check, nil); !errors.Is(err, ErrBusy) {
			t.Fatal("concurrent question was not rejected")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPIdentityScopeAndSSE(t *testing.T) {
	s, p, _, _, token := serviceFixture(t)
	handler, err := NewHandler(s, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if auth != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if r := request("GET", "/api/v1/user/info", "", ""); r.Code != 401 {
		t.Fatal("missing credentials accepted")
	}
	if r := request("POST", "/api/v1/login/access-token", "username=analyst&password=Synthetic-password-1", ""); r.Code != 400 {
		t.Fatal("legacy encrypted login accepted plaintext")
	}
	r := request("GET", "/api/v1/user/info", "", token)
	if r.Code != 200 || strings.Contains(r.Body.String(), "password") || strings.Contains(r.Body.String(), "hash") {
		t.Fatal("userinfo leaked credentials")
	}
	chat, err := s.Start(context.Background(), p, "10", "amount", 0)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chat.ID, "question": "amount"})
	r = request("POST", "/api/v1/chat/question", string(body), token)
	if r.Code != 200 || r.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(r.Body.String(), `"type":"finish"`) || strings.Contains(r.Body.String(), "fixture-agent") {
		t.Fatalf("stream: %d %s", r.Code, r.Body.String())
	}
	r = request(http.MethodPost, "/api/v1/login/logout", "{}", token)
	if r.Code != 200 {
		t.Fatal("logout failed")
	}
	if r = request("GET", "/api/v1/chat/list", "", token); r.Code != 401 {
		t.Fatal("revoked session accepted")
	}
}
