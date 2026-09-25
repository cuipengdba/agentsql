package b5wal

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

type scriptedSink struct {
	mu       sync.Mutex
	extents  [][]byte
	sync     chan error
	writeErr error
	closed   bool
}

func newScriptedSink(syncResult ...error) *scriptedSink {
	channel := make(chan error, max(1, len(syncResult)))
	for _, result := range syncResult {
		channel <- result
	}
	return &scriptedSink{sync: channel}
}

func (sink *scriptedSink) WriteExtent(extent []byte) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.writeErr != nil {
		return sink.writeErr
	}
	sink.extents = append(sink.extents, append([]byte(nil), extent...))
	return nil
}
func (sink *scriptedSink) Sync() error { return <-sink.sync }
func (sink *scriptedSink) Close() error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.closed = true
	return nil
}

func newTestFactory() (SegmentFactory, *MemoryManifestStore) {
	manifest := &MemoryManifestStore{}
	return SegmentFactory{
		MasterRevision: "master-r1",
		Registry:       NewMemoryKeyRegistry(),
		Wrapper:        IdentityKeyWrapper{},
		Manifests:      manifest,
	}, manifest
}

func smallRecord(reservationID [16]byte, uuidByte byte) Record {
	var eventUUID [16]byte
	eventUUID[0] = uuidByte
	return Record{ReservationID: reservationID, EventUUID: eventUUID, EventSchemaID: "agentsql.audit.event.v4", EventSchemaVersion: 4, CanonicalEvent: []byte(`{"kind":"test"}`)}
}

func TestFileManifestStoreFsyncContract(t *testing.T) {
	directory := t.TempDir()
	factory := SegmentFactory{
		MasterRevision: "master-r1",
		Registry:       NewMemoryKeyRegistry(),
		Wrapper:        IdentityKeyWrapper{},
		Manifests:      FileManifestStore{Directory: directory},
	}
	segment, err := factory.Create(context.Background(), 11)
	if runtime.GOOS == "windows" && err != nil {
		// Windows does not expose a successful os.File.Sync for directory
		// handles here. The contract must fail closed after syncing the file,
		// never release a segment whose parent-directory durability is unknown.
		entries, readErr := os.ReadDir(directory)
		if readErr != nil || len(entries) != 1 {
			t.Fatalf("fail-closed manifest artifact: entries=%d readErr=%v createErr=%v", len(entries), readErr, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, hex.EncodeToString(segment.Manifest.SegmentID[:])+".manifest")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("empty durable manifest")
	}
}

func TestSegmentNonceMonotonicAndRotationNeverReusesKey(t *testing.T) {
	factory, manifests := newTestFactory()
	var sinks []*scriptedSink
	manager, err := NewManager(context.Background(), factory, 7, func(Segment) (ExtentSink, error) {
		sink := newScriptedSink(nil, nil, nil)
		sinks = append(sinks, sink)
		return sink, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var reservation [16]byte
	reservation[0] = 9
	first, err := manager.Current().Append(context.Background(), smallRecord(reservation, 1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Current().Append(context.Background(), smallRecord(reservation, 2))
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity.RecordOrdinal != 0 || second.Identity.RecordOrdinal != 1 || first.Identity.Nonce == second.Identity.Nonce {
		t.Fatalf("non-monotonic nonce identities: %+v %+v", first.Identity, second.Identity)
	}
	if err := manager.ChangeOwnership(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	third, err := manager.Current().Append(context.Background(), smallRecord(reservation, 3))
	if err != nil {
		t.Fatal(err)
	}
	if third.Identity.RecordOrdinal != 0 || third.Identity.SegmentID == first.Identity.SegmentID || manager.Current().Segment().Manifest.KeyID == manifests.Manifests[0].KeyID {
		t.Fatalf("rotation reused identity: %+v", third.Identity)
	}
	if len(manifests.Manifests) != 2 || !sinks[0].closed {
		t.Fatalf("manifest count=%d first closed=%v", len(manifests.Manifests), sinks[0].closed)
	}
}

func TestFsyncTimeoutLateSuccessRotationAndSecondWedge(t *testing.T) {
	factory, _ := newTestFactory()
	var sinks []*scriptedSink
	manager, err := NewManager(context.Background(), factory, 1, func(Segment) (ExtentSink, error) {
		sink := newScriptedSink()
		sinks = append(sinks, sink)
		return sink, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var reservation [16]byte
	reservation[0] = 1
	timedOut, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := manager.Current().Append(timedOut, smallRecord(reservation, 1))
	if !errors.Is(err, ErrSyncTimeout) || result.Late == nil || manager.Current().State() != WriterWedgedUnconfirmed {
		t.Fatalf("timeout result=%+v err=%v state=%v", result, err, manager.Current().State())
	}
	sinks[0].sync <- nil
	if late := <-result.Late; late != nil {
		t.Fatalf("late fsync = %v", late)
	}
	if manager.Current().State() != WriterWedgedUnconfirmed {
		t.Fatal("late success reopened writer")
	}
	if err := manager.RotateAfterWedge(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	if manager.Current().Segment().Manifest.SegmentID == result.Identity.SegmentID {
		t.Fatal("rotation reopened old segment")
	}
	timedOutAgain, cancelAgain := context.WithCancel(context.Background())
	cancelAgain()
	second, err := manager.Current().Append(timedOutAgain, smallRecord(reservation, 2))
	if !errors.Is(err, ErrSyncTimeout) {
		t.Fatalf("second timeout = %v", err)
	}
	if err := manager.RotateAfterWedge(context.Background(), reservation); !errors.Is(err, ErrRotationExhausted) {
		t.Fatalf("second rotation error = %v", err)
	}
	sinks[1].sync <- nil
	if late := <-second.Late; late != nil {
		t.Fatal(late)
	}
}

func TestDiskFailureAndLateCallbacksOutOfOrder(t *testing.T) {
	factory, _ := newTestFactory()
	segment1, err := factory.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	segment2, err := factory.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	sink1, sink2 := newScriptedSink(), newScriptedSink()
	writer1, _ := NewWriter(segment1, sink1)
	writer2, _ := NewWriter(segment2, sink2)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel1()
	cancel2()
	result1, err1 := writer1.Append(ctx1, smallRecord([16]byte{}, 1))
	result2, err2 := writer2.Append(ctx2, smallRecord([16]byte{}, 2))
	if !errors.Is(err1, ErrSyncTimeout) || !errors.Is(err2, ErrSyncTimeout) {
		t.Fatalf("timeouts: %v %v", err1, err2)
	}
	sink2.sync <- nil
	if err := <-result2.Late; err != nil {
		t.Fatal(err)
	}
	sink1.sync <- errors.New("device error")
	if err := <-result1.Late; err == nil {
		t.Fatal("late device error lost")
	}

	failedSink := newScriptedSink(nil)
	failedSink.writeErr = errors.New("disk full")
	writer3, _ := NewWriter(segment1, failedSink)
	if _, err := writer3.Append(context.Background(), smallRecord([16]byte{}, 3)); err == nil || writer3.State() != WriterWedgedUnconfirmed {
		t.Fatalf("disk-full err=%v state=%v", err, writer3.State())
	}
}
