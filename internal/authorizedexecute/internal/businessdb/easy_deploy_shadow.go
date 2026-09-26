package businessdb

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type ShadowDifferentialStatus string

const (
	ShadowConsistent ShadowDifferentialStatus = "consistent"
	ShadowDivergent  ShadowDifferentialStatus = "divergent"
	ShadowNativeOnly ShadowDifferentialStatus = "native_only"
	ShadowRejected   ShadowDifferentialStatus = "rejected"
	ShadowSkipped    ShadowDifferentialStatus = "skipped"
)

type ShadowBindFunc func(context.Context, BindRequest) (BoundProgram, error)

type ShadowDifferentialInput struct {
	DatasourceIdentity     string
	RequestDigest          string
	StatementClass         BinderStatementClass
	ClosedDisposition      ClosedRequestDisposition
	NativeCapabilityDigest string
}

type ShadowDifferentialResult struct {
	Status                ShadowDifferentialStatus `json:"status"`
	ClosedSupported       bool                     `json:"closed_supported"`
	NativeSupported       bool                     `json:"native_supported"`
	ClosedFactsDigest     string                   `json:"closed_facts_digest,omitempty"`
	NativeFactsDigest     string                   `json:"native_facts_digest,omitempty"`
	DifferingFields       []string                 `json:"differing_fields,omitempty"`
	NativeMarkedUnhealthy bool                     `json:"native_marked_unhealthy"`
	ClosedReason          string                   `json:"closed_reason,omitempty"`
	NativeReason          string                   `json:"native_reason,omitempty"`
}

type ShadowDifferentialEvent struct {
	At                     time.Time                `json:"at"`
	DatasourceIdentity     string                   `json:"datasource_identity"`
	RequestDigest          string                   `json:"request_digest"`
	StatementClass         BinderStatementClass     `json:"statement_class"`
	ClosedDisposition      ClosedRequestDisposition `json:"closed_disposition"`
	Status                 ShadowDifferentialStatus `json:"status"`
	ClosedFactsDigest      string                   `json:"closed_facts_digest,omitempty"`
	NativeFactsDigest      string                   `json:"native_facts_digest,omitempty"`
	DifferingFields        []string                 `json:"differing_fields,omitempty"`
	NativeCapabilityDigest string                   `json:"native_capability_digest,omitempty"`
	HealthTransition       bool                     `json:"health_transition"`
	ClosedReason           string                   `json:"closed_reason,omitempty"`
	NativeReason           string                   `json:"native_reason,omitempty"`
}

type ShadowDifferentialSink interface {
	RecordShadowDifferential(context.Context, ShadowDifferentialEvent) error
}

type ShadowDifferentialSinkFunc func(context.Context, ShadowDifferentialEvent) error

func (fn ShadowDifferentialSinkFunc) RecordShadowDifferential(ctx context.Context, event ShadowDifferentialEvent) error {
	return fn(ctx, event)
}

type ShadowDifferentialMetrics interface {
	ObserveShadowDifferential(ShadowDifferentialEvent)
}

type ShadowMetricsSnapshot struct {
	Total      uint64                              `json:"total"`
	ByStatus   map[ShadowDifferentialStatus]uint64 `json:"by_status"`
	Divergence uint64                              `json:"divergence"`
}

type InMemoryShadowMetrics struct {
	mu       sync.RWMutex
	total    uint64
	byStatus map[ShadowDifferentialStatus]uint64
}

func NewInMemoryShadowMetrics() *InMemoryShadowMetrics {
	return &InMemoryShadowMetrics{byStatus: make(map[ShadowDifferentialStatus]uint64)}
}

func (metrics *InMemoryShadowMetrics) ObserveShadowDifferential(event ShadowDifferentialEvent) {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.total++
	metrics.byStatus[event.Status]++
}

func (metrics *InMemoryShadowMetrics) Snapshot() ShadowMetricsSnapshot {
	if metrics == nil {
		return ShadowMetricsSnapshot{ByStatus: map[ShadowDifferentialStatus]uint64{}}
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	result := ShadowMetricsSnapshot{Total: metrics.total, ByStatus: make(map[ShadowDifferentialStatus]uint64, len(metrics.byStatus))}
	for key, value := range metrics.byStatus {
		result.ByStatus[key] = value
	}
	result.Divergence = result.ByStatus[ShadowDivergent]
	return result
}

type ShadowDifferentialRunner struct {
	health  *NativeHealthRegistry
	sink    ShadowDifferentialSink
	metrics ShadowDifferentialMetrics
	now     func() time.Time
}

func NewShadowDifferentialRunner(health *NativeHealthRegistry, sink ShadowDifferentialSink, metrics ShadowDifferentialMetrics) *ShadowDifferentialRunner {
	if health == nil {
		health = NewNativeHealthRegistry()
	}
	return &ShadowDifferentialRunner{health: health, sink: sink, metrics: metrics, now: time.Now}
}

// Run executes only bind/proof production for both modes. It never executes
// the business statement. The two binders run concurrently over the same raw
// request, and mode-private evidence is deliberately excluded from comparison.
func (runner *ShadowDifferentialRunner) Run(ctx context.Context, input ShadowDifferentialInput, request BindRequest,
	closed, native ShadowBindFunc) (ShadowDifferentialResult, error) {
	if runner == nil || runner.health == nil || runner.sink == nil || closed == nil || input.DatasourceIdentity == "" || input.RequestDigest == "" {
		return ShadowDifferentialResult{}, NewCapabilityFailure("AUTH_AUDIT_UNAVAILABLE")
	}
	if native == nil {
		result := ShadowDifferentialResult{Status: ShadowSkipped}
		return result, runner.emit(ctx, input, result, false)
	}
	type outcome struct {
		program BoundProgram
		err     error
	}
	closedCh, nativeCh := make(chan outcome, 1), make(chan outcome, 1)
	go func() { program, err := closed(ctx, request); closedCh <- outcome{program: program, err: err} }()
	go func() { program, err := native(ctx, request); nativeCh <- outcome{program: program, err: err} }()
	closedResult, nativeResult := <-closedCh, <-nativeCh
	result := ShadowDifferentialResult{ClosedSupported: closedResult.err == nil, NativeSupported: nativeResult.err == nil,
		ClosedReason: shadowFailureReason(closedResult.err), NativeReason: shadowFailureReason(nativeResult.err)}
	if closedResult.err == nil {
		result.ClosedFactsDigest, _ = closedResult.program.Facts.Digest()
	}
	if nativeResult.err == nil {
		result.NativeFactsDigest, _ = nativeResult.program.Facts.Digest()
	}

	switch {
	case closedResult.err == nil && nativeResult.err == nil:
		result.DifferingFields = SemanticFactDifferences(closedResult.program.Facts, nativeResult.program.Facts)
		if len(result.DifferingFields) == 0 {
			result.Status = ShadowConsistent
		} else {
			result.Status = ShadowDivergent
		}
	case closedResult.err != nil && nativeResult.err == nil && input.ClosedDisposition == ClosedRequestNativeRequired:
		result.Status = ShadowNativeOnly
	case closedResult.err != nil && nativeResult.err != nil && input.ClosedDisposition != ClosedRequestProven:
		result.Status = ShadowRejected
	default:
		result.Status = ShadowDivergent
		result.DifferingFields = []string{"support"}
	}
	transition := false
	if result.Status == ShadowDivergent {
		_, transition = runner.health.MarkDivergent(input.DatasourceIdentity, input.NativeCapabilityDigest)
		result.NativeMarkedUnhealthy = true
	}
	if err := runner.emit(ctx, input, result, transition); err != nil {
		return result, err
	}
	if result.Status == ShadowDivergent {
		return result, binderFailure(BinderFailureDivergence, BinderCodeDivergence)
	}
	return result, nil
}

func (runner *ShadowDifferentialRunner) emit(ctx context.Context, input ShadowDifferentialInput, result ShadowDifferentialResult, transition bool) error {
	event := ShadowDifferentialEvent{At: runner.now().UTC(), DatasourceIdentity: input.DatasourceIdentity,
		RequestDigest: input.RequestDigest, StatementClass: input.StatementClass, ClosedDisposition: input.ClosedDisposition,
		Status: result.Status, ClosedFactsDigest: result.ClosedFactsDigest, NativeFactsDigest: result.NativeFactsDigest,
		DifferingFields: append([]string(nil), result.DifferingFields...), NativeCapabilityDigest: input.NativeCapabilityDigest,
		HealthTransition: transition, ClosedReason: result.ClosedReason, NativeReason: result.NativeReason}
	if err := runner.sink.RecordShadowDifferential(ctx, event); err != nil {
		return NewCapabilityFailure("AUTH_AUDIT_UNAVAILABLE")
	}
	if runner.metrics != nil {
		runner.metrics.ObserveShadowDifferential(event)
	}
	return nil
}

func shadowFailureReason(err error) string {
	if err == nil {
		return ""
	}
	return binderAuditReason(err)
}

// SemanticFactDifferences compares every canonical common-facts field and
// returns field paths only. It never exposes object names or literal values.
func SemanticFactDifferences(left, right SemanticFacts) []string {
	a, b := normalizeSemanticFacts(left), normalizeSemanticFacts(right)
	var differences []string
	collectFactDifferences(reflect.ValueOf(a), reflect.ValueOf(b), "", &differences)
	if len(differences) > 64 {
		differences = append(differences[:64], "...")
	}
	return differences
}

func normalizeSemanticFacts(value SemanticFacts) SemanticFacts {
	value.Relations = append([]SemanticRelation(nil), value.Relations...)
	value.ColumnUses = append([]SemanticColumnUse(nil), value.ColumnUses...)
	value.WriteTargets = append([]SemanticWriteTarget(nil), value.WriteTargets...)
	value.ViewExpansions = append([]SemanticViewExpansion(nil), value.ViewExpansions...)
	value.ObjectUses = append([]SemanticObjectUse(nil), value.ObjectUses...)
	if len(value.Relations) == 0 {
		value.Relations = nil
	} else {
		sort.Slice(value.Relations, func(i, j int) bool {
			return semanticRelationKey(value.Relations[i]) < semanticRelationKey(value.Relations[j])
		})
	}
	if len(value.ColumnUses) == 0 {
		value.ColumnUses = nil
	} else {
		sort.Slice(value.ColumnUses, func(i, j int) bool {
			return semanticColumnKey(value.ColumnUses[i]) < semanticColumnKey(value.ColumnUses[j])
		})
	}
	if len(value.WriteTargets) == 0 {
		value.WriteTargets = nil
	} else {
		sort.Slice(value.WriteTargets, func(i, j int) bool {
			return semanticWriteKey(value.WriteTargets[i]) < semanticWriteKey(value.WriteTargets[j])
		})
	}
	if len(value.ViewExpansions) == 0 {
		value.ViewExpansions = nil
	} else {
		sort.Slice(value.ViewExpansions, func(i, j int) bool {
			return semanticViewKey(value.ViewExpansions[i]) < semanticViewKey(value.ViewExpansions[j])
		})
	}
	if len(value.ObjectUses) == 0 {
		value.ObjectUses = nil
	} else {
		sort.Slice(value.ObjectUses, func(i, j int) bool {
			return semanticObjectKey(value.ObjectUses[i]) < semanticObjectKey(value.ObjectUses[j])
		})
	}
	return value
}

func collectFactDifferences(left, right reflect.Value, path string, differences *[]string) {
	if len(*differences) > 64 || left.Type() != right.Type() {
		*differences = append(*differences, factPath(path))
		return
	}
	switch left.Kind() {
	case reflect.Struct:
		for index := 0; index < left.NumField(); index++ {
			name := left.Type().Field(index).Name
			collectFactDifferences(left.Field(index), right.Field(index), joinFactPath(path, name), differences)
		}
	case reflect.Slice, reflect.Array:
		if left.Len() != right.Len() {
			*differences = append(*differences, factPath(path)+".length")
			return
		}
		for index := 0; index < left.Len(); index++ {
			collectFactDifferences(left.Index(index), right.Index(index), fmt.Sprintf("%s[%d]", factPath(path), index), differences)
		}
	default:
		if !reflect.DeepEqual(left.Interface(), right.Interface()) {
			*differences = append(*differences, factPath(path))
		}
	}
}

func joinFactPath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}
func factPath(value string) string {
	if value == "" {
		return "SemanticFacts"
	}
	return value
}

type EasyDeployCorpusObservation struct {
	ID                 string                   `json:"id"`
	Tier               string                   `json:"tier"`
	Expectation        ClosedRequestDisposition `json:"expectation"`
	EligibleDirectCRUD bool                     `json:"eligible_direct_crud"`
	ClosedSupported    bool                     `json:"closed_supported"`
	NativeSupported    bool                     `json:"native_supported"`
	Divergent          bool                     `json:"divergent"`
}

type EasyDeployCorpusReport struct {
	Schema                     string         `json:"schema"`
	CorpusEntries              int            `json:"corpus_entries"`
	ByTier                     map[string]int `json:"by_tier"`
	ClosedCovered              int            `json:"closed_covered"`
	NativeCovered              int            `json:"native_covered"`
	ClosedCoveragePercent      float64        `json:"closed_coverage_percent"`
	NativeCoveragePercent      float64        `json:"native_coverage_percent"`
	Divergences                int            `json:"divergences"`
	EligibleDirectCRUD         int            `json:"eligible_direct_crud"`
	EligibleDirectCRUDRejected int            `json:"eligible_direct_crud_rejected"`
	EligibleFalseRejectPercent float64        `json:"eligible_false_reject_percent"`
}

func BuildEasyDeployCorpusReport(observations []EasyDeployCorpusObservation) EasyDeployCorpusReport {
	report := EasyDeployCorpusReport{Schema: "agentsql.easy-deploy-s6-report.v1", CorpusEntries: len(observations), ByTier: make(map[string]int)}
	for _, observation := range observations {
		report.ByTier[observation.Tier]++
		if observation.ClosedSupported {
			report.ClosedCovered++
		}
		if observation.NativeSupported {
			report.NativeCovered++
		}
		if observation.Divergent {
			report.Divergences++
		}
		if observation.EligibleDirectCRUD {
			report.EligibleDirectCRUD++
			if !observation.ClosedSupported {
				report.EligibleDirectCRUDRejected++
			}
		}
	}
	if report.CorpusEntries > 0 {
		report.ClosedCoveragePercent = percent(report.ClosedCovered, report.CorpusEntries)
		report.NativeCoveragePercent = percent(report.NativeCovered, report.CorpusEntries)
	}
	if report.EligibleDirectCRUD > 0 {
		report.EligibleFalseRejectPercent = percent(report.EligibleDirectCRUDRejected, report.EligibleDirectCRUD)
	}
	return report
}

func (report EasyDeployCorpusReport) JSON() ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func (report EasyDeployCorpusReport) Markdown() string {
	tiers := make([]string, 0, len(report.ByTier))
	for tier := range report.ByTier {
		tiers = append(tiers, tier)
	}
	sort.Strings(tiers)
	var out strings.Builder
	out.WriteString("# Easy-deploy S6 differential report\n\n")
	fmt.Fprintf(&out, "- Corpus entries: %d\n- Closed coverage: %d/%d (%.2f%%)\n- Native coverage: %d/%d (%.2f%%)\n- Divergences: %d\n- Eligible direct CRUD false rejects: %d/%d (%.2f%%)\n\n",
		report.CorpusEntries, report.ClosedCovered, report.CorpusEntries, report.ClosedCoveragePercent,
		report.NativeCovered, report.CorpusEntries, report.NativeCoveragePercent, report.Divergences,
		report.EligibleDirectCRUDRejected, report.EligibleDirectCRUD, report.EligibleFalseRejectPercent)
	out.WriteString("| Tier | Entries |\n|---|---:|\n")
	for _, tier := range tiers {
		fmt.Fprintf(&out, "| %s | %d |\n", tier, report.ByTier[tier])
	}
	return out.String()
}

func percent(numerator, denominator int) float64 {
	return float64(numerator) * 100 / float64(denominator)
}
