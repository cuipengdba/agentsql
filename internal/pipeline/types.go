package pipeline

import (
	"context"
	"errors"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/executor"
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
	ErrInvalidPorts   = errors.New("invalid pipeline ports")
	ErrInvalidSecret  = errors.New("invalid pipeline secret")
	ErrInvalidOption  = errors.New("invalid pipeline option")
	ErrInvalidRequest = errors.New("invalid pipeline request")
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
	Decision   model.Decision
	Assessment model.Assessment
	Result     *model.QueryResult
	Redact     mask.RedactReport
	ApprovalID string
	AuditID    int64
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
	GetOrOpen(datasource model.Datasource, secret []byte) (executor.Executor, error)
}

type ApprovalWriter interface {
	Create(ctx context.Context, approval model.Approval) (model.Approval, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, log model.AuditLog) (model.AuditLog, error)
}

type RedactorBuilder interface {
	RedactorFor(ctx context.Context, datasourceID string) (mask.Redactor, error)
}

// Ports contains exactly the external dependencies used by Pipeline.
type Ports struct {
	Authenticator IdentityAuthenticator
	Datasources   DatasourceReader
	Policies      PolicyLoader
	Executors     ExecutorProvider
	Approvals     ApprovalWriter
	Audit         AuditRecorder
	Redactors     RedactorBuilder
}

// Option configures immutable pipeline behavior at construction time.
type Option func(*pipelineOptions) error

type pipelineOptions struct {
	ruleLayers engine.RuleLayers
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

var _ ExecutorProvider = (*executor.Manager)(nil)
