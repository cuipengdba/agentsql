package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type b5ServiceSpy struct {
	mu    sync.Mutex
	calls []string
}

func (spy *b5ServiceSpy) called(name string) {
	spy.mu.Lock()
	spy.calls = append(spy.calls, name)
	spy.mu.Unlock()
}
func (spy *b5ServiceSpy) OpenSession(context.Context, string, B5OpenSessionInput) (B5SessionView, error) {
	spy.called("open")
	return B5SessionView{SessionID: "as-session", ContinuationSecret: "once", OwnerEpoch: 1, Status: b5.SessionReady}, nil
}
func (spy *b5ServiceSpy) CloseSession(context.Context, string, B5SessionInput) (B5SessionView, error) {
	spy.called("close")
	return B5SessionView{SessionID: "as-session", OwnerEpoch: 1, Status: b5.SessionTerminal}, nil
}
func (spy *b5ServiceSpy) SessionStatus(context.Context, string, B5SessionInput) (B5SessionView, error) {
	spy.called("session_status")
	return B5SessionView{SessionID: "as-session", OwnerEpoch: 1, Status: b5.SessionReady}, nil
}
func (spy *b5ServiceSpy) Begin(context.Context, string, B5BeginInput) (b5coordinator.Result, error) {
	spy.called("begin")
	return b5coordinator.Result{TransactionID: "tx", Status: b5.TransactionActive, Phase: b5.PhaseActive, Effect: b5.EffectKeepActive}, nil
}
func (spy *b5ServiceSpy) Execute(context.Context, string, B5ExecuteInput) (b5coordinator.Result, error) {
	spy.called("execute")
	return b5coordinator.Result{TransactionID: "tx", Status: b5.TransactionActive, Phase: b5.PhaseActive, Effect: b5.EffectKeepActive, NextOrdinal: 1, AffectedRows: 1}, nil
}
func (spy *b5ServiceSpy) Commit(context.Context, string, B5FinishInput) (b5coordinator.Result, error) {
	spy.called("commit")
	return b5coordinator.Result{TransactionID: "tx", Status: b5.TransactionTerminal, Phase: b5.PhaseTerminal, Effect: b5.EffectTerminalCommitted, DBOutcome: b5.OutcomeCommitted, AuditDurability: b5.DurabilityDurable, ConnectionDisposition: b5.DispositionReleased}, nil
}
func (spy *b5ServiceSpy) Rollback(context.Context, string, B5FinishInput) (b5coordinator.Result, error) {
	spy.called("rollback")
	return b5coordinator.Result{TransactionID: "tx", Status: b5.TransactionTerminal, Phase: b5.PhaseTerminal, Effect: b5.EffectTerminalNotCommitted, DBOutcome: b5.OutcomeNotCommitted, AuditDurability: b5.DurabilityDurable, ConnectionDisposition: b5.DispositionReleased}, nil
}
func (spy *b5ServiceSpy) TransactionStatus(context.Context, string, B5FinishInput) (b5coordinator.Result, error) {
	spy.called("tx_status")
	return b5coordinator.Result{TransactionID: "tx", Status: b5.TransactionActive, Phase: b5.PhaseActive, Effect: b5.EffectKeepActive}, nil
}
func (spy *b5ServiceSpy) Shutdown(context.Context) error { spy.called("shutdown"); return nil }

func TestB5ToolsAreCompletelyHiddenWhenFlagOff(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)

	listed := callMCPBody(t, handler, fixture.handlers.apiKey, listToolsRequest)
	for _, name := range b5ToolNames() {
		require.NotContains(t, listed, `"`+name+`"`)
	}
	called := callMCPBody(t, handler, fixture.handlers.apiKey, toolCall("open_session", map[string]any{}))
	require.Contains(t, called, "unknown tool")
	require.NotContains(t, called, "as-session")
}

func TestB5FlagOnToolsAndSequentialTransactionFlow(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	spy := &b5ServiceSpy{}
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop(), WithB5Sessions(B5Options{B5Sessions: true, B5TxPostgres: true, Service: spy}))
	require.NoError(t, err)

	listed := callMCPBody(t, handler, fixture.handlers.apiKey, listToolsRequest)
	for _, name := range b5ToolNames() {
		require.Equal(t, 1, strings.Count(listed, `"`+name+`"`), name)
	}
	open := callMCPBody(t, handler, fixture.handlers.apiKey, toolCall("open_session", map[string]any{}))
	require.Contains(t, open, `"decision":"allow"`)
	require.Contains(t, open, `"session_id":"as-session"`)

	continuation := map[string]any{"session_id": "as-session", "owner_epoch": 1, "request_id": "r", "continuation_proof": strings.Repeat("a", 43), "body_digest": strings.Repeat("00", 32)}
	begin := cloneMap(continuation)
	begin["transaction_id"], begin["datasource_id"], begin["dialect"], begin["server_major"] = "tx", "ds-allowed", "postgres", 18
	begin["key_revision"], begin["datasource_revision"], begin["policy_revision"] = 1, 1, 1
	begin["statements"] = []map[string]any{{"operation_id": "op-1", "sql": "UPDATE public.customers SET phone='x' WHERE id=1", "reason": "test update"}}
	beginBody := callMCPBody(t, handler, fixture.handlers.apiKey, toolCall("begin_transaction", begin))
	require.Contains(t, beginBody, `"decision":"allow"`)

	execute := cloneMap(continuation)
	execute["transaction_id"], execute["operation_id"], execute["ordinal"] = "tx", "op-1", 0
	require.Contains(t, callMCPBody(t, handler, fixture.handlers.apiKey, toolCall("execute_transaction_statement", execute)), `"next_ordinal":1`)
	finish := cloneMap(continuation)
	finish["transaction_id"] = "tx"
	committed := callMCPBody(t, handler, fixture.handlers.apiKey, toolCall("commit_transaction", finish))
	require.Contains(t, committed, `"db_outcome":"COMMITTED"`)
	require.Contains(t, committed, `"connection_disposition":"RELEASED"`)
	require.Equal(t, []string{"open", "begin", "execute", "commit"}, spy.calls)
}

func TestB5ToolLayerRejectsClosedSurfaceBeforeCoordinator(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	spy := &b5ServiceSpy{}
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(1000), zerolog.Nop(), WithB5Sessions(B5Options{B5Sessions: true, B5TxPostgres: true, Service: spy}))
	require.NoError(t, err)
	tests := []struct {
		name, sql, code string
	}{
		{"stacked", "UPDATE public.customers SET phone='x'; DELETE FROM public.customers", string(b5.ErrorTxControlStatementDenied)},
		{"select", "SELECT * FROM public.customers", string(b5.ErrorTxSelectUnsupported)},
		{"returning", "UPDATE public.customers SET phone='x' RETURNING id", string(b5.ErrorTxReturningUnsupported)},
		{"ddl", "CREATE TABLE forbidden(id int)", string(b5.ErrorTxControlStatementDenied)},
		{"savepoint", "SAVEPOINT forbidden", string(b5.ErrorTxControlStatementDenied)},
		{"session variable", "SET search_path=public", string(b5.ErrorTxControlStatementDenied)},
		{"prepared", "PREPARE forbidden AS SELECT 1", string(b5.ErrorTxControlStatementDenied)},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"session_id": "s", "owner_epoch": 1, "request_id": "r", "continuation_proof": strings.Repeat("a", 43), "body_digest": strings.Repeat("00", 32), "transaction_id": "tx", "datasource_id": "ds-allowed", "dialect": "postgres", "server_major": 18, "key_revision": 1, "datasource_revision": 1, "policy_revision": 1, "statements": []map[string]any{{"operation_id": "op", "sql": test.sql, "reason": "negative test"}}}
			body := callMCPBody(t, handler, fixture.handlers.apiKey, toolCall("begin_transaction", input))
			require.Contains(t, body, `"decision":"error"`)
			require.Contains(t, body, test.code)
			require.NotContains(t, spy.calls, "begin", "case %d reached coordinator", index)
		})
	}
	t.Run("mysql unsupported", func(t *testing.T) {
		mysql := map[string]any{"session_id": "s", "owner_epoch": 1, "request_id": "r", "continuation_proof": strings.Repeat("a", 43), "body_digest": strings.Repeat("00", 32), "transaction_id": "tx", "datasource_id": "ds-allowed", "dialect": "mysql", "server_major": 8, "key_revision": 1, "datasource_revision": 1, "policy_revision": 1, "statements": []map[string]any{{"operation_id": "op", "sql": "UPDATE customers SET phone='x'", "reason": "mysql unsupported"}}}
		body := callMCPBody(t, handler, fixture.handlers.apiKey, toolCall("begin_transaction", mysql))
		require.Contains(t, body, string(b5.ErrorDialectTransactionUnsupported))
		require.Contains(t, body, "MySQL")
		require.Contains(t, body, "不支持")
		require.Contains(t, body, "未取得可写连接")
		require.NotContains(t, spy.calls, "begin", "MySQL unsupported path reached coordinator")
	})
}

func TestMCPProtocolAllowlistRejectsBeforeSDKWithBusinessEnvelope(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	for _, version := range []string{"2025-11-25", "2026-07-28", "1999-01-01", ""} {
		recorder := httptest.NewRecorder()
		request := authorizedRequest(http.MethodPost, initializeRequest, fixture.handlers.apiKey)
		request.Header.Set("MCP-Protocol-Version", version)
		handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), `"decision":"error"`)
		require.Contains(t, recorder.Body.String(), string(b5.ErrorMCPProtocolUnsupported))
	}
	conflict := strings.Replace(initializeRequest, `"protocolVersion":"2025-06-18"`, `"protocolVersion":"2025-11-25"`, 1)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, conflict, fixture.handlers.apiKey))
	require.Contains(t, recorder.Body.String(), string(b5.ErrorMCPProtocolUnsupported))
	metaConflict := strings.Replace(initializeRequest, `"capabilities":{}`, `"_meta":{"protocolVersion":"2026-07-28"},"capabilities":{}`, 1)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, metaConflict, fixture.handlers.apiKey))
	require.Contains(t, recorder.Body.String(), string(b5.ErrorMCPProtocolUnsupported))
	require.Zero(t, registry.buildCount(), "rejected versions must not reach SDK server construction")
}

func TestB5OptionsFailClosedAndStdioProtocolIsExact(t *testing.T) {
	require.Error(t, (B5Options{B5TxPostgres: true}).validate())
	require.Error(t, (B5Options{B5Sessions: true}).validate())
	require.Error(t, (B5Options{B5Sessions: true, B5TxMySQL: true, Service: &b5ServiceSpy{}}).validate())
	transport := legacyProtocolTransport{}
	require.True(t, transport.SupportsProtocolVersion("2025-06-18"))
	require.False(t, transport.SupportsProtocolVersion("2025-11-25"))
	require.False(t, transport.SupportsProtocolVersion("2026-07-28"))
}

func TestB5ProductionFailureContractsAreChineseAndStable(t *testing.T) {
	for _, code := range []b5.ErrorCode{
		b5.ErrorDialectTransactionUnsupported,
		b5.ErrorPostgresVersionUnsupported,
		b5.ErrorPostgresCapabilityUnavailable,
		b5.ErrorTxPlanUnproven,
		b5.ErrorAuthImplicitObjectUnclosed,
		b5.ErrorAuthCatalogRace,
		b5.ErrorAuthDMLActionMissing,
		b5.ErrorTxDMLShapeUnsupported,
		b5.ErrorAuditEmergencyWALUnavailable,
	} {
		response := b5ErrorResponse(code, errors.New("unstable internal error"))
		require.Equal(t, string(code), response.ErrorCode)
		require.True(t, containsHan(response.Reason), code)
		require.True(t, containsHan(response.Suggestion), code)
		require.NotContains(t, response.Reason, "unstable internal error")
	}
}

type disconnectService struct {
	*b5ServiceSpy
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

func (service *disconnectService) Execute(ctx context.Context, _ string, _ B5ExecuteInput) (b5coordinator.Result, error) {
	close(service.started)
	select {
	case <-ctx.Done():
		close(service.canceled)
		return b5coordinator.Result{}, ctx.Err()
	case <-service.release:
		return b5coordinator.Result{TransactionID: "tx", Status: b5.TransactionActive, Phase: b5.PhaseActive, Effect: b5.EffectKeepActive}, nil
	}
}

func TestLegacyHTTPDisconnectDoesNotCancelToolHandler(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	service := &disconnectService{b5ServiceSpy: &b5ServiceSpy{}, started: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop(), WithB5Sessions(B5Options{B5Sessions: true, B5TxPostgres: true, Service: service}))
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()

	arguments := map[string]any{"session_id": "s", "owner_epoch": 1, "request_id": "r", "continuation_proof": strings.Repeat("a", 43), "body_digest": strings.Repeat("00", 32), "transaction_id": "tx", "operation_id": "op", "ordinal": 0}
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp", strings.NewReader(toolCall("execute_transaction_statement", arguments)))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+fixture.handlers.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", legacyMCPProtocolVersion)
	done := make(chan error, 1)
	go func() {
		response, callErr := server.Client().Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- callErr
	}()
	<-service.started
	cancel()
	select {
	case <-service.canceled:
		t.Fatal("2025-06-18 disconnect propagated cancellation into handler")
	case <-time.After(100 * time.Millisecond):
	}
	close(service.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP request did not finish")
	}
}

func TestFinalFenceFailureReturnsAxesButNoBusinessResult(t *testing.T) {
	result := b5coordinator.Result{Code: b5.ErrorFinalFencePendingCommitted, Effect: b5.EffectFenceOnly, DBOutcome: b5.OutcomeCommitted, AuditDurability: b5.DurabilityDurable, ConnectionDisposition: b5.DispositionReleased}
	response := b5ResultResponse(result, &b5coordinator.Failure{Code: result.Code, Cause: errors.New("fence unavailable")})
	require.Equal(t, "error", response.Decision)
	require.Nil(t, response.Data)
	require.Equal(t, string(b5.OutcomeCommitted), response.DBOutcome)
	require.Equal(t, string(b5.DispositionReleased), response.ConnectionDisposition)
}

func TestB5ContinuationDigestBindsOperationAndExcludesProof(t *testing.T) {
	input := B5ExecuteInput{B5Continuation: B5Continuation{SessionID: "session", OwnerEpoch: 1, RequestID: "request", ContinuationProof: "proof-a", BodyDigest: "claim-a"}, TransactionID: "tx", OperationID: "op", Ordinal: 0}
	first, err := B5BodyDigest("execute_transaction_statement", input)
	require.NoError(t, err)
	input.ContinuationProof, input.BodyDigest = "proof-b", "claim-b"
	second, err := B5BodyDigest("execute_transaction_statement", input)
	require.NoError(t, err)
	require.Equal(t, first, second, "proof and claimed digest must not self-authenticate")
	input.Ordinal = 1
	third, err := B5BodyDigest("execute_transaction_statement", input)
	require.NoError(t, err)
	require.NotEqual(t, first, third, "ordinal substitution must invalidate the proof")
	fourth, err := B5BodyDigest("commit_transaction", input)
	require.NoError(t, err)
	require.NotEqual(t, third, fourth, "method substitution must invalidate the proof")
}

func b5ToolNames() []string {
	return []string{"open_session", "close_session", "get_session_status", "begin_transaction", "execute_transaction_statement", "commit_transaction", "rollback_transaction", "get_transaction_status"}
}

func toolCall(name string, arguments map[string]any) string {
	contents, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	if err != nil {
		panic(err)
	}
	return string(contents)
}

func callMCPBody(t *testing.T, handler http.Handler, key, body string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, body, key))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	return recorder.Body.String()
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
