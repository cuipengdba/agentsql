package pipeline

import (
	"context"
	"errors"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	StageAuth         = "auth"
	StageLoad         = "load"
	StageParse        = "parse"
	StageGuardStatic  = "guard_static"
	StageGuardDynamic = "guard_dynamic"
	StageExecute      = "execute"
	StageRedact       = "redact"
	StageAudit        = "audit"
)

const (
	defaultRowLimit          = 1_000
	defaultStatementTimeout  = 5_000
	approvalIDRandomByteSize = 16
)

var (
	ErrInvalidPorts            = errors.New("invalid pipeline ports")
	ErrInvalidSecret           = errors.New("invalid pipeline secret")
	ErrInvalidOption           = errors.New("invalid pipeline option")
	ErrInvalidRequest          = errors.New("invalid pipeline request")
	ErrAuditUnavailable        = errors.New("audit unavailable")
	ErrBusinessCommitUncertain = errors.New("business commit result uncertain")
)

// Request is one protected SQL operation.
type Request struct {
	APIKey          string
	DatasourceID    string
	SQL             string
	SessionID       string
	ConversationID  *string
	MCPTool         string
	ClientIP        *string
	ModelName       *string
	ExplainOnly     bool
	RequireApproval bool
}

// Response is the complete guarded SQL outcome.
type Response struct {
	Decision       model.Decision
	Assessment     model.Assessment
	Result         *model.QueryResult
	Redact         mask.RedactReport
	ApprovalID     string
	AuditID        int64
	ErrorCode      string                             `json:"error_code,omitempty"`
	ErrorStage     string                             `json:"error_stage,omitempty"`
	ErrorMessage   string                             `json:"error_message,omitempty"`
	Suggestion     string                             `json:"suggestion,omitempty"`
	ColumnAuth     *executor.ColumnAuthorizationAudit `json:"column_auth,omitempty"`
	auditPhase     auditPhase
	relatedAuditID int64
}

type IdentityAuthenticator interface {
	Authenticate(ctx context.Context, rawKey string) (model.Agent, error)
}

type DatasourceReader interface {
	Get(ctx context.Context, id string) (model.Datasource, error)
}

type PolicyLoader interface {
	ListByAgentAndDatasource(
		ctx context.Context,
		agentID string,
		datasourceID string,
	) ([]model.Policy, error)
}

type ExecutorProvider interface {
	AuthorizedExecute(context.Context, model.Datasource, []byte, string, string) (executor.Statement, error)
}

type ApprovalWriter interface {
	Create(ctx context.Context, approval model.Approval) (model.Approval, error)
}

// ApprovalWorkflow persists a pending approval and its approve audit event.
// A shared metadata/audit store provides all-or-nothing transaction semantics.
// With separate stores, implementations write audit first; metadata failure may
// therefore leave a valid immutable orphan audit and must return no approval.
type ApprovalWorkflow interface {
	CreatePendingWithAudit(
		ctx context.Context,
		approval model.Approval,
		log model.AuditLog,
	) (model.Approval, model.AuditLog, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, log model.AuditLog) (model.AuditLog, error)
}

type RedactorBuilder interface {
	RedactorFor(ctx context.Context, datasourceID string) (mask.Redactor, error)
}

// ColumnAuthorizationSnapshot owns one protocol-3 control snapshot. Its
// policies and mask plan source were read inside that snapshot; FinalCheck is
// called only after the business transaction and durable audit have ended.
type ColumnAuthorizationSnapshot interface {
	Policies() []model.Policy
	Redactor() mask.Redactor
	RevisionDigest() string
	FinalCheck(context.Context) error
	Close() error
}

type ColumnAuthorizationController interface {
	Begin(context.Context, model.Agent, model.Datasource) (ColumnAuthorizationSnapshot, error)
}

// RuleOverrideReader reads administrator-maintained global rule overrides.
type RuleOverrideReader interface {
	List(ctx context.Context, dbType string) ([]model.Rule, error)
}

// Ports contains the seven required pipeline dependencies plus the optional
// runtime rule-override reader. A nil RuleOverrides preserves built-in rules.
type Ports struct {
	Authenticator IdentityAuthenticator
	Datasources   DatasourceReader
	Policies      PolicyLoader
	Executors     ExecutorProvider
	Approvals     ApprovalWriter
	Audit         AuditRecorder
	Redactors     RedactorBuilder
	RuleOverrides RuleOverrideReader
}

// Option configures immutable pipeline behavior at construction time.
type Option func(*pipelineOptions) error

type pipelineOptions struct {
	ruleLayers engine.RuleLayers
	observer   DecisionObserver
	demo       config.DemoConfig
	column     ColumnAuthorizationController
}

// WithColumnAuthorization enables the feature-gated S4 SELECT path. Normal
// construction omits this option, preserving the protocol-2 table pipeline.
func WithColumnAuthorization(controller ColumnAuthorizationController) Option {
	return func(options *pipelineOptions) error {
		if isNilInterface(controller) {
			return ErrInvalidOption
		}
		options.column = controller
		return nil
	}
}

// WithRuleLayers supplies global, datasource, and Agent rule overrides.
func WithRuleLayers(layers engine.RuleLayers) Option {
	return func(options *pipelineOptions) error {
		if options == nil {
			return ErrInvalidOption
		}
		options.ruleLayers = cloneRuleLayers(layers)
		return nil
	}
}

var _ ExecutorProvider = (*executor.Gateway)(nil)
