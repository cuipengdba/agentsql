package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var httpDurationBuckets = []float64{
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05,
	0.1, 0.25, 0.5, 1, 2.5, 5,
}

var stageDurationBuckets = []float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025,
	0.05, 0.1, 0.25, 0.5, 1, 2,
}

// PoolStat is one instantaneous datasource pool snapshot.
type PoolStat struct {
	DatasourceID string
	Dialect      string
	MaxOpen      int
	InUse        int
	Idle         int
}

// Metrics owns the process-local AgentSQL Prometheus registry.
type Metrics struct {
	registry        *prometheus.Registry
	httpRequests    *prometheus.CounterVec
	httpDuration    *prometheus.HistogramVec
	decisions       *prometheus.CounterVec
	ruleHits        *prometheus.CounterVec
	stageDuration   *prometheus.HistogramVec
	rejected        *prometheus.CounterVec
	poolConnections *prometheus.GaugeVec
}

// New creates an isolated registry and all AgentSQL collectors.
func New(poolSnapshot func() []PoolStat) *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "agentsql_http_requests_total",
			Help: "Total AgentSQL HTTP requests.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "agentsql_http_request_duration_seconds",
			Help:    "AgentSQL HTTP request duration in seconds.",
			Buckets: httpDurationBuckets,
		}, []string{"route"}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "agentsql_decisions_total",
			Help: "Total AgentSQL pipeline decisions.",
		}, []string{"decision", "dialect", "stmt_type"}),
		ruleHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "agentsql_rule_hits_total",
			Help: "Total AgentSQL rule hits.",
		}, []string{"rule_id", "decision", "risk"}),
		stageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "agentsql_pipeline_stage_duration_seconds",
			Help:    "AgentSQL pipeline stage duration in seconds.",
			Buckets: stageDurationBuckets,
		}, []string{"stage"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "agentsql_rejected_total",
			Help: "Total AgentSQL gateway rejections by reason.",
		}, []string{"reason"}),
		poolConnections: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "agentsql_pool_connections",
			Help: "Current AgentSQL datasource pool connections.",
		}, []string{"datasource", "dialect", "state"}),
	}
	metrics.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		metrics.httpRequests,
		metrics.httpDuration,
		metrics.decisions,
		metrics.ruleHits,
		metrics.stageDuration,
		metrics.rejected,
		&poolCollector{
			gauge:    metrics.poolConnections,
			snapshot: poolSnapshot,
		},
	)
	return metrics
}

// ObserveHTTP records one normalized HTTP request and its duration.
func (metrics *Metrics) ObserveHTTP(method, route, status string, seconds float64) {
	if metrics == nil {
		return
	}
	metrics.httpRequests.WithLabelValues(method, route, status).Inc()
	metrics.httpDuration.WithLabelValues(route).Observe(seconds)
}

// ObserveDecision records one final pipeline decision.
func (metrics *Metrics) ObserveDecision(decision, dialect, stmtType string) {
	if metrics == nil {
		return
	}
	metrics.decisions.WithLabelValues(decision, dialect, stmtType).Inc()
}

// ObserveRuleHit records one rule hit from a final assessment.
func (metrics *Metrics) ObserveRuleHit(ruleID, decision, risk string) {
	if metrics == nil {
		return
	}
	metrics.ruleHits.WithLabelValues(ruleID, decision, risk).Inc()
}

// ObserveStage records one final pipeline stage latency after converting ms to seconds.
func (metrics *Metrics) ObserveStage(stage string, latencyMS int64) {
	if metrics == nil {
		return
	}
	metrics.stageDuration.WithLabelValues(stage).Observe(float64(latencyMS) / 1000)
}

// IncRejected increments one bounded gateway rejection reason.
func (metrics *Metrics) IncRejected(reason string) {
	if metrics == nil {
		return
	}
	metrics.rejected.WithLabelValues(reason).Inc()
}

// Handler returns the registry's Prometheus exposition handler.
func (metrics *Metrics) Handler() http.Handler {
	if metrics == nil || metrics.registry == nil {
		return nil
	}
	return promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	})
}

type poolCollector struct {
	mu       sync.Mutex
	gauge    *prometheus.GaugeVec
	snapshot func() []PoolStat
}

func (collector *poolCollector) Describe(descriptions chan<- *prometheus.Desc) {
	if collector == nil || collector.gauge == nil {
		return
	}
	collector.gauge.Describe(descriptions)
}

func (collector *poolCollector) Collect(samples chan<- prometheus.Metric) {
	if collector == nil || collector.gauge == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.gauge.Reset()
	for _, stat := range safePoolSnapshot(collector.snapshot) {
		collector.gauge.WithLabelValues(stat.DatasourceID, stat.Dialect, "max").Set(float64(stat.MaxOpen))
		collector.gauge.WithLabelValues(stat.DatasourceID, stat.Dialect, "inuse").Set(float64(stat.InUse))
		collector.gauge.WithLabelValues(stat.DatasourceID, stat.Dialect, "idle").Set(float64(stat.Idle))
	}
	collector.gauge.Collect(samples)
}

func safePoolSnapshot(snapshot func() []PoolStat) (stats []PoolStat) {
	if snapshot == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			stats = nil
		}
	}()
	return snapshot()
}
