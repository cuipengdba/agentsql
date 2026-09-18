package discovery

import "github.com/cuipengdba/agentsql/internal/mask"

// Fixed first-release limits. Callers may expose these values but must not
// raise them at runtime.
const (
	MinTables                = 1
	MaxTables                = 20
	MaxMetadataColumns       = 500
	MaxCandidateColumns      = 50
	MaxColumnsPerSampleQuery = 10
	DefaultSampleRows        = 10
	MaxSampleRows            = 20
	DefaultSampleValues      = 500
	MaxSampleValues          = 1000
)

// TableRef is a metadata-confirmed table identity. It is deliberately not a
// SQL fragment.
type TableRef struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// ColumnRef is a metadata-confirmed column identity. It is deliberately not a
// SQL fragment.
type ColumnRef struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Column string `json:"column"`
}

// ColumnMeta is the minimum metadata needed by discovery.
type ColumnMeta struct {
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Column   string `json:"column"`
	DataType string `json:"data_type"`
	Ordinal  int    `json:"ordinal"`
}

// Category identifies a first-release discovery category.
type Category string

const (
	CategoryPhone     Category = "phone"
	CategoryEmail     Category = "email"
	CategoryIDCard    Category = "idcard"
	CategoryBankCard  Category = "bankcard"
	CategoryIP        Category = "ip"
	CategoryBirthdate Category = "birthdate"
)

// Confidence is the confidence assigned by the fixed state machine.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Signal contains only a controlled signal name and an aggregate count. It
// never contains a sample or captured text.
type Signal struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// RecommendedRule contains only combinations accepted by mask.NewRedactor.
type RecommendedRule struct {
	SensitiveType mask.SensitiveType `json:"sensitive_type"`
	Algo          mask.Algorithm     `json:"algo"`
}

// Finding is safe to return from the core: it contains counts, never raw
// sample values.
type Finding struct {
	Schema          string           `json:"schema"`
	Table           string           `json:"table"`
	Column          string           `json:"column"`
	DataType        string           `json:"data_type"`
	Category        Category         `json:"category"`
	Signals         []Signal         `json:"signals"`
	Confidence      Confidence       `json:"confidence"`
	Sampled         bool             `json:"sampled"`
	MatchedSamples  int              `json:"matched_samples"`
	EligibleSamples int              `json:"eligible_samples"`
	RecommendedRule *RecommendedRule `json:"recommended_rule"`
	Applicable      bool             `json:"applicable"`
	ExistingRule    bool             `json:"existing_rule"`
	Reason          string           `json:"reason,omitempty"`
}

// ScanRequest always names its table scope explicitly. A nil Sampling value
// means true. SampleRows zero means the default; negative values and values
// above MaxSampleRows are rejected.
type ScanRequest struct {
	Tables     []TableRef `json:"tables"`
	Sampling   *bool      `json:"sampling,omitempty"`
	SampleRows int        `json:"sample_rows,omitempty"`
	Categories []Category `json:"categories,omitempty"`
}

// Scope is the normalized effective scope used for a scan.
type Scope struct {
	DatasourceID string     `json:"datasource_id"`
	Tables       []TableRef `json:"tables"`
	Sampling     bool       `json:"sampling"`
	SampleRows   int        `json:"sample_rows"`
	Categories   []Category `json:"categories"`
}

// Stats contains only aggregate counters.
type Stats struct {
	TablesRequested    int `json:"tables_requested"`
	TablesScanned      int `json:"tables_scanned"`
	ColumnsSeen        int `json:"columns_seen"`
	CandidateColumns   int `json:"candidate_columns"`
	SampledColumns     int `json:"sampled_columns"`
	SampledValuesCount int `json:"sampled_values_count"`
	FindingsCount      int `json:"findings_count"`
}

// Limits reports the immutable core limits.
type Limits struct {
	MaxTables                int `json:"max_tables"`
	MaxMetadataColumns       int `json:"max_metadata_columns"`
	MaxCandidateColumns      int `json:"max_candidate_columns"`
	MaxColumnsPerSampleQuery int `json:"max_columns_per_sample_query"`
	DefaultSampleRows        int `json:"default_sample_rows"`
	MaxSampleRows            int `json:"max_sample_rows"`
	DefaultSampleValues      int `json:"default_sample_values"`
	MaxSampleValues          int `json:"max_sample_values"`
}

// ScanResult is the complete pure-core result.
type ScanResult struct {
	Scope    Scope     `json:"scope"`
	Stats    Stats     `json:"stats"`
	Limits   Limits    `json:"limits"`
	Findings []Finding `json:"findings"`
}

// Result is kept as a concise alias for callers that prefer it.
type Result = ScanResult

// SampleBatch is an in-process transfer object between the controlled reader
// and Scanner. Its raw values are intentionally inaccessible outside this
// package and are never copied into a Finding.
type SampleBatch struct {
	columns []ColumnRef
	rows    [][]*string
}

// NewSampleBatch copies a controlled-reader result into a short-lived batch.
// Nil cells represent SQL NULL. Structural validation is repeated by Scanner.
func NewSampleBatch(columns []ColumnRef, rows [][]*string) SampleBatch {
	batch := SampleBatch{columns: append([]ColumnRef(nil), columns...)}
	if rows == nil {
		return batch
	}
	batch.rows = make([][]*string, len(rows))
	for rowIndex, row := range rows {
		batch.rows[rowIndex] = make([]*string, len(row))
		for columnIndex, pointer := range row {
			if pointer == nil {
				continue
			}
			value := *pointer
			batch.rows[rowIndex][columnIndex] = &value
		}
	}
	return batch
}

func fixedLimits() Limits {
	return Limits{
		MaxTables:                MaxTables,
		MaxMetadataColumns:       MaxMetadataColumns,
		MaxCandidateColumns:      MaxCandidateColumns,
		MaxColumnsPerSampleQuery: MaxColumnsPerSampleQuery,
		DefaultSampleRows:        DefaultSampleRows,
		MaxSampleRows:            MaxSampleRows,
		DefaultSampleValues:      DefaultSampleValues,
		MaxSampleValues:          MaxSampleValues,
	}
}
