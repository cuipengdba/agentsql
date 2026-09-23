package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/redaction"
)

type redactionRegistryReader interface {
	List(context.Context) ([]model.RedactionKeyVersion, error)
}

type redactionRuntime struct {
	mu       sync.RWMutex
	observed redaction.Observed
	result   redaction.Result
	reader   redactionRegistryReader
	metrics  *metrics.Metrics

	cancel context.CancelFunc
	wait   sync.WaitGroup
}

func newRedactionRuntime(ctx context.Context, observed redaction.Observed, reader redactionRegistryReader) (*redactionRuntime, error) {
	if observed.Status == "" {
		observed.Status = "unavailable"
	}
	state := &redactionRuntime{observed: redaction.CloneObserved(observed), reader: reader}
	if observed.Status != "available" {
		state.result = redaction.Result{
			Ready: true, Observed: redaction.CloneObserved(observed),
			Warnings: make([]redaction.Detail, 0), Information: make([]redaction.Detail, 0),
		}
		return state, nil
	}
	if ctx == nil || reader == nil {
		return nil, fmt.Errorf("registry is unavailable during startup reconciliation")
	}
	registered, err := reader.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry read failed during startup reconciliation: %w", err)
	}
	result, err := redaction.Reconcile(redaction.CloneObserved(observed), registered)
	if err != nil {
		return nil, err
	}
	state.result = result
	return state, nil
}

func (state *redactionRuntime) snapshot() (redaction.Result, bool) {
	if state == nil {
		return redaction.Result{}, false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return redaction.CloneResult(state.result), true
}

func (state *redactionRuntime) runStrong(ctx context.Context) error {
	if state == nil || state.reader == nil || state.observed.Status != "available" {
		return nil
	}
	registered, err := state.reader.List(ctx)
	if err != nil {
		failure := redaction.Result{
			Ready: false, Observed: redaction.CloneObserved(state.observed),
			Warnings:    []redaction.Detail{{Number: 1, Kind: "registry_unavailable", Message: "redaction key registry read failed"}},
			Information: make([]redaction.Detail, 0),
		}
		state.replace(failure)
		state.record("registry_unavailable")
		return fmt.Errorf("registry read failed during strong reconciliation: %w", err)
	}
	result, err := redaction.Reconcile(redaction.CloneObserved(state.observed), registered)
	if err != nil {
		state.replace(hardFailureResult(state.observed, err))
		state.record(hardFailureKind(err))
		return err
	}
	state.replace(result)
	state.recordResult(result)
	return nil
}

func (state *redactionRuntime) runPeriodic(ctx context.Context) error {
	if state == nil || state.reader == nil || state.observed.Status != "available" {
		return nil
	}
	registered, err := state.reader.List(ctx)
	if err != nil {
		state.record("registry_unavailable")
		slog.Warn("periodic redaction key reconciliation could not read registry", "kind", "registry_unavailable")
		return fmt.Errorf("periodic redaction registry read failed: %w", err)
	}
	result, err := redaction.Reconcile(redaction.CloneObserved(state.observed), registered)
	if err != nil {
		state.replace(hardFailureResult(state.observed, err))
		kind := hardFailureKind(err)
		state.record(kind)
		slog.Warn("periodic redaction key reconciliation found a hard drift", "kind", kind)
		return err
	}
	state.replace(result)
	state.recordResult(result)
	for _, detail := range result.Warnings {
		slog.Warn("periodic redaction key reconciliation warning", "kind", detail.Kind, "version", detail.Version)
	}
	for _, detail := range result.Information {
		slog.Info("periodic redaction key reconciliation information", "kind", detail.Kind, "version", detail.Version)
	}
	return nil
}

func (state *redactionRuntime) replace(result redaction.Result) {
	state.mu.Lock()
	state.result = redaction.CloneResult(result)
	state.mu.Unlock()
}

func (state *redactionRuntime) attachMetrics(hub *metrics.Metrics) {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.metrics = hub
	result := redaction.CloneResult(state.result)
	state.mu.Unlock()
	state.recordResult(result)
	for _, detail := range result.Warnings {
		slog.Warn("startup redaction key reconciliation warning", "kind", detail.Kind, "version", detail.Version)
	}
	for _, detail := range result.Information {
		slog.Info("startup redaction key reconciliation information", "kind", detail.Kind, "version", detail.Version)
	}
}

func (state *redactionRuntime) recordResult(result redaction.Result) {
	for _, detail := range result.Warnings {
		state.record(detail.Kind)
	}
	for _, detail := range result.Information {
		state.record(detail.Kind)
	}
}

func (state *redactionRuntime) record(kind string) {
	state.mu.RLock()
	hub := state.metrics
	state.mu.RUnlock()
	if hub != nil {
		hub.IncRedactionKeyDrift(kind)
	}
}

func (state *redactionRuntime) start(parent context.Context, interval time.Duration) {
	if state == nil || state.reader == nil || state.observed.Status != "available" || interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	state.mu.Lock()
	state.cancel = cancel
	state.mu.Unlock()
	state.wait.Add(1)
	go func() {
		defer state.wait.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				round, cancelRound := context.WithTimeout(ctx, 10*time.Second)
				_ = state.runPeriodic(round)
				cancelRound()
			}
		}
	}()
}

func (state *redactionRuntime) close() {
	if state == nil {
		return
	}
	state.mu.Lock()
	cancel := state.cancel
	state.cancel = nil
	state.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	state.wait.Wait()
}

func hardFailureKind(err error) string {
	switch {
	case errors.Is(err, redaction.ErrActiveCommitmentMismatch):
		return "active_commitment_mismatch"
	case errors.Is(err, redaction.ErrRetiredKeyIDReuse):
		return "retired_id_reuse"
	default:
		return "registry_integrity"
	}
}

func hardFailureResult(observed redaction.Observed, err error) redaction.Result {
	kind := hardFailureKind(err)
	number := 2
	if kind != "active_commitment_mismatch" {
		number = 0
	}
	return redaction.Result{
		Ready: false, Observed: redaction.CloneObserved(observed),
		Warnings:    []redaction.Detail{{Number: number, Kind: kind, Version: observed.ActiveVersion, Message: "redaction key registry safety check failed"}},
		Information: make([]redaction.Detail, 0),
	}
}
