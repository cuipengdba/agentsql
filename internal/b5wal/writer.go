package b5wal

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
)

type WriterState uint8

const (
	WriterOpen WriterState = iota
	WriterWedgedUnconfirmed
	WriterSealed
)

var (
	ErrWriterNotOpen     = errors.New("b5wal: writer is not open")
	ErrRotationExhausted = errors.New("b5wal: reservation already used its one post-wedge rotation")
	ErrSyncTimeout       = errors.New("b5wal: fsync confirmation timed out")
)

// ExtentSink writes exactly one aligned extent and durably syncs it.
type ExtentSink interface {
	WriteExtent(extent []byte) error
	Sync() error
	Close() error
}

type FileExtentSink struct{ file *os.File }

// NewFileExtentSink always creates a new file. There is intentionally no API
// for reopening an old segment for append.
func NewFileExtentSink(path string) (*FileExtentSink, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileExtentSink{file: file}, nil
}

func (sink *FileExtentSink) WriteExtent(extent []byte) error {
	written, err := sink.file.Write(extent)
	if err == nil && written != len(extent) {
		return fmt.Errorf("b5wal: short extent write: %d/%d", written, len(extent))
	}
	return err
}
func (sink *FileExtentSink) Sync() error  { return sink.file.Sync() }
func (sink *FileExtentSink) Close() error { return sink.file.Close() }

type AppendIdentity struct {
	SegmentID     [16]byte
	RecordOrdinal uint64
	Nonce         [12]byte
	EventUUID     [16]byte
	EventDigest   [32]byte
	ExtentDigest  [32]byte
}

// AppendResult returns a buffered Late channel only on timeout. Exactly one
// fsync result will arrive. A late success never reopens this writer.
type AppendResult struct {
	Identity AppendIdentity
	Late     <-chan error
}

type Writer struct {
	mu      sync.Mutex
	segment Segment
	sink    ExtentSink
	ordinal uint64
	state   WriterState
}

func NewWriter(segment Segment, sink ExtentSink) (*Writer, error) {
	if sink == nil {
		return nil, errors.New("b5wal: nil extent sink")
	}
	return &Writer{segment: segment, sink: sink, state: WriterOpen}, nil
}

func (writer *Writer) State() WriterState {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.state
}

func (writer *Writer) Segment() Segment { return writer.segment }

func (writer *Writer) Append(ctx context.Context, record Record) (AppendResult, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	var result AppendResult
	if writer.state != WriterOpen {
		return result, ErrWriterNotOpen
	}
	ordinal := writer.ordinal
	if ordinal == ^uint64(0) {
		writer.state = WriterSealed
		return result, errors.New("b5wal: ordinal exhausted; rotation required")
	}
	record.SegmentID = writer.segment.Manifest.SegmentID
	record.RecordOrdinal = ordinal
	record.KeyID = writer.segment.Manifest.KeyID
	copy(record.Nonce[0:4], writer.segment.Manifest.NonceDomain[:])
	binary.BigEndian.PutUint64(record.Nonce[4:12], ordinal)
	extent, err := EncodeRecord(writer.segment.Key[:], record)
	if err != nil {
		return result, err
	}
	// Consume the nonce before I/O. Any write or sync ambiguity permanently
	// wedges this segment, so the ordinal can never be retried under this key.
	writer.ordinal++
	result.Identity = AppendIdentity{
		SegmentID:     record.SegmentID,
		RecordOrdinal: ordinal,
		Nonce:         record.Nonce,
		EventUUID:     record.EventUUID,
		EventDigest:   sha256.Sum256(record.CanonicalEvent),
		ExtentDigest:  sha256.Sum256(extent),
	}
	if err := writer.sink.WriteExtent(extent); err != nil {
		writer.state = WriterWedgedUnconfirmed
		return result, err
	}
	syncResult := make(chan error, 1)
	go func() { syncResult <- writer.sink.Sync() }()
	select {
	case err := <-syncResult:
		if err != nil {
			writer.state = WriterWedgedUnconfirmed
			return result, err
		}
		return result, nil
	case <-ctx.Done():
		writer.state = WriterWedgedUnconfirmed
		result.Late = syncResult
		return result, fmt.Errorf("%w: %v", ErrSyncTimeout, ctx.Err())
	}
}

func (writer *Writer) Seal() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.state == WriterSealed {
		return nil
	}
	writer.state = WriterSealed
	return writer.sink.Close()
}

// Manager creates a fresh segment on startup and supports at most one rotation
// after a wedge for each reservation. Rotation itself emits no WAL record. If
// the replacement segment wedges on the same reservation, no second rotation,
// diagnostic, or retry is allowed; remaining facts are LOST and are represented
// only by the durable result receipt plane.
type Manager struct {
	mu      sync.Mutex
	factory SegmentFactory
	owner   uint64
	newSink func(Segment) (ExtentSink, error)
	current *Writer
	retired []*Writer
	rotated map[[16]byte]bool
}

func NewManager(ctx context.Context, factory SegmentFactory, owner uint64, newSink func(Segment) (ExtentSink, error)) (*Manager, error) {
	manager := &Manager{factory: factory, owner: owner, newSink: newSink, rotated: make(map[[16]byte]bool)}
	if err := manager.createCurrent(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func (manager *Manager) createCurrent(ctx context.Context) error {
	segment, err := manager.factory.Create(ctx, manager.owner)
	if err != nil {
		return err
	}
	sink, err := manager.newSink(segment)
	if err != nil {
		return err
	}
	writer, err := NewWriter(segment, sink)
	if err != nil {
		_ = sink.Close()
		return err
	}
	manager.current = writer
	return nil
}

func (manager *Manager) Current() *Writer {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.current
}

func (manager *Manager) RotateAfterWedge(ctx context.Context, reservationID [16]byte) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.current.State() != WriterWedgedUnconfirmed {
		return errors.New("b5wal: rotation requires a wedged current writer")
	}
	if manager.rotated[reservationID] {
		return ErrRotationExhausted
	}
	old := manager.current
	manager.retired = append(manager.retired, old)
	manager.rotated[reservationID] = true
	return manager.createCurrent(ctx)
}

// ChangeOwnership seals the current writer and creates a fresh segment, key,
// nonce domain, and ordinal space. The old segment is never reopened.
func (manager *Manager) ChangeOwnership(ctx context.Context, owner uint64) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := manager.current.Seal(); err != nil {
		return err
	}
	manager.retired = append(manager.retired, manager.current)
	manager.owner = owner
	return manager.createCurrent(ctx)
}
