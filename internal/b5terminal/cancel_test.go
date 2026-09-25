package b5terminal

import (
	"errors"
	"testing"
)

func TestLateCancelFaultProxyMatrix(t *testing.T) {
	t.Parallel()
	t.Run("cancel delayed past successful Execute", func(t *testing.T) {
		guard := NewCancelGuard()
		proxy := NewFaultProxy(guard)
		proxy.ScheduleServerEvent(FaultEventExecuteSuccess, 1)
		proxy.ScheduleServerEvent(FaultEventReadyForQueryIdle, 1)
		proxy.SendCancel(CancelFullPacketWritten, 10)
		proxy.AdvanceTo(1)
		assertTaintedAndBlocked(t, guard, CommandRollback)
		if delivered := proxy.Delivered(); len(delivered) != 2 {
			t.Fatalf("delivered before delayed cancel = %+v", delivered)
		}
		proxy.AdvanceTo(10)
		if delivered := proxy.Delivered(); len(delivered) != 3 || delivered[2].Kind != FaultEventCancelPacket {
			t.Fatalf("delivered after delayed cancel = %+v", delivered)
		}
	})

	t.Run("cancel and rollback frame cannot reorder", func(t *testing.T) {
		guard := NewCancelGuard()
		proxy := NewFaultProxy(guard)
		proxy.SendCancel(CancelSendStartedOrIndeterminate, 20)
		if err := proxy.SendMain(CommandRollback, 1); !errors.Is(err, ErrCancelTainted) {
			t.Fatalf("rollback error = %v", err)
		}
		for _, event := range proxy.Pending() {
			if event.Kind == FaultEventMainCommand && event.Command == CommandRollback {
				t.Fatal("rollback was queued behind delayed cancel")
			}
		}
	})

	t.Run("cancel socket wrote while server handling is delayed", func(t *testing.T) {
		guard := NewCancelGuard()
		proxy := NewFaultProxy(guard)
		proxy.SendCancel(CancelFullPacketWritten, 100)
		proxy.ScheduleServerEvent(FaultEventCancelSocketClosed, 1)
		proxy.AdvanceTo(1)
		snapshot := guard.Snapshot()
		if !snapshot.Tainted || !snapshot.Diagnostics.CancelSocketClosed {
			t.Fatalf("snapshot = %+v", snapshot)
		}
		assertTaintedAndBlocked(t, guard, CommandStatement)
	})

	t.Run("statement timeout concurrent with cancel", func(t *testing.T) {
		guard := NewCancelGuard()
		proxy := NewFaultProxy(guard)
		proxy.SendCancel(CancelSendStartedOrIndeterminate, 5)
		proxy.ScheduleServerEvent(FaultEventStatementTimeout, 5)
		proxy.AdvanceTo(5)
		snapshot := guard.Snapshot()
		if !snapshot.Tainted || !snapshot.Diagnostics.StatementTimeoutObserved {
			t.Fatalf("snapshot = %+v", snapshot)
		}
		assertTaintedAndBlocked(t, guard, CommandCommit)
	})

	t.Run("late cancel cannot hit terminal reset or health", func(t *testing.T) {
		guard := NewCancelGuard()
		proxy := NewFaultProxy(guard)
		proxy.SendCancel(CancelFullPacketWritten, 50)
		for _, command := range []MainCommand{CommandRollback, CommandCommit, CommandReset, CommandHealthProbe, CommandStatement} {
			if err := proxy.SendMain(command, 0); !errors.Is(err, ErrCancelTainted) {
				t.Fatalf("command %v error = %v", command, err)
			}
		}
		snapshot := guard.Snapshot()
		for _, command := range []MainCommand{CommandRollback, CommandCommit, CommandReset, CommandHealthProbe, CommandStatement} {
			if snapshot.Sent[command] != 0 {
				t.Fatalf("command %v sent %d times", command, snapshot.Sent[command])
			}
		}
	})
}

func TestCancelTaintCannotBeClearedByObservations(t *testing.T) {
	t.Parallel()
	guard := NewCancelGuard()
	guard.ObserveEmission(CancelSendStartedOrIndeterminate)
	guard.ObserveReadyForQueryIdle()
	guard.ObserveExecuteExit()
	guard.ObserveCancelSocketClosed()
	guard.ObserveStatementTimeout()
	guard.ObserveEmission(CancelNotSentProven)
	snapshot := guard.Snapshot()
	if !snapshot.Tainted || snapshot.Emission != CancelSendStartedOrIndeterminate {
		t.Fatalf("taint was cleared: %+v", snapshot)
	}
	if guard.Disposition(true) != DispositionDiscarded || guard.Disposition(false) != DispositionDiscardUnconfirmed {
		t.Fatal("tainted disposition is not forced discard")
	}
	if GenerationBoundCancelFence {
		t.Fatal("v0.4 unexpectedly enabled a generation-bound cancel fence")
	}
}

func TestCancelNotSentProvenAllowsSingleRollbackPath(t *testing.T) {
	t.Parallel()
	guard := NewCancelGuard()
	guard.ObserveEmission(CancelNotSentProven)
	if err := guard.RecordMainSend(CommandRollback); err != nil {
		t.Fatal(err)
	}
	snapshot := guard.Snapshot()
	if snapshot.Tainted || snapshot.Sent[CommandRollback] != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func assertTaintedAndBlocked(t *testing.T, guard *CancelGuard, command MainCommand) {
	t.Helper()
	if !guard.Snapshot().Tainted {
		t.Fatal("guard is not tainted")
	}
	if err := guard.RecordMainSend(command); !errors.Is(err, ErrCancelTainted) {
		t.Fatalf("command %v error = %v", command, err)
	}
}
