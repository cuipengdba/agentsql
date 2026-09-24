package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

type fakeBatchBackend struct {
	mu    sync.Mutex
	calls [][]model.AuditLog
	fn    func(int, []model.AuditLog) ([]model.AuditLog, error)
}

func (backend *fakeBatchBackend) InsertBatch(
	ctx context.Context,
	batch []model.AuditLog,
) ([]model.AuditLog, error) {
	copyBatch := append([]model.AuditLog(nil), batch...)
	backend.mu.Lock()
	call := len(backend.calls)
	backend.calls = append(backend.calls, copyBatch)
	fn := backend.fn
	backend.mu.Unlock()
	if fn != nil {
		return fn(call, copyBatch)
	}
	return committedRows(copyBatch), nil
}

func (backend *fakeBatchBackend) snapshotCalls() [][]model.AuditLog {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	result := make([][]model.AuditLog, len(backend.calls))
	for index := range backend.calls {
		result[index] = append([]model.AuditLog(nil), backend.calls[index]...)
	}
	return result
}

func committedRows(batch []model.AuditLog) []model.AuditLog {
	rows := make([]model.AuditLog, len(batch))
	for index := range batch {
		rows[index] = batch[index]
		rows[index].ID = int64(index + 1)
	}
	return rows
}

func testAuditLog(uuid string) model.AuditLog {
	statement := "select " + uuid
	return model.AuditLog{Decision: "allow", EventUUID: stringPtr(uuid), SQLRaw: &statement}
}

func stringPtr(value string) *string { return &value }

func testSink(backend BatchBackend, options ...Option) *GroupCommitSink {
	base := []Option{
		WithQueueCapacity(16),
		WithQueueMaxBytes(1 << 20),
		WithMaxBatch(8),
		WithLinger(20 * time.Millisecond),
		WithEnqueueWaitTimeout(40 * time.Millisecond),
		WithAttemptTimeout(100 * time.Millisecond),
		WithRootTimeout(500 * time.Millisecond),
		WithDrainTimeout(500 * time.Millisecond),
	}
	base = append(base, options...)
	return NewGroupCommitSink(backend, func(model.AuditLog) string { return "management" }, base...)
}

func insertAsync(sink *GroupCommitSink, log model.AuditLog) <-chan groupCommitResult {
	result := make(chan groupCommitResult, 1)
	go func() {
		row, err := sink.Insert(context.Background(), log)
		result <- groupCommitResult{row: row, err: err}
	}()
	return result
}

func requireResult(t *testing.T, result <-chan groupCommitResult) groupCommitResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for insert")
		return groupCommitResult{}
	}
}

func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	require.Eventually(t, predicate, 2*time.Second, time.Millisecond)
}

func requireSinkError(t *testing.T, err error, code executor.DBErrorCode, reason string) *SinkError {
	t.Helper()
	var typed *SinkError
	require.ErrorAs(t, err, &typed)
	require.Equal(t, code, typed.Code)
	require.Equal(t, reason, typed.Reason)
	return typed
}

func TestGroupCommitBatchingAndLinger(t *testing.T) {
	t.Run("full batch flushes immediately and queued backlog does not relinger", func(t *testing.T) {
		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		backend := &fakeBatchBackend{}
		backend.fn = func(call int, batch []model.AuditLog) ([]model.AuditLog, error) {
			if call == 0 {
				close(firstEntered)
				<-releaseFirst
			}
			return committedRows(batch), nil
		}
		sink := testSink(backend, WithMaxBatch(2), WithLinger(time.Second))
		results := []<-chan groupCommitResult{
			insertAsync(sink, testAuditLog("full-1")),
			insertAsync(sink, testAuditLog("full-2")),
		}
		select {
		case <-firstEntered:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("full batch waited for linger")
		}
		results = append(results,
			insertAsync(sink, testAuditLog("backlog-1")),
			insertAsync(sink, testAuditLog("backlog-2")),
			insertAsync(sink, testAuditLog("backlog-3")),
		)
		eventually(t, func() bool { return sink.Stats().QueueDepth == 3 })
		close(releaseFirst)
		for _, result := range results {
			require.NoError(t, requireResult(t, result).err)
		}
		require.NoError(t, sink.Close())
		calls := backend.snapshotCalls()
		require.Len(t, calls, 3)
		require.Len(t, calls[0], 2)
		require.Len(t, calls[1], 2)
		require.Len(t, calls[2], 1)
	})

	t.Run("idle first event waits exactly one linger", func(t *testing.T) {
		entered := make(chan time.Time, 1)
		backend := &fakeBatchBackend{fn: func(_ int, batch []model.AuditLog) ([]model.AuditLog, error) {
			entered <- time.Now()
			return committedRows(batch), nil
		}}
		sink := testSink(backend, WithLinger(35*time.Millisecond))
		started := time.Now()
		result := insertAsync(sink, testAuditLog("linger"))
		when := <-entered
		require.GreaterOrEqual(t, when.Sub(started), 25*time.Millisecond)
		require.Less(t, when.Sub(started), 150*time.Millisecond)
		require.NoError(t, requireResult(t, result).err)
		require.NoError(t, sink.Close())
	})

	t.Run("drain flushes without linger", func(t *testing.T) {
		backend := &fakeBatchBackend{}
		sink := testSink(backend, WithLinger(time.Second))
		result := insertAsync(sink, testAuditLog("drain"))
		eventually(t, func() bool { return sink.Stats().QueueDepth == 1 })
		started := time.Now()
		require.NoError(t, sink.Close())
		require.Less(t, time.Since(started), 200*time.Millisecond)
		require.NoError(t, requireResult(t, result).err)
		require.Len(t, backend.snapshotCalls(), 1)
	})
}

func TestGroupCommitFIFOAndUUIDResultMapping(t *testing.T) {
	backend := &fakeBatchBackend{fn: func(_ int, batch []model.AuditLog) ([]model.AuditLog, error) {
		rows := committedRows(batch)
		for index := range rows {
			rows[index].ID = int64(1000 + index)
		}
		for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
			rows[left], rows[right] = rows[right], rows[left]
		}
		return rows, nil
	}}
	sink := testSink(backend, WithMaxBatch(6), WithLinger(10*time.Millisecond))
	const count = 24
	type answer struct {
		uuid string
		row  model.AuditLog
		err  error
	}
	answers := make(chan answer, count)
	for index := 0; index < count; index++ {
		uuid := fmt.Sprintf("mixed-%02d", index)
		log := testAuditLog(uuid)
		statement := stringsOfLength(16 + index*37)
		log.SQLRaw = &statement
		go func() {
			row, err := sink.Insert(context.Background(), log)
			answers <- answer{uuid: uuid, row: row, err: err}
		}()
	}
	seen := make(map[string]bool, count)
	for index := 0; index < count; index++ {
		answer := <-answers
		require.NoError(t, answer.err)
		require.Equal(t, answer.uuid, auditEventUUID(answer.row))
		require.False(t, seen[answer.uuid])
		seen[answer.uuid] = true
	}
	require.NoError(t, sink.Close())
	for _, call := range backend.snapshotCalls() {
		sequences := make([]string, len(call))
		for index := range call {
			sequences[index] = auditEventUUID(call[index])
		}
		sorted := append([]string(nil), sequences...)
		sort.Strings(sorted)
		// Concurrent admission order is intentionally not lexical. This assertion
		// merely ensures a backend call never duplicated an admitted UUID.
		require.Len(t, sequences, len(uniqueStrings(sorted)))
	}
}

func TestGroupCommitFIFOAdmissionOrder(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBatchBackend{fn: func(call int, batch []model.AuditLog) ([]model.AuditLog, error) {
		if call == 0 {
			close(entered)
			<-release
		}
		return committedRows(batch), nil
	}}
	sink := testSink(backend, WithMaxBatch(3), WithLinger(5*time.Millisecond))
	results := []<-chan groupCommitResult{insertAsync(sink, testAuditLog("fifo-blocker"))}
	<-entered
	expected := make([]string, 0, 9)
	for index := 0; index < 9; index++ {
		uuid := fmt.Sprintf("fifo-%02d", index)
		expected = append(expected, uuid)
		results = append(results, insertAsync(sink, testAuditLog(uuid)))
		wantDepth := index + 1
		eventually(t, func() bool { return sink.Stats().QueueDepth == wantDepth })
	}
	close(release)
	for _, result := range results {
		require.NoError(t, requireResult(t, result).err)
	}
	require.NoError(t, sink.Close())
	actual := make([]string, 0, len(expected))
	for callIndex, call := range backend.snapshotCalls() {
		for rowIndex, row := range call {
			if callIndex == 0 && rowIndex == 0 {
				continue
			}
			actual = append(actual, auditEventUUID(row))
		}
	}
	require.Equal(t, expected, actual)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := []string{values[0]}
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func TestGroupCommitAcknowledgesOnlyDurableBackendResult(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBatchBackend{fn: func(_ int, _ []model.AuditLog) ([]model.AuditLog, error) {
		close(entered)
		<-release
		return nil, NewBatchError(BatchFailureGlobal, false, errors.New("database unavailable"))
	}}
	sink := testSink(backend, WithMaxBatch(1))
	result := insertAsync(sink, testAuditLog("ack"))
	<-entered
	select {
	case <-result:
		t.Fatal("insert acknowledged before durable backend returned")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	completed := requireResult(t, result)
	require.Zero(t, completed.row)
	requireSinkError(t, completed.err, executor.DBErrorCodeAuditUnavailable, "audit_backend_unavailable")
	require.NoError(t, sink.Close())
}

func TestGroupCommitBackpressureRecovers(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBatchBackend{fn: func(call int, batch []model.AuditLog) ([]model.AuditLog, error) {
		if call == 0 {
			close(entered)
			<-release
		}
		return committedRows(batch), nil
	}}
	sink := testSink(backend,
		WithQueueCapacity(1),
		WithMaxBatch(1),
		WithEnqueueWaitTimeout(35*time.Millisecond),
	)
	first := insertAsync(sink, testAuditLog("capacity-1"))
	<-entered
	started := time.Now()
	_, err := sink.Insert(context.Background(), testAuditLog("capacity-2"))
	require.GreaterOrEqual(t, time.Since(started), 25*time.Millisecond)
	typed := requireSinkError(t, err, executor.DBErrorCodeAuditOverloaded, "audit_backpressure")
	require.True(t, typed.Retryable)
	require.Equal(t, 100, typed.RetryAfterMS)
	require.Equal(t, "error", typed.Decision)
	close(release)
	require.NoError(t, requireResult(t, first).err)
	row, err := sink.Insert(context.Background(), testAuditLog("capacity-3"))
	require.NoError(t, err)
	require.Equal(t, "capacity-3", auditEventUUID(row))
	require.NoError(t, sink.Close())
	require.Zero(t, sink.Stats().CapacityCount)
}

func TestGroupCommitUUIDMergeConflictAndEntityTurnover(t *testing.T) {
	t.Run("same canonical joins in-flight and conflict fails immediately", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		backend := &fakeBatchBackend{fn: func(_ int, batch []model.AuditLog) ([]model.AuditLog, error) {
			close(entered)
			<-release
			rows := committedRows(batch)
			rows[0].ID = 99
			return rows, nil
		}}
		sink := testSink(backend, WithMaxBatch(1))
		log := testAuditLog("merge")
		first := insertAsync(sink, log)
		<-entered
		second := insertAsync(sink, log)
		eventually(t, func() bool { return sink.Stats().Waiters == 2 })
		conflict := log
		conflict.Decision = "deny"
		_, err := sink.Insert(context.Background(), conflict)
		requireSinkError(t, err, executor.DBErrorCodeGatewayInternal, "audit_uuid_conflict")
		close(release)
		one := requireResult(t, first)
		two := requireResult(t, second)
		require.NoError(t, one.err)
		require.NoError(t, two.err)
		require.Equal(t, int64(99), one.row.ID)
		require.Equal(t, one.row, two.row)
		require.Len(t, backend.snapshotCalls(), 1)
		require.NoError(t, sink.Close())
	})

	t.Run("capacity wait outlives old entity and creates a new entity", func(t *testing.T) {
		entered := make(chan struct{}, 2)
		releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
		backend := &fakeBatchBackend{fn: func(call int, batch []model.AuditLog) ([]model.AuditLog, error) {
			entered <- struct{}{}
			<-releases[call]
			return committedRows(batch), nil
		}}
		sink := testSink(backend,
			WithQueueCapacity(1),
			WithMaxBatch(1),
			WithEnqueueWaitTimeout(250*time.Millisecond),
		)
		log := testAuditLog("turnover")
		first := insertAsync(sink, log)
		<-entered
		second := insertAsync(sink, log)
		eventually(t, func() bool { return sink.Stats().AdmissionTokens == 1 })
		close(releases[0])
		require.NoError(t, requireResult(t, first).err)
		<-entered
		close(releases[1])
		require.NoError(t, requireResult(t, second).err)
		require.Len(t, backend.snapshotCalls(), 2)
		require.NoError(t, sink.Close())
	})
}

func TestGroupCommitAdmissionTokenCleanup(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBatchBackend{fn: func(call int, batch []model.AuditLog) ([]model.AuditLog, error) {
		if call == 0 {
			close(entered)
			<-release
		}
		return committedRows(batch), nil
	}}
	sink := testSink(backend,
		WithQueueCapacity(1),
		WithMaxBatch(1),
		WithMaxWaiters(3),
		WithEnqueueWaitTimeout(30*time.Millisecond),
	)
	first := insertAsync(sink, testAuditLog("tokens-1"))
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sink.Insert(ctx, testAuditLog("tokens-cancel"))
	require.ErrorIs(t, err, context.Canceled)

	_, err = sink.Insert(context.Background(), testAuditLog("tokens-timeout"))
	requireSinkError(t, err, executor.DBErrorCodeAuditOverloaded, "audit_backpressure")

	// The waiter ceiling is tested while two lock-external reservations are
	// blocked behind the in-batch request.
	ctxA, cancelA := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelA()
	ctxB, cancelB := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelB()
	blockedA := make(chan error, 1)
	blockedB := make(chan error, 1)
	go func() { _, value := sink.Insert(ctxA, testAuditLog("tokens-a")); blockedA <- value }()
	go func() { _, value := sink.Insert(ctxB, testAuditLog("tokens-b")); blockedB <- value }()
	eventually(t, func() bool { return sink.Stats().Waiters == 3 })
	_, err = sink.Insert(context.Background(), testAuditLog("tokens-limit"))
	requireSinkError(t, err, executor.DBErrorCodeAuditOverloaded, "audit_backpressure")
	require.Error(t, <-blockedA)
	require.Error(t, <-blockedB)

	close(release)
	require.NoError(t, requireResult(t, first).err)
	require.NoError(t, sink.Close())
	stats := sink.Stats()
	require.Zero(t, stats.AdmissionTokens)
	require.Zero(t, stats.Waiters)
	require.Zero(t, stats.CapacityCount)
	require.Zero(t, stats.CapacityBytes)
}

func TestGroupCommitAdmissionPhaseTwoFailureReleasesTokenAndCapacity(t *testing.T) {
	tests := []struct {
		name       string
		secondCall func() (string, error)
		reason     string
	}{
		{
			name: "digest failure",
			secondCall: func() (string, error) {
				return "", errors.New("canonical encoder failed")
			},
			reason: "audit_canonicalization_failed",
		},
		{
			name: "digest changed while capacity was reserved",
			secondCall: func() (string, error) {
				return "changed", nil
			},
			reason: "audit_uuid_conflict",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			backend := &fakeBatchBackend{}
			sink := testSink(backend, WithCanonicalDigest(func(model.AuditLog) (string, error) {
				if calls.Add(1) == 1 {
					return "stable", nil
				}
				return test.secondCall()
			}))
			_, err := sink.Insert(context.Background(), testAuditLog("phase-two"))
			requireSinkError(t, err, executor.DBErrorCodeGatewayInternal, test.reason)
			stats := sink.Stats()
			require.Zero(t, stats.AdmissionTokens)
			require.Zero(t, stats.Waiters)
			require.Zero(t, stats.CapacityCount)
			require.Zero(t, stats.CapacityBytes)
			require.Empty(t, backend.snapshotCalls())
			require.NoError(t, sink.Close())
		})
	}
}

func TestGroupCommitRetryBisectionAndGlobalFailure(t *testing.T) {
	t.Run("transient rollback retries same subbatch", func(t *testing.T) {
		backend := &fakeBatchBackend{fn: func(call int, batch []model.AuditLog) ([]model.AuditLog, error) {
			if call < 2 {
				return nil, NewBatchError(BatchFailureTransient, true, errors.New("serialization"))
			}
			return committedRows(batch), nil
		}}
		sink := testSink(backend, WithMaxBatch(1))
		row, err := sink.Insert(context.Background(), testAuditLog("retry"))
		require.NoError(t, err)
		require.Equal(t, "retry", auditEventUUID(row))
		require.Equal(t, uint64(2), sink.Stats().Retries)
		require.Len(t, backend.snapshotCalls(), 3)
		require.NoError(t, sink.Close())
	})

	t.Run("one poison event is quarantined while good events commit", func(t *testing.T) {
		backend := &fakeBatchBackend{fn: func(_ int, batch []model.AuditLog) ([]model.AuditLog, error) {
			for _, log := range batch {
				if auditEventUUID(log) == "poison" {
					return nil, NewBatchError(BatchFailureDeterministic, false, errors.New("bad event"))
				}
			}
			return committedRows(batch), nil
		}}
		sink := testSink(backend, WithMaxBatch(8), WithLinger(50*time.Millisecond))
		results := make(map[string]<-chan groupCommitResult)
		for _, uuid := range []string{"good-0", "good-1", "poison", "good-2", "good-3", "good-4", "good-5", "good-6"} {
			results[uuid] = insertAsync(sink, testAuditLog(uuid))
		}
		for uuid, result := range results {
			completed := requireResult(t, result)
			if uuid == "poison" {
				requireSinkError(t, completed.err, executor.DBErrorCodeGatewayInternal, "audit_event_rejected")
			} else {
				require.NoError(t, completed.err)
				require.Equal(t, uuid, auditEventUUID(completed.row))
			}
		}
		require.Equal(t, uint64(1), sink.Stats().Quarantined)
		require.LessOrEqual(t, len(backend.snapshotCalls()), 9)
		require.NoError(t, sink.Close())
	})

	t.Run("global error never bisects", func(t *testing.T) {
		backend := &fakeBatchBackend{fn: func(_ int, _ []model.AuditLog) ([]model.AuditLog, error) {
			return nil, NewBatchError(BatchFailureGlobal, false, errors.New("key missing"))
		}}
		sink := testSink(backend, WithMaxBatch(4))
		results := []<-chan groupCommitResult{
			insertAsync(sink, testAuditLog("global-1")),
			insertAsync(sink, testAuditLog("global-2")),
			insertAsync(sink, testAuditLog("global-3")),
			insertAsync(sink, testAuditLog("global-4")),
		}
		for _, result := range results {
			requireSinkError(t, requireResult(t, result).err, executor.DBErrorCodeAuditUnavailable, "audit_backend_unavailable")
		}
		require.Len(t, backend.snapshotCalls(), 1)
		require.NoError(t, sink.Close())
	})
}

func TestGroupCommitCloseDrainUnknownAndOrdering(t *testing.T) {
	t.Run("stop admission and drain without loss before exit hook", func(t *testing.T) {
		var exited atomic.Bool
		backend := &fakeBatchBackend{}
		sink := testSink(backend,
			WithMaxBatch(10),
			WithLinger(time.Second),
			WithFlusherExitedHook(func(string) { exited.Store(true) }),
		)
		results := make([]<-chan groupCommitResult, 0, 7)
		for index := 0; index < 7; index++ {
			results = append(results, insertAsync(sink, testAuditLog(fmt.Sprintf("close-%d", index))))
		}
		eventually(t, func() bool { return sink.Stats().QueueDepth == 7 })
		require.NoError(t, sink.Close())
		require.True(t, exited.Load())
		for _, result := range results {
			require.NoError(t, requireResult(t, result).err)
		}
		_, err := sink.Insert(context.Background(), testAuditLog("late"))
		requireSinkError(t, err, executor.DBErrorCodeAuditUnavailable, "audit_closing")
		require.Zero(t, sink.Stats().ActiveFlusherGoroutines)
	})

	t.Run("commit unknown is distinct during close", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		backend := &fakeBatchBackend{fn: func(_ int, _ []model.AuditLog) ([]model.AuditLog, error) {
			close(entered)
			<-release
			return nil, NewBatchError(BatchFailureCommitUnknown, false, errors.New("connection lost after commit"))
		}}
		sink := testSink(backend, WithMaxBatch(1))
		result := insertAsync(sink, testAuditLog("unknown"))
		<-entered
		closed := make(chan struct{})
		go func() { _ = sink.Close(); close(closed) }()
		eventually(t, func() bool {
			sink.mu.Lock()
			defer sink.mu.Unlock()
			return !sink.accepting
		})
		close(release)
		completed := requireResult(t, result)
		requireSinkError(t, completed.err, executor.DBErrorCodeAuditUnavailable, "commit_unknown")
		<-closed
		stats := sink.Stats()
		require.Equal(t, uint64(1), stats.ClosingCommitUnknown)
		require.Zero(t, stats.ClosingNotStarted)
		require.Zero(t, stats.ClosingRolledBack)
	})

	t.Run("drain deadline counts queued work as not started", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		backend := &fakeBatchBackend{fn: func(_ int, _ []model.AuditLog) ([]model.AuditLog, error) {
			close(entered)
			<-release
			return nil, NewBatchError(BatchFailureTransient, false, errors.New("rolled back"))
		}}
		sink := testSink(backend,
			WithQueueCapacity(3),
			WithMaxBatch(1),
			WithDrainTimeout(35*time.Millisecond),
			WithAttemptTimeout(100*time.Millisecond),
		)
		first := insertAsync(sink, testAuditLog("rolled-back"))
		<-entered
		second := insertAsync(sink, testAuditLog("not-started"))
		eventually(t, func() bool { return sink.Stats().QueueDepth == 1 })
		closed := make(chan struct{})
		go func() { _ = sink.Close(); close(closed) }()
		eventually(t, func() bool {
			sink.mu.Lock()
			defer sink.mu.Unlock()
			return !sink.accepting
		})
		time.Sleep(40 * time.Millisecond)
		close(release)
		<-closed
		require.Error(t, requireResult(t, first).err)
		require.Error(t, requireResult(t, second).err)
		stats := sink.Stats()
		require.Equal(t, uint64(1), stats.ClosingRolledBack)
		require.Equal(t, uint64(1), stats.ClosingNotStarted)
	})
}

func TestGroupCommitDuplicateCoordinatorGuardAndOversize(t *testing.T) {
	backend := &fakeBatchBackend{}
	first := testSink(backend)
	second := testSink(backend)
	firstResult := insertAsync(first, testAuditLog("registry-1"))
	eventually(t, func() bool { return first.Stats().ActiveFlusherGoroutines == 1 })
	_, err := second.Insert(context.Background(), testAuditLog("registry-2"))
	requireSinkError(t, err, executor.DBErrorCodeGatewayInternal, "duplicate_chain_coordinator")
	require.NoError(t, requireResult(t, firstResult).err)
	require.NoError(t, second.Close())
	require.NoError(t, first.Close())

	large := stringsOfLength(1024)
	log := testAuditLog("large")
	log.SQLRaw = &large
	third := testSink(&fakeBatchBackend{}, WithMaxEventBytes(128))
	_, err = third.Insert(context.Background(), log)
	requireSinkError(t, err, executor.DBErrorCodeGatewayInternal, "audit_event_too_large")
	require.NoError(t, third.Close())
}

func stringsOfLength(length int) string {
	result := make([]byte, length)
	for index := range result {
		result[index] = 'x'
	}
	return string(result)
}
