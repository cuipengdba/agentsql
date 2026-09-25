package b5wal

import (
	"errors"
	"sync"
)

var ErrReservationExhausted = errors.New("b5wal: reservation exhausted")

// Reservation accounts physical extents, not plaintext bytes. A normal B5
// path is created with MaxPathRecordCount and therefore exactly 120 KiB.
type Reservation struct {
	mu        sync.Mutex
	remaining int
}

func NewReservation(recordCount int) (*Reservation, error) {
	if recordCount < 0 || recordCount > MaxPathRecordCount {
		return nil, errors.New("b5wal: invalid reservation record count")
	}
	return &Reservation{remaining: recordCount * EncodedRecordCharge()}, nil
}

func (reservation *Reservation) ConsumeEncodedRecord(extent []byte) error {
	if len(extent) != EncodedRecordCharge() {
		return errors.New("b5wal: capacity must be charged with a full encoded extent")
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.remaining < len(extent) {
		return ErrReservationExhausted
	}
	reservation.remaining -= len(extent)
	return nil
}

func (reservation *Reservation) Remaining() int {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.remaining
}
