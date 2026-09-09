package mask

import "errors"

// SensitiveType identifies the kind of sensitive value in a result column.
type SensitiveType string

const (
	TypePhone SensitiveType = "phone"
	TypeEmail SensitiveType = "email"
	// Reserved for a future release. NewRedactor rejects these in v0.1.
	TypeIDCard   SensitiveType = "idcard"
	TypeBankCard SensitiveType = "bankcard"
)

// Algorithm identifies the redaction algorithm applied to a value.
type Algorithm string

const (
	AlgoMask Algorithm = "mask"
	// Reserved for a future release. NewRedactor rejects these in v0.1.
	AlgoHash  Algorithm = "hash"
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
	// ErrUnsupportedType indicates a sensitive type not implemented in v0.1.
	ErrUnsupportedType = errors.New("unsupported sensitive type")
	// ErrUnsupportedAlgorithm indicates an algorithm not implemented in v0.1.
	ErrUnsupportedAlgorithm = errors.New("unsupported masking algorithm")
	// ErrDuplicateMaskColumn indicates duplicate normalized rule columns.
	ErrDuplicateMaskColumn = errors.New("duplicate mask column")
)
