package auditchain

import (
	"fmt"
	"time"
)

const (
	DomainTag            = "agentsql-audit-chain-v1"
	GenesisASCII         = "AGENTSQL_AUDIT_CHAIN_GENESIS_V1"
	GenesisPrevHex       = "1fedc65006f788f0d939bfb0fa153df1576be6daf8e4f447f0c6e895eea573d4"
	AlgorithmSHA256      = "sha256"
	AlgorithmHMACSHA256  = "hmac-sha256"
	ChainManagement      = "management"
	ChainTraffic         = "traffic"
	ChainFormatVersionV1 = int64(1)
	TimestampLayout      = "2006-01-02T15:04:05.000000Z"
)

// Envelope contains the nine frozen E2 fields. PrevHash has a compile-time
// fixed width so it is always encoded as exactly 32 raw bytes.
type Envelope struct {
	DomainTag     string
	InstanceID    string
	ChainID       string
	FormatVersion int64
	AlgorithmMode string
	KeyVersion    int64
	ChainSeq      int64
	PrevHash      [32]byte
	Canonical     []byte
}

// NewEnvelope builds a V1 envelope using the fixed domain tag and format.
func NewEnvelope(instanceID, chainID, algorithmMode string, keyVersion, chainSeq int64, prevHash [32]byte, canonical []byte) Envelope {
	return Envelope{
		DomainTag:     DomainTag,
		InstanceID:    instanceID,
		ChainID:       chainID,
		FormatVersion: ChainFormatVersionV1,
		AlgorithmMode: algorithmMode,
		KeyVersion:    keyVersion,
		ChainSeq:      chainSeq,
		PrevHash:      prevHash,
		Canonical:     canonical,
	}
}

// EncodeEnvelope validates and serializes all nine E2 fields in frozen order.
func EncodeEnvelope(envelope Envelope) ([]byte, error) {
	if envelope.DomainTag != DomainTag {
		return nil, fmt.Errorf("domain tag must be %q", DomainTag)
	}
	if !validLowerUUID(envelope.InstanceID) {
		return nil, fmt.Errorf("instance ID must be 36-byte lowercase UUID text")
	}
	if envelope.ChainID != ChainManagement && envelope.ChainID != ChainTraffic {
		return nil, fmt.Errorf("unknown chain ID %q", envelope.ChainID)
	}
	if envelope.FormatVersion != ChainFormatVersionV1 {
		return nil, fmt.Errorf("format version must be %d", ChainFormatVersionV1)
	}
	switch envelope.AlgorithmMode {
	case AlgorithmSHA256:
		if envelope.KeyVersion != 0 {
			return nil, fmt.Errorf("sha256 key version must be 0")
		}
	case AlgorithmHMACSHA256:
		if envelope.KeyVersion < 1 {
			return nil, fmt.Errorf("hmac-sha256 key version must be positive")
		}
	default:
		return nil, fmt.Errorf("unknown algorithm mode %q", envelope.AlgorithmMode)
	}
	if envelope.ChainSeq < 1 {
		return nil, fmt.Errorf("chain sequence must be positive")
	}

	return EncodeTLV(
		textItem(envelope.DomainTag),
		textItem(envelope.InstanceID),
		textItem(envelope.ChainID),
		intItem(envelope.FormatVersion),
		textItem(envelope.AlgorithmMode),
		intItem(envelope.KeyVersion),
		intItem(envelope.ChainSeq),
		bytesItem(envelope.PrevHash[:]),
		bytesItem(envelope.Canonical),
	)
}

// ValidateTimestamp checks the fixed-width UTC microsecond format required by
// E4. Encoding remains byte-preserving and does not call this function.
func ValidateTimestamp(value string) error {
	if len(value) != len(TimestampLayout) {
		return fmt.Errorf("timestamp must use UTC with exactly six fractional digits")
	}
	if _, err := time.Parse(TimestampLayout, value); err != nil {
		return fmt.Errorf("timestamp must match %s: %w", TimestampLayout, err)
	}
	return nil
}

func validLowerUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
			continue
		}
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f')) {
			return false
		}
	}
	return true
}
