//go:build agentsql_b5_soak

package b5soak

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/cuipengdba/agentsql/internal/b5wal"
)

const (
	soakWALSchema        = "agentsql.b5.s10-soak-evidence.v1"
	soakWALSchemaVersion = uint16(1)
	walKindRunStart      = "run_start"
	walKindTerminal      = "terminal"
	walKindRunFinish     = "run_finish"
)

type soakWAL struct {
	writer       *b5wal.Writer
	segment      b5wal.Segment
	path         string
	next         uint64
	previous     [32]byte
	runID        string
	datasourceID string
	terminals    uint64
	started      bool
	finished     bool
	verification walVerification
}

type walVerification struct {
	Records         uint64
	TerminalRecords uint64
	FinalDigest     [32]byte
}

type walRunSummary struct {
	Terminals             uint64 `json:"terminals"`
	Committed             uint64 `json:"committed"`
	NormalUnknown         uint64 `json:"normal_unknown"`
	EvidenceContradiction uint64 `json:"evidence_contradiction"`
	DiscardUnconfirmed    uint64 `json:"discard_unconfirmed"`
	ClaimDrift            int64  `json:"claim_drift"`
	QuarantineCount       int64  `json:"quarantine_count"`
}

type walEvent struct {
	Schema                   string         `json:"schema"`
	Kind                     string         `json:"kind"`
	RunID                    string         `json:"run_id"`
	DatasourceID             string         `json:"datasource_id"`
	Terminal                 uint64         `json:"terminal,omitempty"`
	TransactionID            string         `json:"transaction_id,omitempty"`
	DBOutcome                string         `json:"db_outcome,omitempty"`
	EvidenceConsistency      string         `json:"evidence_consistency,omitempty"`
	Disposition              string         `json:"connection_disposition,omitempty"`
	WritePhase               string         `json:"write_phase,omitempty"`
	BackendPID               uint32         `json:"backend_pid,omitempty"`
	BackendStart             string         `json:"backend_start,omitempty"`
	WatchdogFired            bool           `json:"watchdog_fired,omitempty"`
	ReleaseChecksPassed      bool           `json:"release_checks_passed,omitempty"`
	RequestedDurationSeconds int64          `json:"requested_duration_seconds,omitempty"`
	RequestedTerminals       uint64         `json:"requested_terminals,omitempty"`
	HardBudget               int64          `json:"hard_budget,omitempty"`
	Summary                  *walRunSummary `json:"summary,omitempty"`
	PreviousDigest           string         `json:"previous_digest"`
	ObservedAt               string         `json:"observed_at"`
}

func newSoakWAL(path string) (*soakWAL, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	var segment b5wal.Segment
	if _, err := io.ReadFull(rand.Reader, segment.Manifest.SegmentID[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, segment.Manifest.NonceDomain[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, segment.Key[:]); err != nil {
		return nil, err
	}
	segment.Manifest.FormatVersion = b5wal.WALFormatVersion
	segment.Manifest.OwnerGeneration = 1
	segment.Manifest.KeyID = "s10-soak/" + hex.EncodeToString(segment.Manifest.SegmentID[:])
	sink, err := newSoakExtentSink(path)
	if err != nil {
		return nil, err
	}
	writer, err := b5wal.NewWriter(segment, sink)
	if err != nil {
		_ = sink.Close()
		return nil, err
	}
	return &soakWAL{writer: writer, segment: segment, path: path}, nil
}

func (wal *soakWAL) appendRunStart(ctx context.Context, report Report) error {
	if wal.started || wal.finished || wal.next != 0 || report.RunID == "" || report.DatasourceID == "" {
		return errors.New("b5soak: invalid WAL run start")
	}
	event := walEvent{
		Schema: soakWALSchema, Kind: walKindRunStart, RunID: report.RunID, DatasourceID: report.DatasourceID,
		RequestedDurationSeconds: report.Thresholds.RequestedDurationSeconds,
		RequestedTerminals:       report.Thresholds.RequestedTerminals,
		HardBudget:               report.Quarantine.HardBudget,
	}
	if err := wal.appendEvent(ctx, event); err != nil {
		return err
	}
	wal.runID = report.RunID
	wal.datasourceID = report.DatasourceID
	wal.started = true
	return nil
}

func (wal *soakWAL) appendTerminal(ctx context.Context, transactionID string, terminal uint64, result b5terminal.PGTerminalResult) error {
	if !wal.started || wal.finished || terminal != wal.terminals+1 || transactionID == "" {
		return errors.New("b5soak: invalid WAL terminal sequence")
	}
	event := walEvent{
		Schema: soakWALSchema, Kind: walKindTerminal, RunID: wal.runID, DatasourceID: wal.datasourceID,
		Terminal: terminal, TransactionID: transactionID,
		DBOutcome: string(result.Resolution.Outcome), EvidenceConsistency: string(result.Resolution.Consistency.Verdict),
		Disposition: string(result.Resolution.Disposition), WritePhase: fmt.Sprint(result.Evidence.Write.Phase),
		BackendPID: result.Backend.PID, WatchdogFired: result.WatchdogFired,
		ReleaseChecksPassed: result.ReleaseChecks.AllPassed(),
	}
	if !result.Backend.BackendStart.IsZero() {
		event.BackendStart = result.Backend.BackendStart.UTC().Format(time.RFC3339Nano)
	}
	if err := wal.appendEvent(ctx, event); err != nil {
		return err
	}
	wal.terminals++
	return nil
}

func (wal *soakWAL) appendRunFinish(ctx context.Context, report Report) error {
	if !wal.started || wal.finished || report.RunID != wal.runID || report.DatasourceID != wal.datasourceID || report.Terminals != wal.terminals {
		return errors.New("b5soak: invalid WAL run finish")
	}
	event := walEvent{
		Schema: soakWALSchema, Kind: walKindRunFinish, RunID: wal.runID, DatasourceID: wal.datasourceID,
		Terminal: report.Terminals,
		Summary: &walRunSummary{
			Terminals: report.Terminals, Committed: report.Committed, NormalUnknown: report.NormalUnknown,
			EvidenceContradiction: report.EvidenceContradiction, DiscardUnconfirmed: report.DiscardUnconfirmed,
			ClaimDrift: report.ClaimDrift, QuarantineCount: report.Quarantine.Count,
		},
	}
	if err := wal.appendEvent(ctx, event); err != nil {
		return err
	}
	wal.finished = true
	return nil
}

func (wal *soakWAL) appendEvent(ctx context.Context, event walEvent) error {
	event.PreviousDigest = hex.EncodeToString(wal.previous[:])
	event.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	canonical, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var reservationID, eventUUID [16]byte
	if _, err = io.ReadFull(rand.Reader, reservationID[:]); err != nil {
		return err
	}
	if _, err = io.ReadFull(rand.Reader, eventUUID[:]); err != nil {
		return err
	}
	appendContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	appended, err := wal.writer.Append(appendContext, b5wal.Record{
		ReservationID: reservationID, EventUUID: eventUUID,
		EventSchemaID: soakWALSchema, EventSchemaVersion: soakWALSchemaVersion,
		CanonicalEvent: canonical,
	})
	if err != nil {
		return err
	}
	if appended.Identity.RecordOrdinal != wal.next {
		return fmt.Errorf("b5soak: WAL ordinal %d, expected %d", appended.Identity.RecordOrdinal, wal.next)
	}
	wal.next++
	wal.previous = sha256.Sum256(canonical)
	return nil
}

func (wal *soakWAL) sealAndVerify() error {
	if wal == nil || wal.writer == nil {
		return errors.New("b5soak: WAL is unavailable")
	}
	if !wal.started || !wal.finished {
		return errors.New("b5soak: WAL run boundary is incomplete")
	}
	if err := wal.writer.Seal(); err != nil {
		return err
	}
	verification, err := wal.verify()
	if err != nil {
		return err
	}
	wal.verification = verification
	return nil
}

func (wal *soakWAL) verify() (walVerification, error) {
	var verification walVerification
	file, err := os.Open(wal.path)
	if err != nil {
		return verification, err
	}
	defer file.Close()
	var previous [32]byte
	var runID, datasourceID string
	var summary *walRunSummary
	var derived walRunSummary
	seenFinish := false
	for ordinal := uint64(0); ordinal < wal.next; ordinal++ {
		extent := make([]byte, b5wal.EncodedRecordCharge())
		if _, err := io.ReadFull(file, extent); err != nil {
			return verification, fmt.Errorf("b5soak: WAL extent %d: %w", ordinal, err)
		}
		decoded, err := b5wal.DecodeRecord(wal.segment.Key[:], extent)
		if err != nil {
			return verification, fmt.Errorf("b5soak: WAL decode %d: %w", ordinal, err)
		}
		if decoded.RecordOrdinal != ordinal || decoded.SegmentID != wal.segment.Manifest.SegmentID ||
			decoded.EventSchemaID != soakWALSchema || decoded.EventSchemaVersion != soakWALSchemaVersion {
			return verification, fmt.Errorf("b5soak: WAL identity mismatch at %d", ordinal)
		}
		var event walEvent
		if err := json.Unmarshal(decoded.CanonicalEvent, &event); err != nil {
			return verification, fmt.Errorf("b5soak: WAL event %d: %w", ordinal, err)
		}
		if event.Schema != soakWALSchema || event.PreviousDigest != hex.EncodeToString(previous[:]) {
			return verification, fmt.Errorf("b5soak: WAL payload chain mismatch at %d", ordinal)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.ObservedAt); err != nil {
			return verification, fmt.Errorf("b5soak: WAL timestamp %d: %w", ordinal, err)
		}
		switch event.Kind {
		case walKindRunStart:
			if ordinal != 0 || runID != "" || event.RunID == "" || event.DatasourceID == "" || event.Summary != nil {
				return verification, errors.New("b5soak: invalid WAL run_start")
			}
			runID, datasourceID = event.RunID, event.DatasourceID
		case walKindTerminal:
			verification.TerminalRecords++
			if seenFinish || event.RunID != runID || event.DatasourceID != datasourceID || event.Terminal != verification.TerminalRecords ||
				event.TransactionID == "" || event.DBOutcome == "" || event.EvidenceConsistency == "" || event.Disposition == "" {
				return verification, fmt.Errorf("b5soak: invalid WAL terminal at %d", ordinal)
			}
			derived.Terminals++
			if event.DBOutcome == string(b5terminal.OutcomeCommitted) {
				derived.Committed++
			}
			if event.DBOutcome == string(b5terminal.OutcomeUnknown) {
				derived.NormalUnknown++
			}
			if event.EvidenceConsistency == string(b5terminal.VerdictContradiction) {
				derived.EvidenceContradiction++
			}
			if event.Disposition == string(b5terminal.DispositionDiscardUnconfirmed) {
				derived.DiscardUnconfirmed++
				derived.QuarantineCount++
			}
		case walKindRunFinish:
			if seenFinish || ordinal+1 != wal.next || event.RunID != runID || event.DatasourceID != datasourceID || event.Summary == nil ||
				event.Terminal != verification.TerminalRecords || event.Summary.Terminals != verification.TerminalRecords {
				return verification, errors.New("b5soak: invalid WAL run_finish")
			}
			seenFinish = true
			summary = event.Summary
		default:
			return verification, fmt.Errorf("b5soak: unknown WAL event kind %q", event.Kind)
		}
		verification.Records++
		previous = sha256.Sum256(decoded.CanonicalEvent)
	}
	var extra [1]byte
	if count, readErr := file.Read(extra[:]); count != 0 || readErr != io.EOF {
		return verification, errors.New("b5soak: WAL has trailing bytes")
	}
	if !seenFinish || summary == nil || verification.Records != wal.next || verification.TerminalRecords != wal.terminals || runID != wal.runID || datasourceID != wal.datasourceID {
		return verification, errors.New("b5soak: WAL verification totals mismatch")
	}
	if summary.Terminals != derived.Terminals || summary.Committed != derived.Committed || summary.NormalUnknown != derived.NormalUnknown ||
		summary.EvidenceContradiction != derived.EvidenceContradiction || summary.DiscardUnconfirmed != derived.DiscardUnconfirmed ||
		summary.QuarantineCount != derived.QuarantineCount {
		return verification, errors.New("b5soak: WAL run summary does not match terminal records")
	}
	verification.FinalDigest = previous
	return verification, nil
}

type soakExtentSink struct{ file *os.File }

func newSoakExtentSink(path string) (*soakExtentSink, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	directory, openErr := os.Open(filepath.Dir(path))
	if openErr == nil {
		openErr = directory.Sync()
		openErr = errors.Join(openErr, directory.Close())
	}
	// Windows does not expose directory fsync through os.File.Sync. The soak
	// driver still fsyncs every extent; production b5wal remains fail-closed.
	if openErr != nil && runtime.GOOS != "windows" {
		_ = file.Close()
		return nil, fmt.Errorf("b5soak: sync WAL directory: %w", openErr)
	}
	return &soakExtentSink{file: file}, nil
}

func (sink *soakExtentSink) WriteExtent(extent []byte) error {
	written, err := sink.file.Write(extent)
	if err == nil && written != len(extent) {
		return io.ErrShortWrite
	}
	return err
}

func (sink *soakExtentSink) Sync() error  { return sink.file.Sync() }
func (sink *soakExtentSink) Close() error { return sink.file.Close() }
