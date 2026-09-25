package b5coordinator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5wal"
)

type coordinatorPrimary struct {
	mu     sync.Mutex
	events []b5wal.PrimaryEvent
	err    error
}

func (primary *coordinatorPrimary) Put(_ context.Context, event b5wal.PrimaryEvent) (bool, error) {
	primary.mu.Lock()
	defer primary.mu.Unlock()
	if primary.err != nil {
		return false, primary.err
	}
	primary.events = append(primary.events, event)
	return false, nil
}

type coordinatorSink struct{}

func (coordinatorSink) WriteExtent([]byte) error { return nil }
func (coordinatorSink) Sync() error              { return nil }
func (coordinatorSink) Close() error             { return nil }

func newCoordinatorWALAuditor(t *testing.T, primary *coordinatorPrimary) *WALAuditor {
	t.Helper()
	factory := b5wal.SegmentFactory{MasterRevision: "test", Registry: b5wal.NewMemoryKeyRegistry(), Wrapper: b5wal.IdentityKeyWrapper{}, Manifests: &b5wal.MemoryManifestStore{}}
	manager, err := b5wal.NewManager(context.Background(), factory, 1, func(b5wal.Segment) (b5wal.ExtentSink, error) { return coordinatorSink{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	auditor, err := NewWALAuditor(&b5wal.Service{Primary: primary, Receipts: b5wal.NewMemoryReceiptStore(), WAL: manager})
	if err != nil {
		t.Fatal(err)
	}
	return auditor
}

func TestWALAuditorBeginBarrierAndTransactionDigestChain(t *testing.T) {
	primary := &coordinatorPrimary{}
	auditor := newCoordinatorWALAuditor(t, primary)
	plan := [32]byte{1}
	begin, err := auditor.Barrier(context.Background(), AuditEvent{Kind: AuditTxBegin, TransactionID: "tx", SessionID: "session", RequestID: "begin", Sequence: 1, PlanDigest: plan})
	if err != nil || begin.Durability != b5.DurabilityDurable {
		t.Fatalf("begin=%+v err=%v", begin, err)
	}
	statement, err := auditor.Barrier(context.Background(), AuditEvent{Kind: AuditStatement, TransactionID: "tx", SessionID: "session", RequestID: "execute", Sequence: 2, PlanDigest: plan, StatementOrdinal: 0, Action: b5.ActionUpdate, AffectedRows: 1})
	if err != nil || statement.Durability != b5.DurabilityDurable {
		t.Fatalf("statement=%+v err=%v", statement, err)
	}
	primary.mu.Lock()
	defer primary.mu.Unlock()
	if len(primary.events) != 2 || primary.events[1].PreviousTxDigest != begin.EventDigest || primary.events[1].TransactionSeq != 2 {
		t.Fatalf("events=%+v begin=%x", primary.events, begin.EventDigest)
	}
}

func TestWALAuditorBeginPrimaryFailureIsFailClosed(t *testing.T) {
	primary := &coordinatorPrimary{err: errors.New("primary unavailable")}
	auditor := newCoordinatorWALAuditor(t, primary)
	result, err := auditor.Barrier(context.Background(), AuditEvent{Kind: AuditTxBegin, TransactionID: "tx", SessionID: "session", RequestID: "begin", Sequence: 1, PlanDigest: [32]byte{1}})
	if err == nil || result.Durability != b5.DurabilityLost {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
