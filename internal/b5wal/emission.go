package b5wal

// RecordKind is an event-v4 semantic kind. Result receipts are deliberately
// absent: they live in ReceiptStore and do not consume WAL record slots.
type RecordKind string

const (
	RecordStatement            RecordKind = "tx_statement"
	RecordRollback             RecordKind = "tx_rollback"
	RecordTerminalOutcome      RecordKind = "tx_terminal_outcome"
	RecordSessionTerminal      RecordKind = "session_terminal"
	RecordDurabilityDiagnostic RecordKind = "durability_diagnostic"
	RecordRecovery             RecordKind = "tx_durability_recovered"
	RecordCommitIntent         RecordKind = "tx_commit_intent"
)

type LastSuccessState string
type Transition string

const (
	LastBeforeBeginBarrier      LastSuccessState = "before_begin_barrier"
	LastActiveBeforeEffect      LastSuccessState = "active_before_statement_effect"
	LastAfterStatementEffect    LastSuccessState = "after_statement_effect"
	LastRollbackOnly            LastSuccessState = "rollback_only"
	LastBeforeCommitIntent      LastSuccessState = "before_commit_intent_durable"
	LastIntentDurableBeforeSend LastSuccessState = "intent_durable_before_commit_send"
	LastPositiveCommitACK       LastSuccessState = "positive_commit_ack"
	LastUnknownCommitOutcome    LastSuccessState = "unknown_commit_outcome"
	LastRollbackACK             LastSuccessState = "rollback_ack"
)

const (
	TransitionPrimaryFailure Transition = "primary_failure"
	TransitionFsyncTimeout   Transition = "wal_fsync_timeout"
	TransitionLateRecovery   Transition = "late_or_restart_recovery"
)

// EmissionPath is one complete path after the named primary last-success point.
// Multiplicity is len(Records): each semantic kind is emitted at most once.
// A post-timeout rotation creates no record, the diagnostic coalesces all
// failures for the reservation, and recovery is a single coalesced record.
type EmissionPath struct {
	State                            LastSuccessState
	Transition                       Transition
	Records                          []RecordKind
	Rotation                         bool
	PermanentLossIfReplacementWedges []RecordKind
}

var frozenEmissionPaths = []EmissionPath{
	{LastBeforeBeginBarrier, TransitionPrimaryFailure, []RecordKind{RecordDurabilityDiagnostic, RecordSessionTerminal, RecordRecovery}, true, []RecordKind{RecordSessionTerminal, RecordRecovery}},
	{LastActiveBeforeEffect, TransitionPrimaryFailure, []RecordKind{RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordRecovery}},
	{LastAfterStatementEffect, TransitionFsyncTimeout, []RecordKind{RecordStatement, RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}},
	{LastRollbackOnly, TransitionPrimaryFailure, []RecordKind{RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordTerminalOutcome, RecordSessionTerminal, RecordRecovery}},
	{LastBeforeCommitIntent, TransitionFsyncTimeout, []RecordKind{RecordCommitIntent, RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}},
	{LastIntentDurableBeforeSend, TransitionPrimaryFailure, []RecordKind{RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordTerminalOutcome, RecordSessionTerminal, RecordRecovery}},
	{LastPositiveCommitACK, TransitionPrimaryFailure, []RecordKind{RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordSessionTerminal, RecordRecovery}},
	{LastUnknownCommitOutcome, TransitionPrimaryFailure, []RecordKind{RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordSessionTerminal, RecordRecovery}},
	{LastRollbackACK, TransitionPrimaryFailure, []RecordKind{RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}, true, []RecordKind{RecordSessionTerminal, RecordRecovery}},
}

func EmissionPaths() []EmissionPath {
	paths := make([]EmissionPath, len(frozenEmissionPaths))
	for index, path := range frozenEmissionPaths {
		paths[index] = path
		paths[index].Records = append([]RecordKind(nil), path.Records...)
		paths[index].PermanentLossIfReplacementWedges = append([]RecordKind(nil), path.PermanentLossIfReplacementWedges...)
	}
	return paths
}

func ComputedMaxPathRecordCount() int {
	maximum := 0
	for _, path := range frozenEmissionPaths {
		if len(path.Records) > maximum {
			maximum = len(path.Records)
		}
	}
	return maximum
}
