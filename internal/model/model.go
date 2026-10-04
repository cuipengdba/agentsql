// Package model defines the frozen contracts shared by AgentSQL components.
package model

// DBDialect identifies a supported business-database SQL dialect.
type DBDialect string

const (
	DialectPostgres  DBDialect = "postgres"
	DialectMySQL     DBDialect = "mysql"
	DialectDM        DBDialect = "dm"
	DialectOracle    DBDialect = "oracle"
	DialectYashan    DBDialect = "yashan"
	DialectSQLServer DBDialect = "sqlserver"
)

// SupportedDatasourceType reports whether the runtime has a native connection
// adapter for value. Parser and write capabilities remain dialect-specific.
func SupportedDatasourceType(value string) bool {
	switch DBDialect(value) {
	case DialectPostgres, DialectMySQL, DialectDM, DialectOracle, DialectYashan, DialectSQLServer:
		return true
	default:
		return false
	}
}

// StmtType identifies a SQL statement as SELECT, INSERT, UPDATE, DELETE, DDL,
// ADMIN, or UNKNOWN.
type StmtType string

// Decision is a pipeline outcome. Deny, approve, warn, and allow are rule or
// authorization outcomes; error is reserved for execution/database failures
// and must never be emitted by the rule engine.
type Decision string

// RiskLevel ranks a result as deny (1), approve (2), warn (3), or info (4).
type RiskLevel int

const (
	// DecisionError reports a classified execution/database failure.
	DecisionError Decision = "error"
	// DecisionDeny rejects execution.
	DecisionDeny Decision = "deny"
	// DecisionApprove requires human approval and does not execute immediately.
	DecisionApprove Decision = "approve"
	// DecisionWarn allows execution while retaining a warning.
	DecisionWarn Decision = "warn"
	// DecisionAllow allows execution without a rule hit.
	DecisionAllow Decision = "allow"
)

const (
	// RiskDeny is the highest risk level.
	RiskDeny RiskLevel = 1
	// RiskApprove requires human approval.
	RiskApprove RiskLevel = 2
	// RiskWarn permits execution with a warning.
	RiskWarn RiskLevel = 3
	// RiskInfo represents an informational or allow result.
	RiskInfo RiskLevel = 4
)

// AST is the normalized SQL representation shared by parsers and guards.
type AST struct {
	Dialect            DBDialect
	RawSQL             string
	Normalized         string
	StmtType           StmtType
	IsMulti            bool
	Tables             []ObjectRef
	Columns            []string
	DirectProjections  []DirectProjectionRef
	ProjectionLineages []ProjectionLineage
	HasWhere           bool
	WhereTautology     bool
	HasLimit           bool
	HasGroupBy         bool
	IsPureAggregate    bool
	Functions          []string
	Operations         []string
	Explain            *ExplainInfo
}

// ProjectionLineage describes one SQL target-list item. Variadic items may
// occupy more than one result position; set operations retain every branch in
// Arms so a downstream redactor can make a fail-closed decision.
type ProjectionLineage struct {
	SelectIndex int
	OutputName  string
	Variadic    bool
	SetOp       string
	Arms        []LineageArm
}

// LineageArm describes one ordinary projection or one leaf of a set
// operation. PossibleRelations is restricted to relations that can actually
// contribute to this projection.
type LineageArm struct {
	Kind              LineageKind
	Operation         string
	Status            LineageStatus
	Dependencies      []ColumnDependency
	PossibleRelations []ObjectRef
}

// ColumnDependency records both the physical origin and how it influences the
// result. Relation.Alias is not part of physical identity and must be empty.
type ColumnDependency struct {
	Origin ColumnOrigin
	Role   DependencyRole
}

// ColumnOrigin identifies a physical column and the route used to reach it.
type ColumnOrigin struct {
	Relation ObjectRef
	Column   string
	Route    LineageRoute
}

// LineageKind classifies the value-shaping SQL construct.
type LineageKind string

const (
	LineageDirect      LineageKind = "direct"
	LineageTransparent LineageKind = "transparent"
	LineageComposite   LineageKind = "composite"
	LineageAggregate   LineageKind = "aggregate"
	LineageWindow      LineageKind = "window"
	LineageConstant    LineageKind = "constant"
	LineageWildcard    LineageKind = "wildcard"
	LineageOpaque      LineageKind = "opaque"
)

// LineageStatus describes how conclusively an arm was resolved.
type LineageStatus string

const (
	LineageResolved    LineageStatus = "resolved"
	LineageSourceFree  LineageStatus = "source_free"
	LineageAmbiguous   LineageStatus = "ambiguous"
	LineageOpaqueState LineageStatus = "opaque"
	LineageUnsupported LineageStatus = "unsupported"
)

// DependencyRole distinguishes value sources from side-channel and control
// inputs that also require protection.
type DependencyRole string

const (
	DependencyValue   DependencyRole = "value"
	DependencyControl DependencyRole = "control"
	DependencyGroup   DependencyRole = "group"
	DependencyOrder   DependencyRole = "order"
	DependencyFilter  DependencyRole = "filter"
)

// LineageRoute is a bit set describing transparent relation boundaries.
type LineageRoute uint8

const (
	RouteCTE LineageRoute = 1 << iota
	RouteDerived
	RouteScalarSubquery
	RouteLateral
)

// DirectProjectionRef identifies a top-level direct column projection, its
// position in the result set, and its uniquely resolved physical source.
type DirectProjectionRef struct {
	Column  string
	Offset  int
	FromEnd bool
	Source  ObjectRef
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
	Decision   Decision
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
