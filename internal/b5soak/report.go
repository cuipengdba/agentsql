//go:build agentsql_b5_soak

// Package b5soak implements the flag-independent S10 reliability soak driver.
// It is intentionally not wired into the AgentSQL server or any product flag.
package b5soak

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	GateEnvironment        = "AGENTSQL_B5_SOAK"
	ReportSchema           = "agentsql.b5.s10-soak-report.v1"
	OfficialMinDuration    = 24 * time.Hour
	OfficialMinTerminals   = uint64(10_000)
	DefaultInventoryStop   = 30 * time.Second
	DefaultInventoryPeriod = 5 * time.Second
	DefaultQuarantineStop  = 30 * time.Second
	DefaultQuarantineOff   = 5 * time.Minute
	RecoveryStablePeriod   = 30 * time.Second
)

type Config struct {
	DSN               string
	DatasourceID      string
	Duration          time.Duration
	Terminals         uint64
	Rate              float64
	HardBudget        int64
	InventoryPeriod   time.Duration
	InventoryStop     time.Duration
	ReportPeriod      time.Duration
	OutputPath        string
	WALPath           string
	RecoveryApprovers []RecoveryApproval
}

func (config Config) Validate() error {
	if os.Getenv(GateEnvironment) != "1" {
		return fmt.Errorf("b5soak: %s=1 is required", GateEnvironment)
	}
	if config.DSN == "" || config.DatasourceID == "" || config.OutputPath == "" || config.WALPath == "" {
		return errors.New("b5soak: dsn, datasource, output, and wal paths are required")
	}
	if config.Duration <= 0 || config.Terminals == 0 || config.Rate <= 0 || config.Rate > 1_000 {
		return errors.New("b5soak: duration, terminals, and a rate in (0,1000] are required")
	}
	if config.HardBudget <= 0 || config.InventoryPeriod <= 0 || config.InventoryStop <= 0 || config.ReportPeriod <= 0 {
		return errors.New("b5soak: budget and observation periods must be positive")
	}
	if config.InventoryPeriod >= config.InventoryStop {
		return errors.New("b5soak: inventory period must be below its stop line")
	}
	if filepath.Clean(config.OutputPath) == filepath.Clean(config.WALPath) {
		return errors.New("b5soak: report and WAL paths must differ")
	}
	return nil
}

type RecoveryApproval struct {
	Subject string `json:"subject"`
	Role    string `json:"role"`
}

type RecoveryDecision struct {
	Requested bool   `json:"requested"`
	Eligible  bool   `json:"eligible"`
	Reason    string `json:"reason"`
}

// EvaluateRecovery enforces the S10 two-person rule without changing any
// runtime or product flag. Runtime/SRE and QA/Release must be represented by
// distinct subjects; this driver only records eligibility for a human action.
func EvaluateRecovery(approvals []RecoveryApproval, claimDrift int64, quarantineCount int64, inventoryHealthy bool) RecoveryDecision {
	if len(approvals) == 0 {
		return RecoveryDecision{Reason: "not requested"}
	}
	decision := RecoveryDecision{Requested: true}
	if claimDrift != 0 || quarantineCount != 0 || !inventoryHealthy {
		decision.Reason = "reconciliation preconditions are not clean"
		return decision
	}
	roles := make(map[string]string)
	for _, approval := range approvals {
		if approval.Subject == "" || (approval.Role != "runtime-sre" && approval.Role != "qa-release") {
			decision.Reason = "approvals require runtime-sre and qa-release roles"
			return decision
		}
		if prior, exists := roles[approval.Role]; exists && prior != approval.Subject {
			decision.Reason = "multiple subjects for one role do not replace the other role"
			return decision
		}
		roles[approval.Role] = approval.Subject
	}
	if roles["runtime-sre"] == "" || roles["qa-release"] == "" || roles["runtime-sre"] == roles["qa-release"] {
		decision.Reason = "two distinct subjects in the required roles are mandatory"
		return decision
	}
	decision.Eligible = true
	decision.Reason = "eligible for manual recovery; no flag was changed"
	return decision
}

type Thresholds struct {
	RequestedDurationSeconds int64  `json:"requested_duration_seconds"`
	RequestedTerminals       uint64 `json:"requested_terminals"`
	OfficialDurationSeconds  int64  `json:"official_duration_seconds"`
	OfficialTerminals        uint64 `json:"official_terminals"`
	TimeExemptionAllowed     bool   `json:"time_exemption_allowed"`
}

type InventoryStats struct {
	Checks                uint64    `json:"checks"`
	Failures              uint64    `json:"failures"`
	Reconnects            uint64    `json:"reconnects"`
	LastSuccess           time.Time `json:"last_success,omitempty"`
	MaxUnavailableSeconds float64   `json:"max_unavailable_seconds"`
	StopLineSeconds       float64   `json:"stop_line_seconds"`
	Healthy               bool      `json:"healthy"`
	AdmissionPaused       bool      `json:"admission_paused"`
	StopLineBreached      bool      `json:"stop_line_breached"`
	RecoveryRequired      bool      `json:"recovery_required"`
	ConsecutiveSuccesses  uint64    `json:"consecutive_successes"`
	RecoveryStableSeconds float64   `json:"recovery_stable_seconds"`
	RecoveryEligible      bool      `json:"recovery_eligible"`
}

type QuarantineStats struct {
	Count                     int64   `json:"count"`
	OldestAgeSeconds          float64 `json:"oldest_age_seconds"`
	ChargedSlots              int64   `json:"charged_slots"`
	HardBudget                int64   `json:"hard_budget"`
	BudgetRatio               float64 `json:"budget_ratio"`
	StopCountThreshold        int64   `json:"stop_count_threshold"`
	StopAgeSeconds            float64 `json:"stop_age_seconds"`
	DisableRequiredAgeSeconds float64 `json:"disable_required_age_seconds"`
	BudgetBreached            bool    `json:"budget_breached"`
	AgeStopBreached           bool    `json:"age_stop_breached"`
	DisableRequired           bool    `json:"disable_required"`
}

type WALStats struct {
	Records         uint64 `json:"records"`
	TerminalRecords uint64 `json:"terminal_records"`
	Continuous      bool   `json:"continuous"`
	Sealed          bool   `json:"sealed"`
	Verified        bool   `json:"verified"`
	FinalDigest     string `json:"final_digest,omitempty"`
	Path            string `json:"path"`
}

type ReportChecks struct {
	RequestedDurationMet      bool `json:"requested_duration_met"`
	RequestedTerminalsMet     bool `json:"requested_terminals_met"`
	NormalUnknownZero         bool `json:"normal_unknown_zero"`
	EvidenceContradictionZero bool `json:"evidence_contradiction_zero"`
	DiscardUnconfirmedZero    bool `json:"discard_unconfirmed_zero"`
	QuarantineCountZero       bool `json:"quarantine_count_zero"`
	QuarantineWithinBudget    bool `json:"quarantine_within_budget"`
	QuarantineWithinAge       bool `json:"quarantine_within_age"`
	InventoryHealthy          bool `json:"inventory_healthy"`
	InventoryWithinStopLine   bool `json:"inventory_within_stop_line"`
	ClaimDriftZero            bool `json:"claim_drift_zero"`
	WALContinuous             bool `json:"wal_continuous"`
	WALVerified               bool `json:"wal_verified"`
	WALTerminalCountMatches   bool `json:"wal_terminal_count_matches"`
	ErrorsZero                bool `json:"errors_zero"`
}

type Report struct {
	Schema                 string            `json:"schema"`
	RunID                  string            `json:"run_id"`
	DatasourceID           string            `json:"datasource_id"`
	ServerVersion          string            `json:"server_version,omitempty"`
	TLS                    bool              `json:"tls"`
	StartedAt              time.Time         `json:"started_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
	FinishedAt             *time.Time        `json:"finished_at,omitempty"`
	ElapsedSeconds         float64           `json:"elapsed_seconds"`
	Thresholds             Thresholds        `json:"thresholds"`
	Terminals              uint64            `json:"terminals"`
	Committed              uint64            `json:"committed"`
	NormalUnknown          uint64            `json:"normal_unknown"`
	EvidenceContradiction  uint64            `json:"evidence_contradiction"`
	DiscardUnconfirmed     uint64            `json:"discard_unconfirmed"`
	ClaimDrift             int64             `json:"claim_drift"`
	MaxAbsClaimDrift       int64             `json:"max_abs_claim_drift"`
	Quarantine             QuarantineStats   `json:"quarantine"`
	Inventory              InventoryStats    `json:"inventory"`
	WAL                    WALStats          `json:"wal"`
	Errors                 map[string]uint64 `json:"errors"`
	Checks                 ReportChecks      `json:"checks"`
	Stopped                bool              `json:"stopped"`
	StopReason             string            `json:"stop_reason,omitempty"`
	Status                 string            `json:"status"`
	RunPass                bool              `json:"run_pass"`
	OfficialS10Qualified   bool              `json:"official_s10_qualified"`
	Recovery               RecoveryDecision  `json:"recovery"`
	quarantineSince        time.Time
	inventoryRecoverySince time.Time
}

type reportState struct {
	mu     sync.Mutex
	report Report
}

func (state *reportState) update(fn func(*Report)) {
	state.mu.Lock()
	fn(&state.report)
	state.mu.Unlock()
}

func (state *reportState) snapshot() Report {
	state.mu.Lock()
	defer state.mu.Unlock()
	copyOfReport := state.report
	copyOfReport.Errors = make(map[string]uint64, len(state.report.Errors))
	for class, count := range state.report.Errors {
		copyOfReport.Errors[class] = count
	}
	return copyOfReport
}

func writeReport(path string, report Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	classes := make([]string, 0, len(report.Errors))
	for class := range report.Errors {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	ordered := make(map[string]uint64, len(classes))
	for _, class := range classes {
		ordered[class] = report.Errors[class]
	}
	report.Errors = ordered
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
