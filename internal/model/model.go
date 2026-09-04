// Package model defines the frozen contracts shared by AgentSQL components.
package model

// DBDialect identifies a supported database dialect: postgres or mysql.
type DBDialect string

// StmtType identifies a SQL statement as SELECT, INSERT, UPDATE, DELETE, DDL,
// ADMIN, or UNKNOWN.
type StmtType string

// Decision is an allow, deny, or approve pipeline result.
type Decision string

// RiskLevel ranks a result as deny (1), approve (2), warn (3), or info (4).
type RiskLevel int

// AST is the normalized SQL representation shared by parsers and guards.
type AST struct {
	Dialect        DBDialect
	RawSQL         string
	Normalized     string
	StmtType       StmtType
	IsMulti        bool
	Tables         []ObjectRef
	Columns        []string
	HasWhere       bool
	WhereTautology bool
	HasLimit       bool
	Functions      []string
	Operations     []string
	Explain        *ExplainInfo
}

// ObjectRef identifies a database object and its optional alias.
type ObjectRef struct {
	Schema string
	Table  string
	Alias  string
}

// ExplainInfo contains normalized database query-plan signals.
type ExplainInfo struct {
	EstScanRows int64
	EstCost     float64
	UsesIndex   bool
	SeqScan     bool
	Raw         string
}

// RuleContext contains the inputs available to a rule evaluation.
type RuleContext struct {
	Agent      *Agent
	Datasource *Datasource
	AST        *AST
	Policy     *PolicyDecision
}

// RuleHit records one rule result and its remediation guidance.
type RuleHit struct {
	RuleID     string
	Risk       RiskLevel
	Message    string
	Suggestion string
}

// Assessment is the complete guard decision returned by the pipeline.
type Assessment struct {
	Decision     Decision
	Risk         RiskLevel
	StmtType     StmtType
	Hits         []RuleHit
	EstScanRows  int64
	Reason       string
	Suggestion   string
	Normalized   string
	Objects      []ObjectRef
	StageLatency map[string]int64
}

// QueryResult is the bounded, string-encoded result returned to a caller.
type QueryResult struct {
	Columns   []string
	Rows      [][]string
	RowCount  int
	Truncated bool
	LatencyMS int64
}
