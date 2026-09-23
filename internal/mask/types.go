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
	// TypeGeneric identifies a general sensitive value supported by whole-value
	// hash and block algorithms.
	TypeGeneric SensitiveType = "generic"
	TypeNumber  SensitiveType = "number"
	TypeDate    SensitiveType = "date"
)

// RedactedFallback is the fail-closed output for a non-empty value whose
// representation cannot be recognized by its configured masking rule.
const RedactedFallback = "[REDACTED]"

// BlockPlaceholder is the fixed output for every non-empty value processed by
// the block algorithm.
const BlockPlaceholder = "***"

// Algorithm identifies the redaction algorithm applied to a value.
type Algorithm string

const (
	AlgoMask Algorithm = "mask"
	AlgoHash Algorithm = "hash"
	// AlgoRange buckets numeric values or truncates date values.
	AlgoRange Algorithm = "range"
	// AlgoBlock replaces every non-empty value with BlockPlaceholder.
	AlgoBlock Algorithm = "block"
)

// RangeGranularity controls date truncation for range rules.
type RangeGranularity string

const (
	RangeYear    RangeGranularity = "year"
	RangeQuarter RangeGranularity = "quarter"
	RangeMonth   RangeGranularity = "month"
)

// RangeParams configures numeric bucketing or date truncation. Pointer fields
// distinguish omitted values from explicitly supplied zero values.
type RangeParams struct {
	BucketWidth  *int64
	BucketOffset *int64
	Granularity  *RangeGranularity
}

// Rule configures redaction for one result-set column.
type Rule struct {
	Schema        string
	Table         string
	Column        string
	SensitiveType SensitiveType
	Algorithm     Algorithm
	Range         *RangeParams
}

// RedactReport records redaction activity for a result.
type RedactReport struct {
	// TouchedColumns includes columns whose values are all empty/null sentinels.
	TouchedColumns map[int]SensitiveType
	// MaskedCells is the number of non-empty cells successfully processed or
	// replaced fail-closed.
	MaskedCells       int
	HashFallbackCount int `json:"hash_fallback_count,omitempty"`
	// HashKeyVersion is present only when at least one non-empty cell was
	// successfully fingerprinted with the assembled active key.
	HashKeyVersion *int `json:"-"`
	// UnresolvedScopedColumns records scoped-rule columns that were protected
	// by the fixed block fallback because their physical source was unresolved.
	// A column is included only when at least one non-empty cell was replaced.
	UnresolvedScopedColumns map[int]SensitiveType `json:"unresolved_scoped_columns,omitempty"`
}

var (
	// ErrUnsupportedType indicates a sensitive type not supported by masking.
	ErrUnsupportedType = errors.New("unsupported sensitive type")
	// ErrUnsupportedAlgorithm indicates an algorithm not available for execution.
	ErrUnsupportedAlgorithm = errors.New("unsupported masking algorithm")
	// ErrInvalidRangeParams indicates invalid or mismatched range parameters.
	ErrInvalidRangeParams = errors.New("invalid range parameters")
	// ErrDuplicateMaskColumn indicates duplicate normalized rule columns.
	ErrDuplicateMaskColumn = errors.New("duplicate mask column")
	// ErrHashKeyRequired indicates that hash redaction has no configured key.
	ErrHashKeyRequired = errors.New("hash redaction key is required")
	// ErrHashKeyTooShort indicates that the hash key is shorter than 32 bytes.
	ErrHashKeyTooShort = errors.New("hash redaction key must be at least 32 bytes")
)
