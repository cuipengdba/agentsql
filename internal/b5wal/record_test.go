package b5wal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

func maximalRecord() (Record, []byte) {
	key := bytes.Repeat([]byte{0x42}, 32)
	var segmentID, reservationID, eventUUID [16]byte
	for index := range segmentID {
		segmentID[index] = byte(index + 1)
		reservationID[index] = byte(index + 17)
		eventUUID[index] = byte(index + 33)
	}
	var nonce [12]byte
	copy(nonce[:], bytes.Repeat([]byte{0x55}, len(nonce)))
	return Record{
		SegmentID:          segmentID,
		RecordOrdinal:      99,
		ReservationID:      reservationID,
		EventUUID:          eventUUID,
		EventSchemaID:      string(bytes.Repeat([]byte{'s'}, MaxEventSchemaIDSize)),
		EventSchemaVersion: 4,
		KeyID:              string(bytes.Repeat([]byte{'k'}, MaxKeyIDSize)),
		Nonce:              nonce,
		CanonicalEvent:     bytes.Repeat([]byte{0xab}, MaxCanonicalEventSize),
		Extensions:         []Extension{{Tag: 1, Value: bytes.Repeat([]byte{0xcc}, MaxHeaderExtension-4)}},
	}, key
}

func TestMaximumRecordGoldenCharge(t *testing.T) {
	record, key := maximalRecord()
	extent, err := EncodeRecord(key, record)
	if err != nil {
		t.Fatal(err)
	}
	const independentlyAccountedFrame = 512 + 16_384 + 16 + 32
	const independentlyAlignedCharge = 20_480
	if maxFrameSize() != independentlyAccountedFrame {
		t.Fatalf("frame account = %d", maxFrameSize())
	}
	if EncodedRecordCharge() != independentlyAlignedCharge || len(extent) != independentlyAlignedCharge {
		t.Fatalf("charge=%d encoded=%d", EncodedRecordCharge(), len(extent))
	}
	accountingOffset := len(extent) - AccountingSize
	if got := accountingOffset - independentlyAccountedFrame; got != 3_024 {
		t.Fatalf("zero padding before in-extent accounting = %d", got)
	}
	if !bytes.Equal(extent[accountingOffset:accountingOffset+8], accountingMagic[:]) {
		t.Fatal("accounting metadata is not physically present in the extent")
	}
	decoded, err := DecodeRecord(key, extent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.CanonicalEvent, record.CanonicalEvent) || decoded.FrameLength != independentlyAccountedFrame || decoded.ChargedBytes != independentlyAlignedCharge {
		t.Fatalf("decoded maximum record mismatch: %+v", decoded)
	}
	if decoded.EventPayloadDigest != sha256.Sum256(record.CanonicalEvent) {
		t.Fatal("payload digest mismatch")
	}
}

func TestRecordRejectsCorruptionAndNonCanonicalFields(t *testing.T) {
	record, key := maximalRecord()
	extent, err := EncodeRecord(key, record)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{"torn", func(value []byte) { value[maxFrameSize()-1] = 0 }},
		{"ciphertext", func(value []byte) { value[RecordHeaderSize] ^= 1 }},
		{"tag", func(value []byte) { value[RecordHeaderSize+MaxCanonicalEventSize] ^= 1 }},
		{"commit marker", func(value []byte) { value[maxFrameSize()-1] ^= 1 }},
		{"padding", func(value []byte) { value[maxFrameSize()] = 1 }},
		{"accounting", func(value []byte) { value[len(value)-1] = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := append([]byte(nil), extent...)
			test.mutate(corrupt)
			if _, err := DecodeRecord(key, corrupt); err == nil {
				t.Fatal("corrupt record accepted")
			}
		})
	}

	record.EventSchemaID = string(bytes.Repeat([]byte{'x'}, MaxEventSchemaIDSize+1))
	if _, err := EncodeRecord(key, record); !errors.Is(err, ErrNonCanonicalField) {
		t.Fatalf("oversized schema error = %v", err)
	}
	record, key = maximalRecord()
	record.KeyID = "contains space"
	if _, err := EncodeRecord(key, record); !errors.Is(err, ErrNonCanonicalField) {
		t.Fatalf("non-ASCII-contract key error = %v", err)
	}
	record, key = maximalRecord()
	record.Extensions = []Extension{{Tag: 2}, {Tag: 1}}
	if _, err := EncodeRecord(key, record); !errors.Is(err, ErrNonCanonicalField) {
		t.Fatalf("unordered extensions error = %v", err)
	}
}

func TestWrongKeyAndAADFailAuthentication(t *testing.T) {
	record, key := maximalRecord()
	extent, err := EncodeRecord(key, record)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := bytes.Repeat([]byte{0x43}, 32)
	if _, err := DecodeRecord(wrongKey, extent); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong key error = %v", err)
	}
	// Change an authenticated header byte and repair CRC/accounting enough to
	// ensure AEAD, rather than CRC, is the rejecting layer.
	tampered := append([]byte(nil), extent...)
	tampered[64] ^= 1
	tagOffset := RecordHeaderSize + MaxCanonicalEventSize
	crc := crc32c(tampered[:RecordHeaderSize], tampered[RecordHeaderSize:tagOffset], tampered[tagOffset:tagOffset+AEADTagSize])
	binary.BigEndian.PutUint32(tampered[tagOffset+AEADTagSize:tagOffset+AEADTagSize+4], crc)
	accountingOffset := len(tampered) - AccountingSize
	copy(tampered[accountingOffset+96:accountingOffset+128], sha256Sum(tampered[:maxFrameSize()]))
	binary.BigEndian.PutUint32(tampered[accountingOffset+20:accountingOffset+24], accountingCRC(tampered[accountingOffset:]))
	if _, err := DecodeRecord(key, tampered); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("AAD tamper error = %v", err)
	}
}

func crc32c(parts ...[]byte) uint32 {
	hash := crc32.New(crc32cTable)
	for _, part := range parts {
		_, _ = hash.Write(part)
	}
	return hash.Sum32()
}

func sha256Sum(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}
