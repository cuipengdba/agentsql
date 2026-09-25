package b5

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCanonicalEventV4Golden(t *testing.T) {
	event := CanonicalEvent{
		Schema:    SchemaRef{ID: EventSchemaID, Version: EventSchemaVersion},
		EventUUID: "018f0000-0000-7000-8000-000000000001", EventType: "tx_terminal_outcome",
		OccurredAt: time.Date(2026, 9, 25, 4, 5, 6, 0, time.UTC), TenantID: "tenant-1",
		PrincipalID: "principal-1", AgentID: "agent-1", DatasourceID: "ds-1",
		SessionID: "session-1", TransactionID: "tx-1", RequestID: "request-1",
		OwnerEpoch: 7, TransactionSequence: 4, PreviousTxEventDigest: "prev",
		Action: ActionUpdate, TransactionStatus: TransactionTerminal, TransactionPhase: PhaseTerminal,
		DBOutcome: OutcomeCommitted, TerminalEvidence: &TerminalEvidenceRef{
			Schema:    SchemaRef{ID: TerminalEvidenceSchemaID, Version: TerminalEvidenceVersion},
			Operation: OperationCommit, WritePhase: WriteFullFrame, Reply: ReplyCurrentPositiveACK,
			Consistency: EvidenceConsistent, Digest: "evidence",
		}, ConnectionDisposition: DispositionReleased, Effect: EffectTerminalCommitted,
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"schema":{"id":"agentsql.audit.event.v4","version":4},"event_uuid":"018f0000-0000-7000-8000-000000000001","event_type":"tx_terminal_outcome","occurred_at":"2026-09-25T04:05:06Z","tenant_id":"tenant-1","principal_id":"principal-1","agent_id":"agent-1","datasource_id":"ds-1","session_id":"session-1","transaction_id":"tx-1","request_id":"request-1","owner_epoch":7,"transaction_seq":4,"previous_tx_event_digest":"prev","action":"UPDATE","transaction_status":"TERMINAL","transaction_phase":"TERMINAL","db_outcome":"COMMITTED","terminal_evidence":{"schema":{"id":"agentsql.b5.terminal-evidence.v2","version":2},"operation":"COMMIT","write_phase":"FULL_FRAME_WRITTEN","reply":"CURRENT_POSITIVE_ACK","consistency":"CONSISTENT","digest":"evidence"},"connection_disposition":"RELEASED","effect":"TERMINAL_COMMITTED"}`
	if string(encoded) != golden {
		t.Fatalf("canonical event ABI drift\nwant: %s\n got: %s", golden, encoded)
	}
}

func TestCanonicalEnumGolden(t *testing.T) {
	values := []any{
		[]SessionStatus{SessionReady, SessionActive, SessionTerminal, SessionExpired},
		[]TransactionStatus{TransactionPending, TransactionActive, TransactionRollbackOnly, TransactionTerminal},
		[]WritePhase{WriteNotSent, WriteZeroBytes, WritePartial, WriteFullFrame, WriteIndeterminate},
		[]ConnectionDisposition{DispositionReleased, DispositionDiscarded, DispositionDiscardUnconfirmed},
		[]AuditDurability{DurabilityDurable, DurabilityAuditPending, DurabilityLost},
		[]DMLAction{ActionInsert, ActionUpdate, ActionDelete},
		[]GrantElement{GrantElementAction, GrantElementWriteTarget, GrantElementReference},
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `[["READY","ACTIVE","TERMINAL","EXPIRED"],["PENDING","ACTIVE","ROLLBACK_ONLY","TERMINAL"],["NOT_SENT","ZERO_BYTES_WRITTEN","PARTIAL_BYTES_WRITTEN","FULL_FRAME_WRITTEN","WRITE_INDETERMINATE"],["RELEASED","DISCARDED","DISCARD_UNCONFIRMED"],["DURABLE","AUDIT_PENDING","DURABILITY_LOST"],["INSERT","UPDATE","DELETE"],["ACTION","WRITE_TARGET","REFERENCE"]]`
	if string(encoded) != golden {
		t.Fatalf("canonical enum ABI drift\nwant: %s\n got: %s", golden, encoded)
	}
}
