package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// B5Options is deliberately zero/off by default. Tests and, after S10, the
// activation assembly must opt in explicitly. MySQL cannot be enabled in v0.4.
type B5Options struct {
	B5Sessions   bool
	B5TxPostgres bool
	B5TxMySQL    bool
	Service      B5ToolService
}

func (options B5Options) validate() error {
	if options.B5TxMySQL {
		return errors.New("b5_tx_mysql is fixed unsupported in v0.4")
	}
	if !options.B5Sessions {
		if options.B5TxPostgres {
			return errors.New("B5 dependencies require b5_sessions")
		}
		return nil
	}
	if options.Service == nil {
		return errors.New("b5_sessions requires a transaction service")
	}
	return nil
}

type B5Continuation struct {
	SessionID         string  `json:"session_id" jsonschema:"显式 AgentSQL session ID；不得传 MCP transport session ID"`
	OwnerEpoch        uint64  `json:"owner_epoch"`
	RequestID         string  `json:"request_id"`
	ContinuationProof string  `json:"continuation_proof"`
	BodyDigest        string  `json:"body_digest" jsonschema:"参与 continuation proof 的 32-byte hex digest"`
	ExpectedSeq       *uint64 `json:"expected_seq,omitempty"`
}

type B5OpenSessionInput struct {
	TenantID    string `json:"tenant_id,omitempty"`
	PrincipalID string `json:"principal_id,omitempty"`
}

type B5SessionInput struct{ B5Continuation }

type B5PlanStatement struct {
	OperationID string `json:"operation_id"`
	SQL         string `json:"sql"`
	Reason      string `json:"reason"`
}

type B5BeginInput struct {
	B5Continuation
	TransactionID      string            `json:"transaction_id"`
	DatasourceID       string            `json:"datasource_id"`
	Dialect            string            `json:"dialect"`
	ServerMajor        int               `json:"server_major"`
	KeyRevision        uint64            `json:"key_revision"`
	DatasourceRevision uint64            `json:"datasource_revision"`
	PolicyRevision     uint64            `json:"policy_revision"`
	ApprovalID         string            `json:"approval_id,omitempty"`
	Statements         []B5PlanStatement `json:"statements"`
}

type B5ExecuteInput struct {
	B5Continuation
	TransactionID string `json:"transaction_id"`
	OperationID   string `json:"operation_id"`
	Ordinal       int    `json:"ordinal"`
}

type B5FinishInput struct {
	B5Continuation
	TransactionID string `json:"transaction_id"`
}

type B5SessionView struct {
	SessionID          string           `json:"session_id"`
	ContinuationSecret string           `json:"continuation_secret,omitempty"`
	OwnerEpoch         uint64           `json:"owner_epoch"`
	Status             b5.SessionStatus `json:"status"`
	StickyRoute        string           `json:"sticky_route,omitempty"`
}

// B5ToolService is the narrow MCP-to-coordinator bridge. It does not expose a
// raw executor, database handle, transaction, or arbitrary SQL execution.
type B5ToolService interface {
	OpenSession(context.Context, string, B5OpenSessionInput) (B5SessionView, error)
	CloseSession(context.Context, string, B5SessionInput) (B5SessionView, error)
	SessionStatus(context.Context, string, B5SessionInput) (B5SessionView, error)
	Begin(context.Context, string, B5BeginInput) (b5coordinator.Result, error)
	Execute(context.Context, string, B5ExecuteInput) (b5coordinator.Result, error)
	Commit(context.Context, string, B5FinishInput) (b5coordinator.Result, error)
	Rollback(context.Context, string, B5FinishInput) (b5coordinator.Result, error)
	TransactionStatus(context.Context, string, B5FinishInput) (b5coordinator.Result, error)
	Shutdown(context.Context) error
}

func registerB5Tools(server *mcp.Server, handlers *toolHandlers, options B5Options) {
	add := func(name, description string, call func(context.Context, any) ToolResponse) {
		_ = name
		_ = description
		_ = call
	}
	_ = add // typed registrations below keep SDK schemas precise.

	mcp.AddTool(server, &mcp.Tool{Name: "open_session", Description: "创建显式 AgentSQL logical session；返回一次性 continuation secret。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5OpenSessionInput) (*mcp.CallToolResult, ToolResponse, error) {
		view, err := options.Service.OpenSession(ctx, handlers.agent.ID, input)
		response := b5SessionResponse(view, err)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "close_session", Description: "使用 continuation proof 关闭显式 AgentSQL session。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5SessionInput) (*mcp.CallToolResult, ToolResponse, error) {
		view, err := options.Service.CloseSession(ctx, handlers.agent.ID, input)
		response := b5SessionResponse(view, err)
		if err == nil {
			response.TxEffect = string(b5.EffectSessionTerminal)
		}
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_session_status", Description: "查询显式 AgentSQL session 状态；Mcp-Session-Id 仅为 transport metadata。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5SessionInput) (*mcp.CallToolResult, ToolResponse, error) {
		view, err := options.Service.SessionStatus(ctx, handlers.agent.ID, input)
		response := b5SessionResponse(view, err)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "begin_transaction", Description: "预检完整有序 DML 计划并开始 PostgreSQL transaction；不执行 statement。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5BeginInput) (*mcp.CallToolResult, ToolResponse, error) {
		if !options.B5TxPostgres || input.Dialect != "postgres" {
			response := b5ErrorResponse(b5.ErrorDialectTransactionUnsupported, errors.New("transaction dialect is disabled"))
			return protocolResult(response), response, nil
		}
		if code, err := validateB5Plan(input.Statements); err != nil {
			response := b5ErrorResponse(code, err)
			return protocolResult(response), response, nil
		}
		result, err := options.Service.Begin(ctx, handlers.agent.ID, input)
		response := b5ResultResponse(result, err)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "execute_transaction_statement", Description: "按计划 ordinal 顺序执行一条已 seal 的 DML；SQL 不能在 execute 阶段替换。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5ExecuteInput) (*mcp.CallToolResult, ToolResponse, error) {
		result, err := options.Service.Execute(ctx, handlers.agent.ID, input)
		response := b5ResultResponse(result, err)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "commit_transaction", Description: "提交已完整执行计划的 transaction，并在 disposition 后执行 final fence。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5FinishInput) (*mcp.CallToolResult, ToolResponse, error) {
		result, err := options.Service.Commit(ctx, handlers.agent.ID, input)
		response := b5ResultResponse(result, err)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "rollback_transaction", Description: "回滚 transaction，并在 disposition 后执行 final fence。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5FinishInput) (*mcp.CallToolResult, ToolResponse, error) {
		result, err := options.Service.Rollback(ctx, handlers.agent.ID, input)
		response := b5ResultResponse(result, err)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_transaction_status", Description: "幂等查询 transaction 状态与 outcome/audit/disposition 三轴。"}, func(ctx context.Context, _ *mcp.CallToolRequest, input B5FinishInput) (*mcp.CallToolResult, ToolResponse, error) {
		result, err := options.Service.TransactionStatus(ctx, handlers.agent.ID, input)
		response := b5ResultResponse(result, err)
		return protocolResult(response), response, nil
	})
}

func validateB5Plan(statements []B5PlanStatement) (b5.ErrorCode, error) {
	if len(statements) == 0 {
		return b5.ErrorTxPlanRequired, errors.New("transaction plan is empty")
	}
	sqlParser, err := parser.NewParser(model.DBDialect("postgres"))
	if err != nil {
		return b5.ErrorTxDMLShapeUnsupported, err
	}
	for _, statement := range statements {
		sql := strings.TrimSpace(statement.SQL)
		if sql == "" || sql != statement.SQL || strings.TrimSpace(statement.OperationID) == "" || strings.TrimSpace(statement.Reason) == "" {
			return b5.ErrorTxDMLShapeUnsupported, errors.New("each planned statement requires operation_id, reason, and exact SQL")
		}
		ast, parseErr := sqlParser.Parse(sql)
		if parseErr != nil || ast == nil || ast.IsMulti {
			return b5.ErrorTxControlStatementDenied, errors.New("stacked or malformed SQL is denied")
		}
		switch ast.StmtType {
		case model.StmtType("SELECT"):
			return b5.ErrorTxSelectUnsupported, errors.New("SELECT inside B5 transaction is unsupported")
		case model.StmtType("INSERT"), model.StmtType("UPDATE"), model.StmtType("DELETE"):
		default:
			return b5.ErrorTxControlStatementDenied, fmt.Errorf("statement type %s is denied", ast.StmtType)
		}
		upper := strings.ToUpper(sql)
		if containsSQLKeyword(upper, "RETURNING") {
			return b5.ErrorTxReturningUnsupported, errors.New("RETURNING is unsupported")
		}
	}
	return b5.ErrorNone, nil
}

func containsSQLKeyword(sql, keyword string) bool {
	fields := strings.FieldsFunc(sql, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	})
	for _, field := range fields {
		if field == keyword {
			return true
		}
	}
	return false
}

func b5SessionResponse(view B5SessionView, err error) ToolResponse {
	if err != nil {
		return b5ErrorResponse(b5coordinator.ErrorCode(err), err)
	}
	response := ToolResponse{Decision: "allow", Reason: "B5 session operation completed", Suggestion: "preserve the continuation secret and sign every later request", Data: view, TxEffect: string(b5.EffectNoTxChange)}
	setB5RetryContract(&response, b5.ErrorNone)
	return response
}

func b5ResultResponse(result b5coordinator.Result, err error) ToolResponse {
	code := result.Code
	if code == b5.ErrorNone && err != nil {
		code = b5coordinator.ErrorCode(err)
	}
	if code != b5.ErrorNone || err != nil {
		response := b5ErrorResponse(code, err)
		if code != b5.ErrorFinalFencePendingCommitted && code != b5.ErrorFinalFencePendingNotCommitted && code != b5.ErrorFinalFencePendingUnknown {
			response.Data = result
		}
		response.TxEffect = string(result.Effect)
		response.DBOutcome = string(result.DBOutcome)
		response.AuditDurability = string(result.AuditDurability)
		response.ConnectionDisposition = string(result.ConnectionDisposition)
		setB5RetryContract(&response, code)
		return response
	}
	response := ToolResponse{Decision: "allow", Reason: "B5 transaction operation completed", Suggestion: "continue only with the next planned operation", Data: result, TxEffect: string(result.Effect), DBOutcome: string(result.DBOutcome), AuditDurability: string(result.AuditDurability), ConnectionDisposition: string(result.ConnectionDisposition)}
	setB5RetryContract(&response, b5.ErrorNone)
	return response
}

func b5ErrorResponse(code b5.ErrorCode, err error) ToolResponse {
	if code == b5.ErrorNone {
		code = b5.ErrorTxPlanRequired
	}
	reason := string(code)
	if err != nil {
		reason = err.Error()
	}
	response := ToolResponse{Decision: "error", ErrorCode: string(code), Reason: reason, Suggestion: "do not replay writes; follow retry_same_request and tx_effect", TxEffect: string(fixedB5Effect(code))}
	setB5RetryContract(&response, code)
	return response
}

func fixedB5Effect(code b5.ErrorCode) b5.TxEffect {
	switch code {
	case b5.ErrorSessionBusy, b5.ErrorTxPlanIncomplete:
		return b5.EffectKeepActive
	case b5.ErrorSessionOwnerLost, b5.ErrorSessionExpired, b5.ErrorSessionTerminalRecordExpired:
		return b5.EffectSessionTerminal
	case b5.ErrorFinalFencePendingCommitted, b5.ErrorFinalFencePendingNotCommitted, b5.ErrorFinalFencePendingUnknown, b5.ErrorAuditEventUUIDConflict:
		return b5.EffectFenceOnly
	case b5.ErrorTxApprovalConsumedBeginFailed, b5.ErrorTxBeginManifestMismatch, b5.ErrorTxBeginOutcomeUncertainConnectionQuarantine,
		b5.ErrorTxIdleTimeout, b5.ErrorTxMaxDuration, b5.ErrorTxStatementTimeout, b5.ErrorTxOperationWatchdog,
		b5.ErrorTxCancelEmittedConnectionQuarantined, b5.ErrorTxRollbackEvidenceContradictionNoCommit,
		b5.ErrorTxRollbackUnconfirmed, b5.ErrorTxNotCommittedAuditPending,
		b5.ErrorTxNotCommittedAuditDurabilityLost, b5.ErrorTxNotCommittedConnectionQuarantined:
		return b5.EffectTerminalNotCommitted
	case b5.ErrorTxOperationWatchdogOutcomeUnknown, b5.ErrorTxCommitEvidenceContradiction,
		b5.ErrorTxRollbackEvidenceContradictionUnknown, b5.ErrorTxDBOutcomeUnknown,
		b5.ErrorTxDBOutcomeUnknownAuditPending, b5.ErrorTxDBOutcomeUnknownAuditDurabilityLost,
		b5.ErrorTxUnknownConnectionQuarantined:
		return b5.EffectTerminalUnknown
	case b5.ErrorTxCommittedAuditPending, b5.ErrorTxCommittedAuditDurabilityLost, b5.ErrorTxCommittedConnectionQuarantined:
		return b5.EffectTerminalCommitted
	default:
		return b5.EffectNoTxChange
	}
}

func setB5RetryContract(response *ToolResponse, code b5.ErrorCode) {
	if response == nil {
		return
	}
	retry := false
	newTx := false
	response.RetrySameRequest = &retry
	response.NewTransactionAllowed = &newTx
	switch code {
	case b5.ErrorSessionWrongInstance, b5.ErrorSessionRouteUnavailable, b5.ErrorSessionBusy,
		b5.ErrorSessionLimitExceeded, b5.ErrorTxLimitExceeded, b5.ErrorPlanByteLimitExceeded,
		b5.ErrorAuditBarrierUnavailableBeforeTx, b5.ErrorAuditEmergencyWALUnavailable,
		b5.ErrorFinalFencePendingCommitted, b5.ErrorFinalFencePendingNotCommitted, b5.ErrorFinalFencePendingUnknown:
		*response.RetrySameRequest = true
	}
	switch code {
	case b5.ErrorSessionLimitExceeded, b5.ErrorSessionExpired, b5.ErrorTxActivePlanMismatch,
		b5.ErrorTxPlanRequired, b5.ErrorTxPlanMismatch, b5.ErrorTxApprovalRequired,
		b5.ErrorTxApprovalExpired, b5.ErrorTxApprovalPlanMismatch, b5.ErrorTxApprovalConsumedBeginFailed,
		b5.ErrorTxBeginManifestMismatch, b5.ErrorTxBeginOutcomeUncertainConnectionQuarantine,
		b5.ErrorTxRollbackOnly, b5.ErrorTxMaskUnsupported, b5.ErrorTxSelectUnsupported,
		b5.ErrorTxReturningUnsupported, b5.ErrorTxControlStatementDenied, b5.ErrorTxDMLShapeUnsupported,
		b5.ErrorTxIdleTimeout, b5.ErrorTxMaxDuration, b5.ErrorTxStatementTimeout,
		b5.ErrorTxOperationWatchdog, b5.ErrorTxCancelEmittedConnectionQuarantined,
		b5.ErrorTxRollbackEvidenceContradictionNoCommit, b5.ErrorTxRollbackUnconfirmed,
		b5.ErrorTxLimitExceeded, b5.ErrorPlanByteLimitExceeded, b5.ErrorTxExecutionLimitExceeded,
		b5.ErrorTxNotCommittedAuditPending, b5.ErrorTxNotCommittedAuditDurabilityLost,
		b5.ErrorAuthDMLBinderRequired, b5.ErrorAuthDMLActionMissing,
		b5.ErrorAuthDMLWriteTargetGrantMissing, b5.ErrorAuthDMLReferenceGrantMissing,
		b5.ErrorAuthImplicitObjectUnclosed, b5.ErrorAuthClosureUnsupported,
		b5.ErrorAuthConstraintClosureUnsupported, b5.ErrorAuthTypeClosureUnsupported,
		b5.ErrorAuthDefaultClosureUnsupported, b5.ErrorAuthExpressionClosureUnsupported,
		b5.ErrorAuthRewriteClosureUnsupported, b5.ErrorAuthRelationKindUnsupported,
		b5.ErrorAuthInternalObjectDenied, b5.ErrorAuthWholeRowUnsupported, b5.ErrorAuthCatalogRace,
		b5.ErrorTxNotCommittedConnectionQuarantined, b5.ErrorFinalFencePendingNotCommitted:
		*response.NewTransactionAllowed = true
	}
}
