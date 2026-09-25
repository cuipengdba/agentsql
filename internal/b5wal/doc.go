// Package b5wal contains the feature-off B5 emergency-WAL contracts and the
// S7b production persistence/service boundary. No MCP or HTTP entry point
// imports this package; the future S6 coordinator is its intended caller.
//
// This is deliberately not the auditchain V1 canonical encoder. Callers pass
// already-canonical agentsql.audit.event.v4 bytes and the encoder binds those
// bytes, their schema identifier, and their digest into an encrypted record.
// The event-v4 and PostgreSQL receipt schemas are the frozen S1b definitions.
package b5wal

const (
	RecordAlignment       = 4096
	RecordHeaderSize      = 512
	MaxCanonicalEventSize = 16 * 1024
	AEADTagSize           = 16 // stored separately from ciphertext
	RecordTrailerSize     = 32
	AccountingSize        = 512 // physically stored inside the same extent

	MaxEventSchemaIDSize = 64
	MaxKeyIDSize         = 96
	MaxHeaderExtension   = 212

	MaxPathRecordCount = 6
)

// EncodedRecordCharge is the maximum and actual physical extent occupied by
// every encoded record. AccountingSize is not an additional allocation: the
// accounting block occupies the final 512 bytes of the record's aligned
// extent. At the maximum input size the byte account is:
//
//	512 header + 16,384 ciphertext + 16 tag + 32 trailer = 16,944 frame bytes
//	align_up(16,944, 4,096) = 20,480 extent bytes
//	the last 512 bytes are accounting metadata; 3,024 bytes remain zero padding
func EncodedRecordCharge() int { return alignUp(maxFrameSize(), RecordAlignment) }

// ReservationCharge is the maximum physical WAL reservation for one path.
func ReservationCharge() int { return MaxPathRecordCount * EncodedRecordCharge() }

func maxFrameSize() int {
	return RecordHeaderSize + MaxCanonicalEventSize + AEADTagSize + RecordTrailerSize
}

func alignUp(n, alignment int) int {
	return (n + alignment - 1) / alignment * alignment
}
