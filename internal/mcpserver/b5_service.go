package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5session"
)

// B5CoordinatorService adapts the reviewed directory and planned coordinator
// to MCP without exposing either a raw SQL executor or a database session.
type B5CoordinatorService struct {
	Directory   *b5session.Directory
	Coordinator *b5coordinator.Coordinator
	Analyzer    b5coordinator.Analyzer
	InstanceID  string
	StickyRoute string
	FinalFence  func(context.Context, b5coordinator.Result) error

	mu              sync.Mutex
	activeBySession map[string]string
}

func (service *B5CoordinatorService) ready() error {
	if service == nil || service.Directory == nil || service.Coordinator == nil || service.Analyzer == nil || service.InstanceID == "" || service.StickyRoute == "" {
		return errors.New("B5 MCP service is unavailable")
	}
	service.mu.Lock()
	if service.activeBySession == nil {
		service.activeBySession = make(map[string]string)
	}
	service.mu.Unlock()
	return nil
}

func (service *B5CoordinatorService) OpenSession(ctx context.Context, agentID string, input B5OpenSessionInput) (B5SessionView, error) {
	if err := service.ready(); err != nil {
		return B5SessionView{}, err
	}
	// Until an external identity mapping is signed off, principal and tenant
	// are bound to the authenticated Agent. Caller-provided aliases fail closed.
	if input.PrincipalID != "" && input.PrincipalID != agentID || input.TenantID != "" && input.TenantID != agentID {
		return B5SessionView{}, &b5coordinator.SessionFailure{Code: b5.ErrorSessionNotFoundOrDenied}
	}
	created, err := service.Directory.Create(ctx, b5session.CreateSession{AgentID: agentID, TenantID: agentID, PrincipalID: agentID, OwnerInstanceID: service.InstanceID, StickyRoute: service.StickyRoute})
	if err != nil {
		return B5SessionView{}, err
	}
	return B5SessionView{SessionID: created.Session.SessionID, ContinuationSecret: created.ContinuationSecret, OwnerEpoch: created.Session.OwnerEpoch, Status: created.Session.Status, StickyRoute: created.Session.StickyRoute}, nil
}

func (service *B5CoordinatorService) CloseSession(ctx context.Context, agentID string, input B5SessionInput) (B5SessionView, error) {
	if err := service.ready(); err != nil {
		return B5SessionView{}, err
	}
	service.mu.Lock()
	_, active := service.activeBySession[input.SessionID]
	service.mu.Unlock()
	if active {
		return B5SessionView{}, &b5coordinator.Failure{Code: b5.ErrorTxAlreadyActive, Cause: errors.New("session has an active transaction")}
	}
	continuation, err := service.continuation(agentID, "close_session", input.B5Continuation, input)
	if err != nil {
		return B5SessionView{}, err
	}
	decision, err := service.Directory.Close(ctx, continuation)
	return sessionDecisionView(decision), routeError(decision, err)
}

func (service *B5CoordinatorService) SessionStatus(ctx context.Context, agentID string, input B5SessionInput) (B5SessionView, error) {
	if err := service.ready(); err != nil {
		return B5SessionView{}, err
	}
	continuation, err := service.continuation(agentID, "get_session_status", input.B5Continuation, input)
	if err != nil {
		return B5SessionView{}, err
	}
	decision, err := service.Directory.Lookup(ctx, continuation)
	return sessionDecisionView(decision), routeError(decision, err)
}

func (service *B5CoordinatorService) Begin(ctx context.Context, agentID string, input B5BeginInput) (b5coordinator.Result, error) {
	if err := service.ready(); err != nil {
		return b5coordinator.Result{}, err
	}
	continuation, err := service.authorization(agentID, "begin_transaction", input.B5Continuation, input)
	if err != nil {
		return b5coordinator.Result{}, err
	}
	statements := make([]b5coordinator.StatementRequest, len(input.Statements))
	for index, statement := range input.Statements {
		statements[index] = b5coordinator.StatementRequest{OperationID: statement.OperationID, SQL: statement.SQL, Reason: statement.Reason}
	}
	request := b5coordinator.BeginRequest{Session: continuation, TransactionID: input.TransactionID, RequestID: input.RequestID, ApprovalID: input.ApprovalID, Plan: b5coordinator.PlanRequest{
		TenantID: agentID, PrincipalID: agentID, AgentID: agentID, DatasourceID: input.DatasourceID,
		KeyRevision: input.KeyRevision, DatasourceRevision: input.DatasourceRevision, PolicyRevision: input.PolicyRevision,
		Dialect: input.Dialect, ServerMajor: input.ServerMajor, Isolation: "read_committed", BinderABI: b5coordinator.RequiredBinderABI,
		ClosurePolicy: "agentsql.b5.closure.v1", Statements: statements, Limits: b5coordinator.DefaultResourceLimits(),
	}}
	result, err := service.Coordinator.Begin(ctx, request, service.Analyzer)
	if err == nil {
		service.mu.Lock()
		service.activeBySession[input.SessionID] = input.TransactionID
		service.mu.Unlock()
	}
	return result, err
}

func (service *B5CoordinatorService) Execute(ctx context.Context, agentID string, input B5ExecuteInput) (b5coordinator.Result, error) {
	authorization, err := service.authorization(agentID, "execute_transaction_statement", input.B5Continuation, input)
	if err != nil {
		return b5coordinator.Result{}, err
	}
	return service.Coordinator.Execute(ctx, b5coordinator.ExecuteRequest{Session: authorization, TransactionID: input.TransactionID, RequestID: input.RequestID, OperationID: input.OperationID, Ordinal: input.Ordinal})
}

func (service *B5CoordinatorService) Commit(ctx context.Context, agentID string, input B5FinishInput) (b5coordinator.Result, error) {
	return service.finish(ctx, agentID, input, true)
}

func (service *B5CoordinatorService) Rollback(ctx context.Context, agentID string, input B5FinishInput) (b5coordinator.Result, error) {
	return service.finish(ctx, agentID, input, false)
}

func (service *B5CoordinatorService) finish(ctx context.Context, agentID string, input B5FinishInput, commit bool) (b5coordinator.Result, error) {
	method := "rollback_transaction"
	if commit {
		method = "commit_transaction"
	}
	authorization, err := service.authorization(agentID, method, input.B5Continuation, input)
	if err != nil {
		return b5coordinator.Result{}, err
	}
	request := b5coordinator.FinishRequest{Session: authorization, TransactionID: input.TransactionID, RequestID: input.RequestID}
	var result b5coordinator.Result
	if commit {
		result, err = service.Coordinator.Commit(ctx, request)
	} else {
		result, err = service.Coordinator.Rollback(ctx, request)
	}
	if result.Status == b5.TransactionTerminal {
		service.mu.Lock()
		delete(service.activeBySession, input.SessionID)
		service.mu.Unlock()
		if service.FinalFence != nil {
			if fenceErr := service.FinalFence(ctx, result); fenceErr != nil {
				result.Code = finalFenceCode(result.DBOutcome)
				result.Effect = b5.EffectFenceOnly
				return result, &b5coordinator.Failure{Code: result.Code, Cause: fenceErr}
			}
		}
	}
	return result, err
}

func (service *B5CoordinatorService) TransactionStatus(ctx context.Context, agentID string, input B5FinishInput) (b5coordinator.Result, error) {
	authorization, err := service.authorization(agentID, "get_transaction_status", input.B5Continuation, input)
	if err != nil {
		return b5coordinator.Result{}, err
	}
	return service.Coordinator.Status(ctx, authorization, input.TransactionID)
}

func (service *B5CoordinatorService) Shutdown(ctx context.Context) error {
	if service == nil || service.Coordinator == nil {
		return nil
	}
	return service.Coordinator.Shutdown(ctx)
}

func (service *B5CoordinatorService) authorization(agentID, method string, input B5Continuation, payload any) (b5coordinator.SessionAuthorization, error) {
	continuation, err := service.continuation(agentID, method, input, payload)
	if err != nil {
		return b5coordinator.SessionAuthorization{}, err
	}
	return b5coordinator.SessionAuthorization{SessionID: continuation.AgentSQLSessionID, PrincipalID: continuation.PrincipalID, InstanceID: continuation.InstanceID, Method: continuation.Method, RequestID: continuation.RequestID, Proof: continuation.Proof, OwnerEpoch: continuation.OwnerEpoch, ExpectedSeq: continuation.ExpectedSeq, BodyDigest: continuation.BodyDigest}, nil
}

func (service *B5CoordinatorService) continuation(agentID, method string, input B5Continuation, payload any) (b5session.ContinuationInput, error) {
	if err := service.ready(); err != nil {
		return b5session.ContinuationInput{}, err
	}
	provided, err := hex.DecodeString(input.BodyDigest)
	expected, digestErr := B5BodyDigest(method, payload)
	if err != nil || digestErr != nil || len(provided) != 32 || subtle.ConstantTimeCompare(provided, expected[:]) != 1 {
		return b5session.ContinuationInput{}, &b5coordinator.SessionFailure{Code: b5.ErrorSessionProofRequired}
	}
	return b5session.ContinuationInput{AgentSQLSessionID: input.SessionID, PrincipalID: agentID, InstanceID: service.InstanceID, Method: method, RequestID: input.RequestID, OwnerEpoch: input.OwnerEpoch, ExpectedSeq: input.ExpectedSeq, BodyDigest: expected, Proof: input.ContinuationProof}, nil
}

// B5BodyDigest returns the canonical digest signed by the continuation proof.
// Proof and claimed digest fields are blanked before encoding, so neither can
// self-authenticate. The schema and method are domain-separated.
func B5BodyDigest(method string, payload any) ([32]byte, error) {
	var canonical any
	switch input := payload.(type) {
	case B5SessionInput:
		input.ContinuationProof, input.BodyDigest = "", ""
		canonical = input
	case B5BeginInput:
		input.ContinuationProof, input.BodyDigest = "", ""
		canonical = input
	case B5ExecuteInput:
		input.ContinuationProof, input.BodyDigest = "", ""
		canonical = input
	case B5FinishInput:
		input.ContinuationProof, input.BodyDigest = "", ""
		canonical = input
	default:
		return [32]byte{}, errors.New("unsupported B5 digest payload")
	}
	wire, err := json.Marshal(struct {
		Schema    string `json:"schema"`
		Method    string `json:"method"`
		Arguments any    `json:"arguments"`
	}{Schema: "agentsql.b5.mcp-body.v1", Method: method, Arguments: canonical})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(wire), nil
}

func sessionDecisionView(decision b5session.RouteDecision) B5SessionView {
	return B5SessionView{SessionID: decision.Session.SessionID, OwnerEpoch: decision.Session.OwnerEpoch, Status: decision.Session.Status, StickyRoute: decision.StickyRoute}
}

func routeError(decision b5session.RouteDecision, err error) error {
	if err != nil {
		return err
	}
	if !decision.Authorized {
		return &b5coordinator.SessionFailure{Code: decision.Code}
	}
	return nil
}

func finalFenceCode(outcome b5.DBOutcome) b5.ErrorCode {
	switch outcome {
	case b5.OutcomeCommitted:
		return b5.ErrorFinalFencePendingCommitted
	case b5.OutcomeNotCommitted:
		return b5.ErrorFinalFencePendingNotCommitted
	default:
		return b5.ErrorFinalFencePendingUnknown
	}
}

var _ B5ToolService = (*B5CoordinatorService)(nil)
