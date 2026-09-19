package mask

import "errors"

// SensitiveType identifies the kind of sensitive value in a result column.
type SensitiveType string

const (
	TypePhone SensitiveType = "phone"
	TypeEmail SensitiveType = "email"
	// TypeIDCard masks recognized 15-digit and 18-digit identity-card text.
	TypeIDCard SensitiveType = "idcard"
	// TypeBankCard masks recognized 13-to-19-digit payment-card text.
	TypeBankCard SensitiveType = "bankcard"
	// TypeIP masks recognized IPv4, IPv6, inet, and CIDR text.
	TypeIP SensitiveType = "ip"
	// TypeBirthDate masks recognized calendar dates and supported timestamps.
	TypeBirthDate SensitiveType = "birthdate"
	// TypeGeneric identifies a general sensitive value supported by hashing.
	TypeGeneric SensitiveType = "generic"
)

// RedactedFallback is the fail-closed output for a non-empty value whose
// representation cannot be recognized by its configured masking rule.
const RedactedFallback = "[REDACTED]"

// Algorithm identifies the redaction algorithm applied to a value.
type Algorithm string

const (
	AlgoMask Algorithm = "mask"
	AlgoHash Algorithm = "hash"
	// These algorithms are reserved capabilities and are not executable.
	AlgoRange Algorithm = "range"
	AlgoBlock Algorithm = "block"
)

// Rule configures redaction for one result-set column.
type Rule struct {
	Column        string
	SensitiveType SensitiveType
	Algorithm     Algorithm
}

// RedactReport records which result columns were matched and how many cells
// actually changed. TouchedColumns includes columns whose values are all
// empty/null sentinels.
type RedactReport struct {
	TouchedColumns map[int]SensitiveType
	MaskedCells    int
}

var (
	// ErrUnsupportedType indicates a sensitive type not supported by masking.
	ErrUnsupportedType = errors.New("unsupported sensitive type")
	// ErrUnsupportedAlgorithm indicates an algorithm not available for execution.
	ErrUnsupportedAlgorithm = errors.New("unsupported masking algorithm")
	// ErrDuplicateMaskColumn indicates duplicate normalized rule columns.
	ErrDuplicateMaskColumn = errors.New("duplicate mask column")
	// ErrHashKeyRequired indicates that hash redaction has no configured key.
	ErrHashKeyRequired = errors.New("hash redaction key is required")
	// ErrHashKeyTooShort indicates that the hash key is shorter than 32 bytes.
	ErrHashKeyTooShort = errors.New("hash redaction key must be at least 32 bytes")
)
