//go:build agentsql_b5_soak

package b5soak

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
)

type Runner struct {
	config            Config
	testAfterTerminal func(*pgRuntime, uint64)
}

func New(config Config) (*Runner, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Runner{config: config}, nil
}

func (runner *Runner) Run(parent context.Context) (Report, error) {
	if parent == nil {
		parent = context.Background()
	}
	config := runner.config
	runID, err := randomRunID()
	if err != nil {
		return Report{}, err
	}
	started := time.Now().UTC()
	stopCount := int64(math.Ceil(float64(config.HardBudget) * 0.01))
	if stopCount < 1 {
		stopCount = 1
	}
	state := &reportState{report: Report{
		Schema: ReportSchema, RunID: runID, DatasourceID: config.DatasourceID,
		StartedAt: started, UpdatedAt: started, Status: "RUNNING",
		Thresholds: Thresholds{
			RequestedDurationSeconds: int64(config.Duration / time.Second), RequestedTerminals: config.Terminals,
			OfficialDurationSeconds: int64(OfficialMinDuration / time.Second), OfficialTerminals: OfficialMinTerminals,
			TimeExemptionAllowed: false,
		},
		Quarantine: QuarantineStats{
			HardBudget: config.HardBudget, StopCountThreshold: stopCount,
			StopAgeSeconds: DefaultQuarantineStop.Seconds(), DisableRequiredAgeSeconds: DefaultQuarantineOff.Seconds(),
		},
		Inventory: InventoryStats{StopLineSeconds: config.InventoryStop.Seconds()},
		WAL:       WALStats{Continuous: true, Path: config.WALPath}, Errors: make(map[string]uint64),
	}}
	fail := func(class string, cause error) {
		state.update(func(report *Report) {
			report.Stopped = true
			if report.StopReason == "" {
				report.StopReason = class
			}
			report.Errors[class]++
			if cause != nil && report.StopReason == class {
				report.StopReason = class + ": " + sanitizeError(cause)
			}
		})
	}

	runContext, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	wal, err := newSoakWAL(config.WALPath)
	if err != nil {
		fail("wal_open", err)
		return runner.finish(state, nil, nil, started, err)
	}
	if err := wal.appendRunStart(runContext, state.snapshot()); err != nil {
		fail("wal_run_start", err)
		_ = wal.writer.Seal()
		return runner.finish(state, nil, nil, started, err)
	}
	state.update(func(report *Report) { report.WAL.Records++ })
	runtime, serverVersion, tls, err := openPGRuntime(runContext, config.DSN, runID)
	if err != nil {
		fail("postgres_open", err)
		return runner.finish(state, nil, wal, started, err)
	}
	state.update(func(report *Report) {
		report.ServerVersion = serverVersion
		report.TLS = tls
	})

	var background sync.WaitGroup
	var admissionPaused atomic.Bool
	fatal := make(chan error, 1)
	stop := func(class string, cause error) {
		fail(class, cause)
		select {
		case fatal <- fmt.Errorf("%s: %w", class, cause):
		default:
		}
		cancel(cause)
	}

	// Establish a healthy independent inventory before opening admission.
	if drift, reconnected, inventoryErr := runtime.inventory(runContext); inventoryErr != nil || drift != 0 {
		if inventoryErr == nil {
			inventoryErr = fmt.Errorf("initial claim drift=%d", drift)
		}
		stop("inventory_initial", inventoryErr)
	} else {
		state.update(func(report *Report) {
			report.Inventory.Checks++
			if reconnected {
				report.Inventory.Reconnects++
			}
			report.Inventory.LastSuccess = time.Now().UTC()
			report.Inventory.Healthy = true
			report.Inventory.ConsecutiveSuccesses = 1
			report.Inventory.RecoveryEligible = true
		})
	}

	background.Add(1)
	go func() {
		defer background.Done()
		ticker := time.NewTicker(config.InventoryPeriod)
		defer ticker.Stop()
		var unavailableSince time.Time
		for {
			select {
			case <-runContext.Done():
				return
			case observed := <-ticker.C:
				checkContext, checkCancel := context.WithTimeout(runContext, config.InventoryPeriod)
				drift, reconnected, inventoryErr := runtime.inventory(checkContext)
				checkCancel()
				if inventoryErr == nil {
					if drift != 0 {
						state.update(func(report *Report) {
							report.ClaimDrift = drift
							if absolute(drift) > report.MaxAbsClaimDrift {
								report.MaxAbsClaimDrift = absolute(drift)
							}
							report.Inventory.Checks++
						})
						stop("claim_drift", fmt.Errorf("observed drift %d", drift))
						return
					}
					gap := time.Duration(0)
					if !unavailableSince.IsZero() {
						gap = observed.Sub(unavailableSince)
					}
					unavailableSince = time.Time{}
					admissionPaused.Store(false)
					state.update(func(report *Report) {
						report.ClaimDrift = 0
						report.Inventory.Checks++
						if reconnected {
							report.Inventory.Reconnects++
						}
						report.Inventory.LastSuccess = observed.UTC()
						report.Inventory.Healthy = true
						report.Inventory.AdmissionPaused = false
						report.Inventory.ConsecutiveSuccesses++
						if report.Inventory.RecoveryRequired {
							if report.inventoryRecoverySince.IsZero() {
								report.inventoryRecoverySince = observed.UTC()
							}
							report.Inventory.RecoveryStableSeconds = observed.Sub(report.inventoryRecoverySince).Seconds()
							report.Inventory.RecoveryEligible = report.Inventory.ConsecutiveSuccesses >= 2 && report.Inventory.RecoveryStableSeconds >= RecoveryStablePeriod.Seconds()
						} else {
							report.Inventory.RecoveryEligible = true
						}
						if gap.Seconds() > report.Inventory.MaxUnavailableSeconds {
							report.Inventory.MaxUnavailableSeconds = gap.Seconds()
						}
					})
					continue
				}
				admissionPaused.Store(true)
				if unavailableSince.IsZero() {
					unavailableSince = state.snapshot().Inventory.LastSuccess
					if unavailableSince.IsZero() {
						unavailableSince = observed
					}
				}
				gap := observed.Sub(unavailableSince)
				state.update(func(report *Report) {
					report.Inventory.Checks++
					report.Inventory.Failures++
					report.Inventory.Healthy = false
					report.Inventory.AdmissionPaused = true
					report.Inventory.RecoveryRequired = true
					report.Inventory.ConsecutiveSuccesses = 0
					report.Inventory.RecoveryStableSeconds = 0
					report.Inventory.RecoveryEligible = false
					report.inventoryRecoverySince = time.Time{}
					if gap.Seconds() > report.Inventory.MaxUnavailableSeconds {
						report.Inventory.MaxUnavailableSeconds = gap.Seconds()
					}
				})
				if gap >= config.InventoryStop {
					state.update(func(report *Report) { report.Inventory.StopLineBreached = true })
					stop("inventory_stop_line", inventoryErr)
					return
				}
			}
		}
	}()

	background.Add(1)
	go func() {
		defer background.Done()
		ticker := time.NewTicker(config.ReportPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-runContext.Done():
				return
			case now := <-ticker.C:
				state.update(func(report *Report) {
					report.UpdatedAt = now.UTC()
					report.ElapsedSeconds = now.Sub(started).Seconds()
					if !report.quarantineSince.IsZero() {
						report.Quarantine.OldestAgeSeconds = now.Sub(report.quarantineSince).Seconds()
						report.Quarantine.AgeStopBreached = report.Quarantine.OldestAgeSeconds > report.Quarantine.StopAgeSeconds
						report.Quarantine.DisableRequired = report.Quarantine.OldestAgeSeconds > report.Quarantine.DisableRequiredAgeSeconds
					}
				})
				if reportErr := writeReport(config.OutputPath, state.snapshot()); reportErr != nil {
					stop("report_checkpoint", reportErr)
					return
				}
			}
		}
	}()

	interval := time.Duration(float64(time.Second) / config.Rate)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	nextTerminal := time.Now()
	for runContext.Err() == nil {
		snapshot := state.snapshot()
		if time.Since(started) >= config.Duration && snapshot.Terminals >= config.Terminals {
			break
		}
		if admissionPaused.Load() {
			select {
			case <-runContext.Done():
				break
			case <-time.After(25 * time.Millisecond):
				continue
			}
		}
		if wait := time.Until(nextTerminal); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-runContext.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
		if runContext.Err() != nil {
			break
		}
		nextTerminal = nextTerminal.Add(interval)
		ordinal := snapshot.Terminals + 1
		transactionID := fmt.Sprintf("%s-%012d", runID, ordinal)
		result, terminalErr := runtime.terminal(runContext, ordinal, transactionID)
		if terminalErr != nil {
			stop(classifyError(terminalErr), terminalErr)
			break
		}
		state.update(func(report *Report) {
			report.Terminals++
			if result.Resolution.Outcome == b5terminal.OutcomeCommitted {
				report.Committed++
			}
			if result.Resolution.Outcome == b5terminal.OutcomeUnknown {
				report.NormalUnknown++
			}
			if result.Resolution.Consistency.Verdict == b5terminal.VerdictContradiction {
				report.EvidenceContradiction++
			}
			if result.Resolution.Disposition == b5terminal.DispositionDiscardUnconfirmed {
				if report.quarantineSince.IsZero() {
					report.quarantineSince = time.Now()
				}
				report.DiscardUnconfirmed++
				report.Quarantine.Count++
				report.Quarantine.ChargedSlots++
				report.Quarantine.BudgetRatio = float64(report.Quarantine.ChargedSlots) / float64(config.HardBudget)
				report.Quarantine.BudgetBreached = report.Quarantine.Count >= report.Quarantine.StopCountThreshold || report.Quarantine.BudgetRatio >= 0.01
			}
		})
		q := state.snapshot().Quarantine
		if q.BudgetBreached {
			stop("quarantine_budget", errors.New("S10 quarantine threshold reached"))
			break
		}
		if result.Resolution.Outcome != b5terminal.OutcomeCommitted ||
			result.Resolution.Consistency.Verdict != b5terminal.VerdictConsistent ||
			result.Resolution.Disposition != b5terminal.DispositionReleased {
			stop("normal_terminal_invariant", fmt.Errorf("outcome=%s consistency=%s disposition=%s", result.Resolution.Outcome, result.Resolution.Consistency.Verdict, result.Resolution.Disposition))
			break
		}
		if walErr := wal.appendTerminal(runContext, transactionID, ordinal, result); walErr != nil {
			state.update(func(report *Report) { report.WAL.Continuous = false })
			stop("wal_append", walErr)
			break
		}
		state.update(func(report *Report) {
			report.WAL.Records++
			report.WAL.TerminalRecords++
		})
		if runner.testAfterTerminal != nil {
			runner.testAfterTerminal(runtime, ordinal)
		}
	}

	cancel(context.Canceled)
	background.Wait()
	select {
	case err = <-fatal:
	default:
		if cause := context.Cause(runContext); cause != nil && !errors.Is(cause, context.Canceled) {
			err = cause
		}
	}
	return runner.finish(state, runtime, wal, started, err)
}

func (runner *Runner) finish(state *reportState, runtime *pgRuntime, wal *soakWAL, started time.Time, runErr error) (Report, error) {
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cleanupCancel()
	if runtime != nil {
		var truthCount uint64
		if count, reconnected, err := runtime.databaseTruth(cleanupContext); err != nil {
			state.update(func(report *Report) { report.Errors["db_truth_check"]++ })
			runErr = errors.Join(runErr, err)
		} else {
			truthCount = count
			if reconnected {
				state.update(func(report *Report) { report.Inventory.Reconnects++ })
			}
			if truthCount != state.snapshot().Committed {
				state.update(func(report *Report) { report.Errors["db_truth_mismatch"]++ })
				runErr = errors.Join(runErr, fmt.Errorf("b5soak: database truth=%d committed=%d", truthCount, state.snapshot().Committed))
			}
		}
		if drift, reconnected, err := runtime.inventory(cleanupContext); err != nil {
			state.update(func(report *Report) { report.Errors["inventory_final"]++ })
			runErr = errors.Join(runErr, err)
		} else if drift != 0 {
			state.update(func(report *Report) {
				report.ClaimDrift = drift
				report.MaxAbsClaimDrift = max(report.MaxAbsClaimDrift, absolute(drift))
				report.Errors["claim_drift_final"]++
			})
			runErr = errors.Join(runErr, fmt.Errorf("b5soak: final claim drift=%d", drift))
		} else {
			state.update(func(report *Report) {
				report.ClaimDrift = 0
				report.Inventory.Checks++
				report.Inventory.Healthy = true
				report.Inventory.AdmissionPaused = false
				report.Inventory.LastSuccess = time.Now().UTC()
				report.Inventory.ConsecutiveSuccesses++
				if report.Inventory.RecoveryRequired {
					now := report.Inventory.LastSuccess
					if report.inventoryRecoverySince.IsZero() {
						report.inventoryRecoverySince = now
					}
					report.Inventory.RecoveryStableSeconds = now.Sub(report.inventoryRecoverySince).Seconds()
					report.Inventory.RecoveryEligible = report.Inventory.ConsecutiveSuccesses >= 2 && report.Inventory.RecoveryStableSeconds >= RecoveryStablePeriod.Seconds()
				} else {
					report.Inventory.RecoveryEligible = true
				}
				if reconnected {
					report.Inventory.Reconnects++
				}
			})
		}
		if closeErr := runtime.close(cleanupContext); closeErr != nil {
			state.update(func(report *Report) { report.Errors["cleanup"]++ })
			runErr = errors.Join(runErr, closeErr)
		}
	}
	if wal != nil {
		if !wal.finished {
			if walErr := wal.appendRunFinish(cleanupContext, state.snapshot()); walErr != nil {
				state.update(func(report *Report) {
					report.WAL.Continuous = false
					report.Errors["wal_run_finish"]++
				})
				runErr = errors.Join(runErr, walErr)
			} else {
				state.update(func(report *Report) { report.WAL.Records++ })
			}
		}
		if walErr := wal.sealAndVerify(); walErr != nil {
			state.update(func(report *Report) {
				report.WAL.Continuous = false
				report.Errors["wal_verify"]++
			})
			runErr = errors.Join(runErr, walErr)
		} else {
			state.update(func(report *Report) {
				report.WAL.Records = wal.verification.Records
				report.WAL.TerminalRecords = wal.verification.TerminalRecords
				report.WAL.Sealed = true
				report.WAL.Verified = true
				report.WAL.FinalDigest = hex.EncodeToString(wal.verification.FinalDigest[:])
			})
		}
	}
	finished := time.Now().UTC()
	state.update(func(report *Report) {
		report.UpdatedAt = finished
		report.FinishedAt = &finished
		report.ElapsedSeconds = finished.Sub(started).Seconds()
		if !report.quarantineSince.IsZero() {
			report.Quarantine.OldestAgeSeconds = finished.Sub(report.quarantineSince).Seconds()
			report.Quarantine.AgeStopBreached = report.Quarantine.OldestAgeSeconds > report.Quarantine.StopAgeSeconds
			report.Quarantine.DisableRequired = report.Quarantine.OldestAgeSeconds > report.Quarantine.DisableRequiredAgeSeconds
		}
		report.Checks = ReportChecks{
			RequestedDurationMet: report.ElapsedSeconds >= runner.config.Duration.Seconds(), RequestedTerminalsMet: report.Terminals >= runner.config.Terminals,
			NormalUnknownZero: report.NormalUnknown == 0, EvidenceContradictionZero: report.EvidenceContradiction == 0,
			DiscardUnconfirmedZero: report.DiscardUnconfirmed == 0, QuarantineCountZero: report.Quarantine.Count == 0,
			QuarantineWithinBudget: !report.Quarantine.BudgetBreached, QuarantineWithinAge: !report.Quarantine.AgeStopBreached,
			InventoryHealthy: report.Inventory.Healthy, InventoryWithinStopLine: !report.Inventory.StopLineBreached && report.Inventory.MaxUnavailableSeconds <= runner.config.InventoryStop.Seconds(),
			ClaimDriftZero: report.ClaimDrift == 0 && report.MaxAbsClaimDrift == 0,
			WALContinuous:  report.WAL.Continuous, WALVerified: report.WAL.Sealed && report.WAL.Verified,
			WALTerminalCountMatches: report.WAL.TerminalRecords == report.Terminals, ErrorsZero: len(report.Errors) == 0,
		}
		checks := report.Checks
		clean := checks.NormalUnknownZero && checks.EvidenceContradictionZero && checks.DiscardUnconfirmedZero &&
			checks.QuarantineCountZero && checks.QuarantineWithinBudget && checks.QuarantineWithinAge && checks.InventoryHealthy &&
			checks.InventoryWithinStopLine && checks.ClaimDriftZero && checks.WALContinuous && checks.WALVerified &&
			checks.WALTerminalCountMatches && checks.ErrorsZero
		report.RunPass = checks.RequestedDurationMet && checks.RequestedTerminalsMet && clean && runErr == nil
		report.OfficialS10Qualified = report.RunPass && report.ElapsedSeconds >= OfficialMinDuration.Seconds() && report.Terminals >= OfficialMinTerminals
		inventoryRecoveryReady := report.Inventory.Healthy && (!report.Inventory.RecoveryRequired || report.Inventory.RecoveryEligible)
		report.Recovery = EvaluateRecovery(runner.config.RecoveryApprovers, report.ClaimDrift, report.Quarantine.Count, inventoryRecoveryReady)
		report.Status = "FAIL"
		if report.RunPass {
			report.Status = "PASS"
		}
		if !report.RunPass && report.StopReason == "" {
			report.Stopped = true
			report.StopReason = "requested thresholds or SLO invariants not met"
		}
	})
	report := state.snapshot()
	if err := writeReport(runner.config.OutputPath, report); err != nil {
		return report, errors.Join(runErr, err)
	}
	if !report.RunPass && runErr == nil {
		runErr = errors.New(report.StopReason)
	}
	return report, runErr
}

func randomRunID() (string, error) {
	var value [6]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, b5terminal.ErrPGTerminalWatchdog):
		return "watchdog"
	case errors.Is(err, b5terminal.ErrPGProtocol):
		return "protocol"
	case errors.Is(err, b5terminal.ErrPGConnectionDiscarded):
		return "connection_discarded"
	default:
		return "postgres_terminal"
	}
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func absolute(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
