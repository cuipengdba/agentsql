package model

import "time"

// Agent is an authenticated AI database client.
type Agent struct {
	ID         string
	Name       string
	Owner      *string
	Status     string
	APIKeyHash string
	Level      string
	ExpiresAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Datasource is a protected PostgreSQL or MySQL connection target.
type Datasource struct {
	ID            string
	Name          string
	DBType        string
	Host          string
	Port          int
	Database      string
	Username      string
	PasswordEnc   string
	ConnLimit     int
	StmtTimeoutMS int
	RowLimit      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Policy grants or denies an agent access to a database object.
type Policy struct {
	ID           string
	AgentID      string
	DatasourceID string
	ObjectType   string
	ObjectName   string
	Columns      *string
	RowFilter    *string
	Action       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Rule is a persisted SQL security-rule definition.
type Rule struct {
	ID          string
	DBType      string
	Title       string
	RiskLevel   int
	PatternType string
	Definition  string
	Enabled     bool
	Builtin     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MaskRule configures redaction for one result column.
type MaskRule struct {
	ID                string
	DatasourceID      *string
	SchemaName        string
	TableName         string
	ColumnName        string
	SensitiveType     string
	Algo              string
	Enabled           bool
	RangeBucketWidth  *int64
	RangeBucketOffset *int64
	RangeGranularity  *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// AuditLog is an immutable record of one AgentSQL operation.
type AuditLog struct {
	ID             int64
	TS             time.Time
	AgentID        *string
	DatasourceID   *string
	SessionID      *string
	ConversationID *string
	MCPTool        *string
	DBType         *string
	SQLRaw         *string
	SQLNorm        *string
	StmtType       *string
	Objects        *string
	Decision       string
	RuleHits       *string
	RiskLevel      *int
	EstRows        *int64
	RowsReturned   *int
	LatencyMS      *int64
	ClientIP       *string
	ModelName      *string
	ErrorMsg       *string
	ErrorCode      *string `json:"error_code,omitempty"`
	Action         *string
	ActorType      *string
	ActorID        *string
	DetailsJSON    *string
}

// Approval is a persisted decision request associated with an audit record.
type Approval struct {
	ID        string
	AuditID   *int64
	AgentID   *string
	SQLRaw    *string
	Reason    *string
	Status    string
	Approver  *string
	DecidedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}
