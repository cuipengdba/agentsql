package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestT25MetricsEndpointObservesRealHTTPActions(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(2), zerolog.Nop())
	require.NoError(t, err)

	baseline := scrapeMetrics(t, handler)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	afterUnauthorized := scrapeMetrics(t, handler)
	require.Greater(t,
		metricValue(afterUnauthorized, "agentsql_http_requests_total", map[string]string{
			"method": "GET", "route": "/mcp", "status": "401",
		}),
		metricValue(baseline, "agentsql_http_requests_total", map[string]string{
			"method": "GET", "route": "/mcp", "status": "401",
		}),
	)

	allowBefore := metricValue(afterUnauthorized, "agentsql_decisions_total", map[string]string{
		"decision": "allow", "dialect": "postgres", "stmt_type": "SELECT",
	})
	allow := httptest.NewRecorder()
	handler.ServeHTTP(allow, authorizedRequest(
		http.MethodPost,
		queryCall(`"metrics-allow"`, "ds-allowed"),
		fixture.handlers.apiKey,
	))
	require.Equal(t, http.StatusOK, allow.Code, allow.Body.String())
	require.Contains(t, allow.Body.String(), `"decision":"allow"`)
	afterAllow := scrapeMetrics(t, handler)
	require.Greater(t,
		metricValue(afterAllow, "agentsql_decisions_total", map[string]string{
			"decision": "allow", "dialect": "postgres", "stmt_type": "SELECT",
		}),
		allowBefore,
	)

	denyBefore := metricValue(afterAllow, "agentsql_decisions_total", map[string]string{
		"decision": "deny", "dialect": "postgres", "stmt_type": "UPDATE",
	})
	ruleBefore := metricValue(afterAllow, "agentsql_rule_hits_total", map[string]string{
		"rule_id": "R002", "decision": "deny",
	})
	httpOKBefore := metricValue(afterAllow, "agentsql_http_requests_total", map[string]string{
		"method": "POST", "route": "/mcp", "status": "200",
	})
	deny := httptest.NewRecorder()
	handler.ServeHTTP(deny, authorizedRequest(
		http.MethodPost,
		`{"jsonrpc":"2.0","id":"metrics-deny","method":"tools/call","params":{"name":"execute_write","arguments":{"datasource_id":"ds-allowed","sql":"UPDATE public.customers SET phone='blocked'","reason":"metrics deny"}}}`,
		fixture.handlers.apiKey,
	))
	require.Equal(t, http.StatusOK, deny.Code, deny.Body.String())
	require.Contains(t, deny.Body.String(), `"decision":"deny"`)
	afterDeny := scrapeMetrics(t, handler)
	require.Greater(t,
		metricValue(afterDeny, "agentsql_decisions_total", map[string]string{
			"decision": "deny", "dialect": "postgres", "stmt_type": "UPDATE",
		}),
		denyBefore,
	)
	require.Greater(t,
		metricValue(afterDeny, "agentsql_rule_hits_total", map[string]string{
			"rule_id": "R002", "decision": "deny",
		}),
		ruleBefore,
	)
	require.Greater(t,
		metricValue(afterDeny, "agentsql_http_requests_total", map[string]string{
			"method": "POST", "route": "/mcp", "status": "200",
		}),
		httpOKBefore,
	)

	rejectedBefore := metricValue(afterDeny, "agentsql_rejected_total", map[string]string{
		"reason": "rate_limited",
	})
	status429Before := metricValue(afterDeny, "agentsql_http_requests_total", map[string]string{
		"method": "POST", "route": "/mcp", "status": "429",
	})
	limited := false
	for attempt := 0; attempt < 100; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authorizedRequest(http.MethodPost, listToolsRequest, fixture.handlers.apiKey))
		if response.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	require.True(t, limited, "authenticated burst must exhaust the per-Agent token bucket")
	afterRateLimit := scrapeMetrics(t, handler)
	require.Greater(t,
		metricValue(afterRateLimit, "agentsql_rejected_total", map[string]string{"reason": "rate_limited"}),
		rejectedBefore,
	)
	require.Greater(t,
		metricValue(afterRateLimit, "agentsql_http_requests_total", map[string]string{
			"method": "POST", "route": "/mcp", "status": "429",
		}),
		status429Before,
	)

	for _, name := range []string{
		"agentsql_http_requests_total",
		"agentsql_http_request_duration_seconds",
		"agentsql_decisions_total",
		"agentsql_rule_hits_total",
		"agentsql_pipeline_stage_duration_seconds",
		"agentsql_rejected_total",
		"agentsql_pool_connections",
	} {
		require.Contains(t, afterRateLimit, "# HELP "+name+" ", name)
	}
	require.Equal(t, float64(5), metricValue(afterRateLimit, "agentsql_pool_connections", map[string]string{
		"datasource": "ds-allowed", "dialect": "postgres", "state": "max",
	}))
	assertMetricLabelBoundaries(t, afterRateLimit, fixture.handlers.apiKey)
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

type metricSample struct {
	labels map[string]string
	value  float64
}

func scrapeMetrics(t *testing.T, handler http.Handler) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	return recorder.Body.String()
}

func metricValue(exposition, name string, wanted map[string]string) float64 {
	var total float64
	for _, sample := range metricSamples(exposition, name) {
		matches := true
		for label, value := range wanted {
			if sample.labels[label] != value {
				matches = false
				break
			}
		}
		if matches {
			total += sample.value
		}
	}
	return total
}

func metricSamples(exposition, name string) []metricSample {
	var samples []metricSample
	for _, line := range strings.Split(exposition, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seriesEnd := strings.IndexAny(line, "{ ")
		if seriesEnd < 0 || line[:seriesEnd] != name {
			continue
		}
		labels := make(map[string]string)
		valueText := strings.TrimSpace(line[seriesEnd:])
		if strings.HasPrefix(valueText, "{") {
			closeIndex := strings.Index(valueText, "}")
			if closeIndex < 0 {
				continue
			}
			for _, pair := range strings.Split(valueText[1:closeIndex], ",") {
				parts := strings.SplitN(pair, "=", 2)
				if len(parts) != 2 {
					continue
				}
				value, err := strconv.Unquote(parts[1])
				if err != nil {
					continue
				}
				labels[parts[0]] = value
			}
			valueText = strings.TrimSpace(valueText[closeIndex+1:])
		}
		fields := strings.Fields(valueText)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseFloat(fields[0], 64)
		if err == nil {
			samples = append(samples, metricSample{labels: labels, value: value})
		}
	}
	return samples
}

func assertMetricLabelBoundaries(t *testing.T, exposition, apiKey string) {
	t.Helper()
	for _, sample := range metricSamples(exposition, "agentsql_http_requests_total") {
		require.Contains(t, []string{"GET", "POST"}, sample.labels["method"])
		require.Contains(t, []string{"/mcp", "/metrics"}, sample.labels["route"])
		require.Contains(t, []string{"200", "401", "429"}, sample.labels["status"])
		require.Len(t, sample.labels, 3)
	}
	require.NotContains(t, exposition, apiKey)
	require.NotContains(t, exposition, "metrics-deny")
	require.NotContains(t, exposition, "UPDATE public.customers")
}
