package mcpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestT25MetricsEndpointIsUnauthenticatedAndObservesHTTP(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	fixture.runtime.Metrics = metrics.New(nil)
	fixture.runtime.Metrics.ObserveDecision("allow", "postgres", "SELECT")
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, metricsResponse.Code)
	body, err := io.ReadAll(metricsResponse.Result().Body)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "agentsql_")
	require.Contains(t, text, "agentsql_http_requests_total")
	require.Contains(t, text, `method="GET"`)
	require.Contains(t, text, `route="/mcp"`)
	require.Contains(t, text, `status="401"`)
}

func TestT25MetricsEndpointFailsClosedWithoutHub(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	fixture.runtime.Metrics = nil
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestClassifyRouteUsesOnlyBoundedLabels(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/mcp", want: "/mcp"},
		{path: "/healthz", want: "/healthz"},
		{path: "/readyz", want: "/readyz"},
		{path: "/metrics", want: "/metrics"},
		{path: "/api/v1/agents", want: "/api/v1/agents"},
		{path: "/api/v1/agents/agent-real-customer-42", want: "/api/v1/agents/{id}"},
		{path: "/api/v1/agents/550e8400-e29b-41d4-a716-446655440000", want: "/api/v1/agents/{id}"},
		{path: "/api/v1/agents/asql_secret-key", want: "/api/v1/agents/{id}"},
		{path: "/api/v1/agents/123456", want: "/api/v1/agents/{id}"},
		{path: "/api/v1/rules/0123456789abcdef0123456789abcdef", want: "/api/v1/rules/{id}"},
		{path: "/api/v1/agents/ag-1/rotate-key", want: "/api/v1/agents/{id}/rotate-key"},
		{path: "/api/v1/datasources/ds-1/ping", want: "/api/v1/datasources/{id}/ping"},
		{path: "/api/v1/auth/login", want: "/api/v1/auth/login"},
		{path: "/api/v1/audit/export", want: "/api/v1/audit/export"},
		{path: "/api/v1/unknown/real-id", want: "/other"},
		{path: "/audit", want: "/other"},
	}
	for _, test := range tests {
		t.Run(strings.ReplaceAll(test.path, "/", "_"), func(t *testing.T) {
			require.Equal(t, test.want, classifyRoute(test.path))
			require.NotContains(t, classifyRoute(test.path), "secret-key")
			require.NotContains(t, classifyRoute(test.path), "real-customer")
		})
	}
}
