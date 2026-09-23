package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetricsObserveAndGather(t *testing.T) {
	hub := New(func() []PoolStat {
		return []PoolStat{{DatasourceID: "ds-1", Dialect: "postgres", MaxOpen: 5, InUse: 2, Idle: 3}}
	})
	hub.ObserveHTTP("GET", "/healthz", "200", 0.004)
	hub.ObserveDecision("deny", "postgres", "UPDATE")
	hub.ObserveRuleHit("R002", "deny", "1")
	hub.ObserveStage("guard_static", 7)
	hub.IncRejected("rate_limited")
	hub.IncRedactionKeyDrift("active_not_registered")
	hub.RecordNotificationSent("hook", time.Unix(123, 0))
	hub.RecordNotificationFailed("hook", "timeout")
	hub.RecordNotificationDropped("hook")
	hub.SetAuditWriterReady("management", true)
	hub.SetAuditChainValid("management", true)
	hub.SetAuditChainLastVerified("management", time.Unix(456, 0))
	hub.SetAuditChainVerificationLag("management", true)

	require.Equal(t, float64(1), testutil.ToFloat64(hub.httpRequests.WithLabelValues("GET", "/healthz", "200")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.decisions.WithLabelValues("deny", "postgres", "UPDATE")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.ruleHits.WithLabelValues("R002", "deny", "1")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.rejected.WithLabelValues("rate_limited")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.redactionKeyDrift.WithLabelValues("active_not_registered")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.notificationSent.WithLabelValues("hook")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.notificationFailed.WithLabelValues("hook", "timeout")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.notificationDropped.WithLabelValues("hook")))
	require.Equal(t, float64(123), testutil.ToFloat64(hub.notificationLastSuccess.WithLabelValues("hook")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.notificationLastError.WithLabelValues("hook", "timeout")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.auditWriterReady.WithLabelValues("management")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.auditChainValid.WithLabelValues("management")))
	require.Equal(t, float64(456), testutil.ToFloat64(hub.auditChainLastVerified.WithLabelValues("management")))
	require.Equal(t, float64(1), testutil.ToFloat64(hub.auditChainVerificationLag.WithLabelValues("management")))

	families, err := hub.registry.Gather()
	require.NoError(t, err)
	names := make(map[string]struct{}, len(families))
	histograms := make(map[string]struct {
		count uint64
		sum   float64
	})
	for _, family := range families {
		names[family.GetName()] = struct{}{}
		if len(family.Metric) == 1 && family.Metric[0].Histogram != nil {
			histograms[family.GetName()] = struct {
				count uint64
				sum   float64
			}{
				count: family.Metric[0].Histogram.GetSampleCount(),
				sum:   family.Metric[0].Histogram.GetSampleSum(),
			}
		}
	}
	for _, name := range []string{
		"agentsql_http_requests_total",
		"agentsql_http_request_duration_seconds",
		"agentsql_decisions_total",
		"agentsql_rule_hits_total",
		"agentsql_pipeline_stage_duration_seconds",
		"agentsql_rejected_total",
		"agentsql_pool_connections",
		"agentsql_audit_writer_ready",
		"agentsql_audit_chain_valid",
		"agentsql_audit_chain_last_verified_timestamp_seconds",
		"agentsql_audit_chain_verification_lag",
		"redaction_key_drift",
		"agentsql_notification_sent_total",
		"agentsql_notification_failed_total",
		"agentsql_notification_dropped_total",
		"agentsql_notification_last_error_info",
		"agentsql_notification_last_success_timestamp_seconds",
	} {
		_, exists := names[name]
		require.True(t, exists, name)
	}
	require.Equal(t, uint64(1), histograms["agentsql_http_request_duration_seconds"].count)
	require.InDelta(t, 0.004, histograms["agentsql_http_request_duration_seconds"].sum, 0.0000001)
	require.Equal(t, uint64(1), histograms["agentsql_pipeline_stage_duration_seconds"].count)
	require.InDelta(t, 0.007, histograms["agentsql_pipeline_stage_duration_seconds"].sum, 0.0000001)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	hub.Handler().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	body, err := io.ReadAll(recorder.Result().Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "agentsql_http_requests_total")
}

func TestMetricsBucketsMatchSpecification(t *testing.T) {
	require.Equal(t, []float64{
		0.001, 0.0025, 0.005, 0.01, 0.025, 0.05,
		0.1, 0.25, 0.5, 1, 2.5, 5,
	}, httpDurationBuckets)
	require.Equal(t, []float64{
		0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025,
		0.05, 0.1, 0.25, 0.5, 1, 2,
	}, stageDurationBuckets)
}

func TestPoolCollectorEmitsAllStates(t *testing.T) {
	hub := New(func() []PoolStat {
		return []PoolStat{
			{DatasourceID: "ds-pg", Dialect: "postgres", MaxOpen: 5, InUse: 2, Idle: 3},
			{DatasourceID: "ds-my", Dialect: "mysql", MaxOpen: 9, InUse: 4, Idle: 5},
		}
	})
	require.Equal(t, map[string]float64{
		"ds-my/mysql/idle": 5, "ds-my/mysql/inuse": 4, "ds-my/mysql/max": 9,
		"ds-pg/postgres/idle": 3, "ds-pg/postgres/inuse": 2, "ds-pg/postgres/max": 5,
	}, gatherPoolValues(t, hub))
}

func TestMetricsNilReceiverAndSnapshotPanicAreSafe(t *testing.T) {
	var hub *Metrics
	require.NotPanics(t, func() {
		hub.ObserveHTTP("GET", "/other", "500", 1)
		hub.ObserveDecision("deny", "", "")
		hub.ObserveRuleHit("R001", "deny", "1")
		hub.ObserveStage("audit", 1)
		hub.IncRejected("rate_limited")
		hub.IncRedactionKeyDrift("registry_unavailable")
		hub.RecordNotificationSent("hook", time.Now())
		hub.RecordNotificationFailed("hook", "timeout")
		hub.RecordNotificationDropped("hook")
		hub.SetAuditWriterReady("management", true)
		hub.SetAuditChainValid("management", true)
		hub.SetAuditChainLastVerified("management", time.Now())
		hub.SetAuditChainVerificationLag("management", true)
		require.Nil(t, hub.Handler())
	})

	for _, test := range []struct {
		name     string
		snapshot func() []PoolStat
	}{
		{name: "nil callback"},
		{name: "nil snapshot", snapshot: func() []PoolStat { return nil }},
		{name: "panic", snapshot: func() []PoolStat { panic("snapshot unavailable") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshotHub := New(test.snapshot)
			require.NotPanics(t, func() {
				require.Empty(t, gatherPoolValues(t, snapshotHub))
			})
		})
	}

	panics := false
	panickingAfterSuccess := New(func() []PoolStat {
		if panics {
			panic("snapshot unavailable")
		}
		return []PoolStat{{DatasourceID: "stale", Dialect: "postgres", MaxOpen: 1}}
	})
	require.NotEmpty(t, gatherPoolValues(t, panickingAfterSuccess))
	panics = true
	require.NotPanics(t, func() {
		require.Empty(t, gatherPoolValues(t, panickingAfterSuccess), "panic must not leak stale gauge series")
	})
}

func TestNewUsesPrivateRegistryWithoutDuplicateCollectors(t *testing.T) {
	first := New(nil)
	second := New(nil)
	require.NotSame(t, first.registry, second.registry)
	firstFamilies, err := first.registry.Gather()
	require.NoError(t, err)
	secondFamilies, err := second.registry.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, firstFamilies)
	require.NotEmpty(t, secondFamilies)
}

func gatherPoolValues(t *testing.T, hub *Metrics) map[string]float64 {
	t.Helper()
	families, err := hub.registry.Gather()
	require.NoError(t, err)
	values := make(map[string]float64)
	for _, family := range families {
		if family.GetName() != "agentsql_pool_connections" {
			continue
		}
		for _, metric := range family.Metric {
			labels := make(map[string]string, len(metric.Label))
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			key := labels["datasource"] + "/" + labels["dialect"] + "/" + labels["state"]
			values[key] = metric.GetGauge().GetValue()
		}
	}
	return values
}
