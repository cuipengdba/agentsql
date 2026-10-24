package model

import "time"

// Tenant is an isolation boundary for control-plane identities and RBAC data.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// User is a human control-plane identity. PasswordHash is never serialized by
// the HTTP layer and contains only a slow password hash, never plaintext.
type User struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	Username        string    `json:"username"`
	DisplayName     string    `json:"display_name"`
	PasswordHash    string    `json:"-"`
	Status          string    `json:"status"`
	AuthProvider    string    `json:"auth_provider"`
	ExternalSubject *string   `json:"external_subject,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Role groups permissions inside exactly one tenant.
type Role struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Builtin     bool      `json:"builtin"`
	ParentIDs   []string  `json:"parent_role_ids"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Permission is a stable, globally-defined action code assignable to roles.
type Permission struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Agent is an authenticated AI database client.
type Agent struct {
	ID         string
	TenantID   string
	Name       string
	Owner      *string
	Status     string
	APIKeyHash string
	Level      string
	ExpiresAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// TaskGrantID is set only on an Agent authenticated with a temporary task
	// key. It is never persisted as an Agent field.
	TaskGrantID string
	// TaskGrantIssuerUserID is bound from the authenticated task key before
	// SQL validation so denied attempts retain their issuer in traffic audit.
	// It is runtime-only and is never serialized or persisted as Agent data.
	TaskGrantIssuerUserID string `json:"-"`
}

// TaskReadGrant narrows one Agent to one datasource and one exact SELECT text.
// SQLSHA256 and KeyHash are digests; neither SQL nor the key is persisted.
type TaskReadGrant struct {
	ID           string
	TenantID     string
	IssuerUserID string
	AgentID      string
	DatasourceID string
	SQLSHA256    string
	KeyHash      string
	MaxUses      *int64
	UsedCount    int64
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	CreatedAt    time.Time
}

// TaskReadGrantRequest is the approval record for one exact-query grant. The
// SQL is encrypted for authorized reviewer inspection; the execution key is
// stored only as a digest on the issued grant.
type TaskReadGrantRequest struct {
	ID              string
	TenantID        string
	RequesterUserID string
	AgentID         string
	DatasourceID    string
	SQLSHA256       string
	SQLCiphertext   string
	RequestDigest   string
	IdempotencyKey  string
	MaxUses         *int64
	ExpiresAt       time.Time
	Status          string
	ReviewerUserID  *string
	ReviewedAt      *time.Time
	IssuedAt        *time.Time
	GrantID         *string
	GrantRevokedAt  *time.Time
	CreatedAt       time.Time
}

// Datasource is a protected business-database connection target. SQL Server
// normalizes an empty TLSMode to strict. PostgreSQL supports an explicit
// verify-full mode with a CA file; an empty mode keeps the legacy connection
// behavior for existing datasources and must not be described as verified TLS.
type Datasource struct {
	ID            string
	TenantID      string
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
	TLSMode       string
	TLSServerName string
	TLSCAFile     string
	// ReadControlJSON holds bounded, administrator-approved native read topology
	// and index metadata. It must never contain credentials.
	ReadControlJSON string
	// TrustServerCertificate is only accepted with SQL Server verify-full mode
	// for controlled development environments. It must remain false in
	// production because it disables server identity verification.
	TrustServerCertificate bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Policy grants or denies an agent access to a database object.
type Policy struct {
	ID                    string
	TenantID              string
	AgentID               string
	DatasourceID          string
	ObjectType            string
	ObjectName            string
	Columns               *string
	RowFilter             *string
	Action                string
	RelationBindingID     *string
	Revision              int64
	LegacyUnrepresentable bool
	RelationBinding       *RelationPolicyBinding
	ColumnPermissions     []PolicyColumnPermission
	ColumnStaging         []PolicyColumnPermissionStaging
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// RelationPolicyBinding is the control-plane identity selected for a policy.
// S1 permits staging identities; only a later slice may mark them healthy.
type RelationPolicyBinding struct {
	ID                 string
	TenantID           string
	PolicyID           string
	DatasourceID       string
	SchemaName         string
	RelationName       string
	StableObjectID     *string
	CatalogFingerprint *string
	Status             string
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// PolicyColumnPermission is an ordinal-bound output/reference grant. An
// ordinal is never synthesized from a legacy CSV token.
type PolicyColumnPermission struct {
	PolicyID             string
	TenantID             string
	RelationEnrollmentID string
	ColumnOrdinal        int
	ColumnName           string
	ColumnTypeDigest     string
	Usage                string
	ParentRevision       int64
}

// PolicyColumnPermissionStaging retains an untrusted legacy token until S3
// can bind it under the control-to-business two-phase protocol.
type PolicyColumnPermissionStaging struct {
	PolicyID       string
	TenantID       string
	TokenOrdinal   int
	LegacyToken    string
	RequestedUsage string
	SourceCSVHash  string
	BindStatus     string
	ErrorCode      *string
}

// Rule is a persisted SQL security-rule definition.
type Rule struct {
	ID          string
	TenantID    string
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
	TenantID          string
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
	TenantID       string `json:"tenant_id,omitempty"`
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
	EventUUID      *string
}

// Approval is a persisted decision request associated with an audit record.
type Approval struct {
	ID                    string
	TenantID              string
	RequesterUserID       *string
	AuditID               *int64
	AgentID               *string
	DatasourceID          *string
	DatasourceFingerprint *string
	SQLRaw                *string
	SQLNorm               *string
	Reason                *string
	RequestReason         *string
	DecisionComment       *string
	Seal                  *string
	Status                string
	Approver              *string
	DecidedAt             *time.Time
	ExpiresAt             *time.Time
	ClaimedAt             *time.Time
	FinishedAt            *time.Time
	ExecutionAuditID      *int64
	OutcomeCode           *string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}
