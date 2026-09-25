package b5wal

import (
	"errors"
	"testing"
)

func TestFrozenEmissionTableAndCapacity(t *testing.T) {
	if ComputedMaxPathRecordCount() != MaxPathRecordCount || MaxPathRecordCount != 6 {
		t.Fatalf("computed=%d frozen=%d", ComputedMaxPathRecordCount(), MaxPathRecordCount)
	}
	if ReservationCharge() != 122_880 {
		t.Fatalf("reservation charge = %d", ReservationCharge())
	}
	for _, path := range EmissionPaths() {
		if len(path.Records) > MaxPathRecordCount {
			t.Fatalf("path exceeds bound: %+v", path)
		}
		seen := make(map[RecordKind]bool)
		for _, record := range path.Records {
			if seen[record] {
				t.Fatalf("path emits duplicate %q: %+v", record, path)
			}
			seen[record] = true
		}
	}

	record, key := maximalRecord()
	extent, err := EncodeRecord(key, record)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := NewReservation(MaxPathRecordCount)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxPathRecordCount; index++ {
		if err := reservation.ConsumeEncodedRecord(extent); err != nil {
			t.Fatalf("record %d: %v", index+1, err)
		}
	}
	if reservation.Remaining() != 0 {
		t.Fatalf("remaining = %d", reservation.Remaining())
	}
	if err := reservation.ConsumeEncodedRecord(extent); !errors.Is(err, ErrReservationExhausted) {
		t.Fatalf("seventh record = %v", err)
	}
}
