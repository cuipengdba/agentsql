package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/rules"
)

// Pipeline is the immutable coordinator for one protected SQL flow.
type Pipeline struct {
	ports    Ports
	secret   []byte
	resolver *policy.Resolver
	engine   engine.Engine
	limiter  rules.RateLimiter
	layers   engine.RuleLayers
	observer DecisionObserver
	demo     config.DemoConfig
	column   ColumnAuthorizationController
}

// New validates all seven ports and constructs the shared guard components.
func New(ports Ports, secret []byte, options ...Option) (*Pipeline, error) {
	if err := validatePorts(ports); err != nil {
		return nil, err
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("construct pipeline: secret must contain 32 bytes: %w", ErrInvalidSecret)
	}
	configuration := pipelineOptions{}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("construct pipeline option %d: %w", index, ErrInvalidOption)
		}
		if err := option(&configuration); err != nil {
			return nil, fmt.Errorf("construct pipeline option %d: %w", index, errors.Join(ErrInvalidOption, err))
		}
	}
	configuration.ruleLayers = cloneRuleLayers(configuration.ruleLayers)
	configuration.demo = cloneDemoConfig(configuration.demo)
	if err := validatePipelineRuleLayers(configuration.ruleLayers); err != nil {
		return nil, fmt.Errorf("construct pipeline rule layers: %w", errors.Join(ErrInvalidOption, err))
	}
	return &Pipeline{
		ports:    ports,
		secret:   append([]byte(nil), secret...),
		resolver: policy.NewResolver(),
		engine:   engine.Engine{},
		limiter:  rules.NewDefaultTokenBucketLimiter(),
		layers:   configuration.ruleLayers,
		observer: configuration.observer,
		demo:     configuration.demo,
		column:   configuration.column,
	}, nil
}

// Process runs auth, load, parse, two guard gates, execute, redact, and audit.
func (pipeline *Pipeline) Process(ctx context.Context, request Request) (Response, error) {
	return pipeline.process(ctx, request, runModeProduction)
}

func (pipeline *Pipeline) process(
	ctx context.Context,
	request Request,
	mode pipelineRunMode,
) (Response, error) {
	if pipeline == nil {
		return Response{}, fmt.Errorf("process SQL request: %w", ErrInvalidRequest)
	}
	run := newPipelineRun(pipeline, request, mode)
	if ctx == nil {
		return run.finish(
			context.Background(),
			fmt.Errorf("process SQL request with nil context: %w", ErrInvalidRequest),
		)
	}
	requestContext, cancelRequest := executor.WithRequestDeadline(ctx, executor.DefaultRequestWall)
	defer cancelRequest()
	ctx = requestContext

	if err := run.measure(StageAuth, func() error {
		agent, err := pipeline.ports.Authenticator.Authenticate(ctx, request.APIKey)
		if err != nil {
			return err
		}
		run.agent = &agent
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}

	if err := run.measure(StageLoad, func() error {
		datasource, err := pipeline.ports.Datasources.Get(ctx, request.DatasourceID)
		if err != nil {
			return err
		}
		if datasource.ID != request.DatasourceID {
			return fmt.Errorf("datasource reader returned ID %q for %q", datasource.ID, request.DatasourceID)
		}
		if datasource.DBType != "postgres" && datasource.DBType != "mysql" {
			return fmt.Errorf("datasource %q has unsupported dialect %q", datasource.ID, datasource.DBType)
		}
		run.datasource = &datasource
		if run.isDemo() {
			if hit, denied := run.demoScopeDeny(); denied {
				run.appendStructuralHit(hit)
				return nil
			}
		}
		if !isNilInterface(pipeline.ports.RuleOverrides) {
			storedRules, err := pipeline.ports.RuleOverrides.List(ctx, "")
			if err != nil {
				return fmt.Errorf("load rule overrides: %w", err)
			}
			run.ruleLayer = ruleOverridesToLayer(storedRules, datasource.DBType)
		}
		storedPolicies, err := pipeline.ports.Policies.ListByAgentAndDatasource(
			ctx,
			run.agent.ID,
			datasource.ID,
		)
		if err != nil {
			return err
		}
		if err := validateLoadedPolicies(storedPolicies, run.agent.ID, datasource.ID); err != nil {
			return err
		}
		run.storedPolicies = append([]model.Policy(nil), storedPolicies...)
		decision, err := pipeline.resolver.Resolve(storedPolicies, run.agent.Level)
		if err != nil {
			return err
		}
		run.policy = decision
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}
	if run.response.Decision == model.DecisionDeny {
		return run.finish(ctx, nil)
	}
	if err := executor.ValidatePreParse(request.SQL, nil, executor.DefaultLimits); err != nil {
		return run.finish(ctx, err)
	}

	parseFailure := false
	if err := run.measure(StageParse, func() error {
		sqlParser, err := parser.NewParser(model.DBDialect(run.datasource.DBType))
		if err != nil {
			return err
		}
		ast, err := sqlParser.Parse(request.SQL)
		run.ast = ast
		if err != nil {
			parseFailure = true
			if run.isDemo() && ast != nil && ast.IsMulti {
				if ast.StmtType == "" {
					ast.StmtType = model.StmtType("UNKNOWN")
				}
				return nil
			}
			return err
		}
		if ast == nil {
			return fmt.Errorf("parser returned nil AST")
		}
		if err := executor.NewBudget(executor.DefaultLimits).ChargeAST(ast); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if run.isDemo() {
			if run.ast == nil {
				run.ast = &model.AST{
					Dialect:  model.DBDialect(run.datasource.DBType),
					RawSQL:   request.SQL,
					StmtType: model.StmtType("UNKNOWN"),
				}
			} else if run.ast.StmtType == "" {
				run.ast.StmtType = model.StmtType("UNKNOWN")
			}
			run.appendStructuralHit(demoParseHit(err))
			return run.finish(ctx, nil)
		}
		if parseFailure {
			return run.finish(ctx, executor.NewDBError(
				executor.DBErrorKindSyntax,
				executor.DBErrorCodeSyntax,
				executor.DBStageParse,
			))
		}
		return run.finish(ctx, err)
	}

	if err := run.measure(StageGuardStatic, func() error {
		allRules, err := assembleRules(run.ast.Dialect, run.reservation, panicMetadataProvider{})
		if err != nil {
			return err
		}
		staticRules, _ := splitRulesForRun(allRules, run.isDemo())
		requestLayers, err := run.requestRuleLayers()
		if err != nil {
			return err
		}
		assessment, err := pipeline.engine.Evaluate(
			run.ast,
			run.evalContext(panicMetadataProvider{}),
			staticRules,
			projectRuleLayers(requestLayers, staticRules),
		)
		run.setAssessment(assessment)
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}
	staticAssessment := run.response.Assessment
	if run.isDemo() && !isDemoSemanticReadOnly(run.ast) {
		if demoWriteApprovalEligible(run.ast) && staticAssessment.Decision != model.DecisionDeny {
			run.appendStructuralHit(demoWriteApprovalHit())
			return run.approve(ctx)
		}
		run.appendStructuralHit(demoNonSelectHit())
		staticAssessment = run.response.Assessment
	}
	bindDeniedSelect := shouldBindDeniedSelect(run.ast, staticAssessment)
	// All static denials except an isolated SELECT R010 remain terminal. R010
	// is allowed through a prepare-only binder/EXPLAIN so a missing object can
	// be distinguished from an existing but unauthorized object. The static
	// denial remains authoritative and prevents execution.
	if staticAssessment.Decision == model.DecisionDeny && !bindDeniedSelect {
		return run.finish(ctx, nil)
	}
	if run.ast.StmtType == model.StmtType("SELECT") && !isNilInterface(pipeline.column) {
		if run.datasource.DBType == "mysql" && columnAuthorizationConfigured(pipeline.column) && hasB2ColumnPolicy(run.storedPolicies) {
			return run.finish(ctx, &executor.AuthError{Reason: executor.ReasonColumnAuthUnsupported})
		}
		enabled := true
		if router, ok := pipeline.column.(ColumnAuthorizationRouter); ok {
			enabled = router.ColumnAuthorizationEnabled(*run.datasource)
		}
		if enabled {
			return run.processColumnAuthorizedSelect(ctx, bindDeniedSelect)
		}
	}
	lockedContext, cancelLockedWork := executor.WithLockDeadline(ctx, executor.DefaultRequestWall)
	defer cancelLockedWork()
	ctx = lockedContext

	if err := run.measure(StageGuardDynamic, func() (stageError error) {
		dynamicContext, cancel, err := statementContext(ctx, run.datasource.StmtTimeoutMS)
		if err != nil {
			return err
		}
		defer cancel()
		openedStatement := false
		defer func() {
			if stageError == nil || !openedStatement || run.statement == nil {
				return
			}
			if closeError := run.statement.Close(); closeError != nil {
				stageError = errors.Join(stageError, closeError)
			}
			run.statement = nil
		}()

		tenant := run.agent.ID
		if run.agent.Owner != nil && strings.TrimSpace(*run.agent.Owner) != "" {
			tenant = strings.TrimSpace(*run.agent.Owner)
		}
		dynamicContext = executor.WithReservationScope(dynamicContext, run.agent.ID, tenant)
		statement, err := pipeline.ports.Executors.AuthorizedExecute(
			dynamicContext,
			*run.datasource,
			append([]byte(nil), pipeline.secret...),
			request.SQL,
			request.SessionID,
		)
		if err != nil {
			return err
		}
		if isNilInterface(statement) {
			return fmt.Errorf("authorized execution provider returned nil statement")
		}
		openedStatement = true
		if statement.Dialect() != run.datasource.DBType {
			return fmt.Errorf(
				"statement dialect %q does not match datasource %q",
				statement.Dialect(),
				run.datasource.DBType,
			)
		}
		run.statement = statement
		metadata := any(statement)
		if shouldExplain(run.ast) {
			explain, err := statement.Explain(dynamicContext)
			if err != nil {
				return err
			}
			run.ast.Explain = &explain
		}

		allRules, err := assembleRules(run.ast.Dialect, run.reservation, metadata)
		if err != nil {
			return err
		}
		_, dynamicRules := splitRulesForRun(allRules, run.isDemo())
		requestLayers, err := run.requestRuleLayers()
		if err != nil {
			return err
		}
		dynamicAssessment, err := pipeline.engine.Evaluate(
			run.ast,
			run.evalContext(metadata),
			dynamicRules,
			projectRuleLayers(requestLayers, dynamicRules),
		)
		if err != nil {
			return err
		}
		run.setAssessment(mergeAssessment(staticAssessment, dynamicAssessment))
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}
	defer run.statement.ReleaseReservation()
	barrier := classifyExecutionBarrier(run.ast)
	if barrier == barrierDeny {
		run.response.Decision = model.DecisionDeny
		run.response.Assessment.Decision = model.DecisionDeny
		run.response.Assessment.Risk = model.RiskDeny
		run.response.Assessment.Reason = "statement is not eligible for guarded execution"
		if err := run.closeUnusedSession(); err != nil {
			return run.finish(ctx, err)
		}
		return run.finish(ctx, nil)
	}
	if request.RequireApproval &&
		run.response.Decision != model.DecisionDeny &&
		run.response.Decision != model.DecisionApprove {
		run.response.Assessment.Hits = append(
			run.response.Assessment.Hits,
			model.RuleHit{
				RuleID:     "REQUEST_APPROVAL",
				Risk:       model.RiskApprove,
				Decision:   model.DecisionApprove,
				Message:    "调用方要求本次 SQL 进入人工审批",
				Suggestion: "请等待 DBA 完成审批后再继续，不要绕过审批重复提交",
			},
		)
		recomputeAssessmentDecision(&run.response.Assessment)
		run.response.Decision = run.response.Assessment.Decision
	}
	if request.ExplainOnly {
		if err := run.closeUnusedSession(); err != nil {
			return run.finish(ctx, err)
		}
		return run.finish(ctx, nil)
	}

	switch run.response.Decision {
	case model.DecisionDeny:
		if err := run.closeUnusedSession(); err != nil {
			return run.finish(ctx, err)
		}
		return run.finish(ctx, nil)
	case model.DecisionApprove:
		if err := run.closeUnusedSession(); err != nil {
			return run.finish(ctx, err)
		}
		return run.approve(ctx)
	case model.DecisionAllow, model.DecisionWarn:
	default:
		return run.finish(ctx, fmt.Errorf("unsupported pipeline decision %q", run.response.Decision))
	}

	if barrier == barrierTransactionalWrite {
		return run.executeTransactionalWrite(ctx)
	}
	if barrier == barrierPreIntent {
		return run.executeAfterIntent(ctx)
	}

	returnsRows := barrier == barrierRead
	if err := run.measure(StageExecute, func() error {
		executionContext, cancel, err := statementContext(ctx, run.datasource.StmtTimeoutMS)
		if err != nil {
			return err
		}
		defer cancel()
		var result model.QueryResult
		if returnsRows {
			rowLimit, normalizeErr := normalizedRowLimit(run.datasource.RowLimit)
			if normalizeErr != nil {
				return normalizeErr
			}
			result, err = run.statement.Query(executionContext, rowLimit)
		} else {
			result, err = run.statement.Execute(executionContext)
		}
		if err != nil {
			return err
		}
		if err := executor.ValidateResult(result, false, executor.DefaultLimits); err != nil {
			return err
		}
		run.executionResult = &result
		responseResult := result
		run.response.Result = &responseResult
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}

	if returnsRows {
		if err := run.measure(StageRedact, func() error {
			if err := validateResultRectangle(*run.response.Result); err != nil {
				return err
			}
			redactor, err := pipeline.ports.Redactors.RedactorFor(ctx, run.datasource.ID)
			if err != nil {
				return err
			}
			if isNilInterface(redactor) {
				return fmt.Errorf("redactor builder returned nil redactor")
			}
			var redacted model.QueryResult
			var report mask.RedactReport
			if lineageAware, ok := redactor.(mask.ProjectionLineageAwareRedactor); ok && len(run.ast.ProjectionLineages) > 0 {
				aligned := resolveProjectionLineages(run.ast.ProjectionLineages, run.response.Result.Columns)
				redacted, report = lineageAware.ApplyWithProjectionLineages(*run.response.Result, aligned)
			} else {
				columnSources := resolveColumnSources(run.ast.DirectProjections, len(run.response.Result.Columns))
				if relationAware, ok := redactor.(mask.RelationSourceAwareRedactor); ok {
					redacted, report = relationAware.ApplyWithColumnSources(*run.response.Result, columnSources, run.ast.Tables)
				} else if sourceAware, ok := redactor.(mask.SourceAwareRedactor); ok {
					redacted, report = sourceAware.ApplyWithSourceColumns(*run.response.Result, legacySourceNames(columnSources))
				} else {
					redacted, report = redactor.Apply(*run.response.Result)
				}
			}
			if err := executor.ValidateResult(redacted, true, executor.DefaultLimits); err != nil {
				return err
			}
			run.response.Result = &redacted
			run.response.Redact = report
			return nil
		}); err != nil {
			return run.finish(ctx, err)
		}
	}
	return run.finish(ctx, nil)
}

func (run *pipelineRun) executeTransactionalWrite(ctx context.Context) (Response, error) {
	var auditFailed bool
	if err := run.measure(StageExecute, func() error {
		executionContext, cancel, err := statementContext(ctx, run.datasource.StmtTimeoutMS)
		if err != nil {
			return err
		}
		defer cancel()
		result, err := run.statement.ExecuteTransactional(executionContext, func(candidate model.QueryResult) error {
			run.executionResult = &candidate
			if err := run.audit(ctx, string(run.response.Decision), nil, auditPhaseSingle); err != nil {
				auditFailed = true
				return ErrAuditUnavailable
			}
			return nil
		})
		if err == nil {
			run.executionResult = &result
		}
		return err
	}); err != nil {
		if auditFailed {
			return run.finish(ctx, ErrAuditUnavailable)
		}
		if errors.Is(err, executor.ErrCommitOutcomeUnknown) {
			return run.finishOutcome(ctx, ErrBusinessCommitUncertain)
		}
		return run.finish(ctx, err)
	}
	result := *run.executionResult
	run.response.Result = &result
	return run.finish(ctx, nil)
}

func (run *pipelineRun) executeAfterIntent(ctx context.Context) (Response, error) {
	if err := run.audit(ctx, string(run.response.Decision), nil, auditPhaseIntent); err != nil {
		return run.finish(ctx, ErrAuditUnavailable)
	}
	if err := run.measure(StageExecute, func() error {
		executionContext, cancel, err := statementContext(ctx, run.datasource.StmtTimeoutMS)
		if err != nil {
			return err
		}
		defer cancel()
		var result model.QueryResult
		result, err = run.statement.Execute(executionContext)
		if err != nil {
			return err
		}
		run.executionResult = &result
		responseResult := result
		run.response.Result = &responseResult
		return nil
	}); err != nil {
		return run.finishOutcome(ctx, err)
	}
	return run.finishOutcome(ctx, nil)
}

func resolveColumnSources(refs []model.DirectProjectionRef, columnCount int) []mask.ColumnSource {
	if len(refs) == 0 || columnCount <= 0 {
		return nil
	}
	sources := make([]mask.ColumnSource, columnCount)
	occupied := make([]bool, columnCount)
	for _, ref := range refs {
		if ref.Column == "" || ref.Offset < 0 || ref.Offset >= columnCount {
			return nil
		}
		index := ref.Offset
		if ref.FromEnd {
			index = columnCount - 1 - ref.Offset
		}
		if index < 0 || index >= columnCount || occupied[index] {
			return nil
		}
		occupied[index] = true
		sources[index] = mask.ColumnSource{Column: ref.Column, Source: ref.Source}
	}
	return sources
}

func legacySourceNames(sources []mask.ColumnSource) []string {
	if sources == nil {
		return nil
	}
	names := make([]string, len(sources))
	for index, source := range sources {
		names[index] = source.Column
	}
	return names
}

func (run *pipelineRun) closeUnusedSession() error {
	if run.statement == nil {
		return nil
	}
	statement := run.statement
	run.statement = nil
	if err := statement.Close(); err != nil {
		return fmt.Errorf("close non-executing authorized statement: %w", err)
	}
	return nil
}

type pipelineRun struct {
	pipeline        *Pipeline
	request         Request
	started         time.Time
	stageLatency    map[string]int64
	response        Response
	agent           *model.Agent
	datasource      *model.Datasource
	policy          *model.PolicyDecision
	ruleLayer       engine.RuleLayer
	ast             *model.AST
	statement       executor.Statement
	executionResult *model.QueryResult
	reservation     *requestLimiter
	storedPolicies  []model.Policy
	auditAttempts   int
	auditAnchorID   int64
	mode            pipelineRunMode
}

type auditPhase string

const (
	auditPhaseSingle  auditPhase = ""
	auditPhaseIntent  auditPhase = "intent"
	auditPhaseOutcome auditPhase = "outcome"
)

func newPipelineRun(pipeline *Pipeline, request Request, mode pipelineRunMode) *pipelineRun {
	stages := pipelineStageNames()
	latency := make(map[string]int64, len(stages))
	for _, stage := range stages {
		latency[stage] = 0
	}
	assessment := model.Assessment{
		Decision:     model.DecisionAllow,
		Risk:         model.RiskInfo,
		Hits:         []model.RuleHit{},
		Reason:       "无规则命中",
		StageLatency: latency,
	}
	return &pipelineRun{
		pipeline:     pipeline,
		request:      request,
		started:      time.Now(),
		stageLatency: latency,
		response: Response{
			Decision:   model.DecisionAllow,
			Assessment: assessment,
		},
		reservation: &requestLimiter{delegate: pipeline.limiter},
		mode:        mode,
	}
}

func pipelineStageNames() [8]string {
	return [8]string{
		StageAuth,
		StageLoad,
		StageParse,
		StageGuardStatic,
		StageGuardDynamic,
		StageExecute,
		StageRedact,
		StageAudit,
	}
}

func (run *pipelineRun) evalContext(metadata any) engine.EvalContext {
	return engine.EvalContext{
		AST:              run.ast,
		AgentLevel:       run.agent.Level,
		Agent:            run.agent,
		Datasource:       run.datasource,
		Policy:           run.policy,
		MetadataProvider: metadata,
	}
}

func (run *pipelineRun) setAssessment(assessment model.Assessment) {
	assessment.StageLatency = run.stageLatency
	run.response.Assessment = assessment
	run.response.Decision = assessment.Decision
}

func (run *pipelineRun) measure(stage string, operation func() error) error {
	started := time.Now()
	err := operation()
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	run.stageLatency[stage] += elapsed
	return err
}

func (run *pipelineRun) setFailure(cause error) {
	code, message, suggestion := internalFailurePresentation(cause)
	run.response.Decision = model.DecisionDeny
	run.response.Result = nil
	run.response.Redact = mask.RedactReport{}
	run.response.ApprovalID = ""
	run.response.ErrorCode = string(code)
	run.response.ErrorStage = internalFailureStage(cause)
	run.response.ErrorMessage = message
	run.response.Suggestion = suggestion
	run.response.Assessment.Decision = model.DecisionDeny
	run.response.Assessment.Risk = model.RiskDeny
	run.response.Assessment.Reason = message
	run.response.Assessment.Suggestion = suggestion
	run.response.Assessment.StageLatency = run.stageLatency
}

func (run *pipelineRun) finish(ctx context.Context, operationError error) (Response, error) {
	return run.finishWithAudit(ctx, operationError, auditPhaseSingle, false)
}

func (run *pipelineRun) finishOutcome(ctx context.Context, operationError error) (Response, error) {
	return run.finishWithAudit(ctx, operationError, auditPhaseOutcome, true)
}

func (run *pipelineRun) finishWithAudit(
	ctx context.Context,
	operationError error,
	phase auditPhase,
	forceAudit bool,
) (Response, error) {
	auditDecision := string(run.response.Decision)
	_, databaseFailure := businessDatabaseError(operationError)
	authorizationFailure := expectedAuthorizationFailure(operationError)
	if operationError != nil {
		if databaseError, ok := businessDatabaseError(operationError); ok {
			run.setDatabaseFailure(databaseError)
			auditDecision = string(model.DecisionError)
		} else {
			run.setFailure(operationError)
			if authorizationFailure {
				auditDecision = string(model.DecisionDeny)
			} else {
				auditDecision = string(model.DecisionError)
			}
		}
	}
	finalError := operationError
	if forceAudit || run.auditAttempts == 0 {
		finalError = run.audit(ctx, auditDecision, operationError, phase)
	}
	if errors.Is(operationError, ErrBusinessCommitUncertain) &&
		errors.Is(finalError, ErrAuditUnavailable) {
		// Never let an outcome-audit outage erase the commit-unknown warning:
		// callers must still see the explicit do-not-retry contract.
		run.setFailure(ErrBusinessCommitUncertain)
		finalError = errors.Join(ErrBusinessCommitUncertain, ErrAuditUnavailable)
	}
	if databaseFailure {
		if _, ok := businessDatabaseError(finalError); ok {
			finalError = nil
		}
	}
	if authorizationFailure && expectedAuthorizationFailure(finalError) {
		finalError = nil
	}
	run.observe()
	return run.response, finalError
}

func expectedAuthorizationFailure(err error) bool {
	stable := executor.StableError(err)
	if stable == nil {
		return false
	}
	switch stable.Reason {
	case executor.ReasonAgentDenied,
		executor.ReasonStatementClassDenied,
		executor.ReasonDatasourceUnsupported,
		executor.ReasonColumnAuthUnsupported,
		executor.ReasonFunctionQualification,
		executor.ReasonImplicitObject,
		executor.ReasonRelationShape,
		executor.ReasonColumnShape,
		executor.ReasonExpressionShape,
		executor.ReasonBinderModeRequired,
		executor.ReasonBinderModeUnsupported,
		executor.ReasonRelationDenied,
		executor.ReasonRelationGrantMissing,
		executor.ReasonIdentityUnproven,
		executor.ReasonColumnGrantMissing,
		executor.ReasonMaskMeetUndefined:
		return true
	default:
		return false
	}
}

func (run *pipelineRun) setDatabaseFailure(databaseError *executor.DBError) {
	message := databaseError.Error()
	suggestion := executor.Suggestion(databaseError.Code)
	run.response.Decision = model.DecisionError
	run.response.Result = nil
	run.response.Redact = mask.RedactReport{}
	run.response.ApprovalID = ""
	run.response.ErrorCode = string(databaseError.Code)
	run.response.ErrorStage = string(databaseError.Stage)
	run.response.ErrorMessage = message
	run.response.Suggestion = suggestion
	run.response.Assessment.Decision = model.DecisionError
	run.response.Assessment.Reason = message
	run.response.Assessment.Suggestion = suggestion
	run.response.Assessment.StageLatency = run.stageLatency
}

// businessDatabaseError accepts DBErrors through ordinary wrappers and the
// rule engine's classification sentinel. Any other leaf in a multi-error chain
// rejects conversion so a DBError cannot hide a session-close,
// reservation-release, or other internal failure.
func businessDatabaseError(operationError error) (*executor.DBError, bool) {
	if operationError == nil ||
		errors.Is(operationError, ErrAuditUnavailable) ||
		errors.Is(operationError, ErrBusinessCommitUncertain) {
		return nil, false
	}
	var found *executor.DBError
	valid := true
	var visit func(error)
	visit = func(current error) {
		if current == nil || !valid {
			return
		}
		if databaseError, ok := current.(*executor.DBError); ok {
			if found != nil && found != databaseError {
				valid = false
				return
			}
			found = databaseError
			return
		}
		if current == engine.ErrRuleEvaluation {
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
			return
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			visit(wrapped.Unwrap())
			return
		}
		valid = false
	}
	visit(operationError)
	if !valid || found == nil {
		return nil, false
	}
	return found, true
}

func internalFailurePresentation(cause error) (executor.DBErrorCode, string, string) {
	stable := executor.StableError(cause)
	if stable != nil && stable.Reason != executor.ReasonDatabaseFailure {
		switch stable.Reason {
		case executor.ReasonColumnAuthUnsupported:
			return executor.DBErrorCode(stable.Reason),
				"MySQL 列级授权在 v0.4 中暂不支持",
				"请改用 PostgreSQL 演示列级授权，或移除 MySQL 列级策略并使用表级保护"
		case executor.ReasonFunctionQualification:
			return executor.DBErrorCode(stable.Reason),
				"SQL 中的 PostgreSQL 内置函数必须显式使用 pg_catalog 限定",
				"请为内置函数添加 pg_catalog. 前缀，例如 SELECT pg_catalog.count(*) ..."
		case executor.ReasonRelationShape, executor.ReasonColumnShape, executor.ReasonExpressionShape,
			executor.ReasonBinderModeRequired, executor.ReasonBinderModeUnsupported:
			return executor.DBErrorCode(stable.Reason),
				"当前列级授权模式无法安全证明该查询或结果形状",
				"请缩小为 schema-qualified 普通基表上的封闭 SQL，或安装可选的 NATIVE_C_V1 加速器"
		case executor.ReasonColumnAuthUnavailable, executor.ReasonBinderIncomplete, executor.ReasonBinderCapability:
			return executor.DBErrorCode(stable.Reason),
				"PostgreSQL 列级授权当前不可用，已拒绝执行",
				"请检查 B2 健康状态、策略绑定和数据源 catalog 权限后重试"
		}
		return executor.DBErrorCode(stable.Reason),
			"请求超出安全资源边界",
			"请缩小 SQL、参数或结果规模后重试"
	}
	switch {
	case errors.Is(cause, ErrBusinessCommitUncertain):
		return executor.DBErrorCodeCommitOutcomeUnknown,
			"数据库提交结果未知",
			"请勿自动重试；请先核对业务数据与审计记录"
	case errors.Is(cause, ErrAuditUnavailable):
		return executor.DBErrorCodeAuditUnavailable,
			"审计服务不可用",
			"请联系管理员恢复审计服务后重试"
	default:
		return executor.DBErrorCodeGatewayInternal,
			"网关内部错误",
			"请联系管理员并提供审计标识"
	}
}

func internalFailureStage(cause error) string {
	stable := executor.StableError(cause)
	if stable != nil && stable.Reason != executor.ReasonDatabaseFailure {
		return StageGuardStatic
	}
	switch {
	case errors.Is(cause, ErrBusinessCommitUncertain):
		return string(executor.DBStageCommit)
	case errors.Is(cause, ErrAuditUnavailable):
		return StageAudit
	default:
		return ""
	}
}

func (run *pipelineRun) observe() {
	if run == nil || run.pipeline == nil || run.pipeline.observer == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	dialect := ""
	if run.datasource != nil {
		dialect = run.datasource.DBType
	}
	assessment := run.response.Assessment
	run.pipeline.observer.ObserveDecision(
		string(run.response.Decision),
		dialect,
		string(assessment.StmtType),
	)
	for _, hit := range assessment.Hits {
		run.pipeline.observer.ObserveRuleHit(
			hit.RuleID,
			string(hit.Decision),
			strconv.Itoa(int(hit.Risk)),
		)
	}
	for stage, latencyMS := range assessment.StageLatency {
		run.pipeline.observer.ObserveStage(stage, latencyMS)
	}
}

func (run *pipelineRun) audit(
	ctx context.Context,
	auditDecision string,
	operationError error,
	phase auditPhase,
) error {
	if err := run.beginAuditAttempt(phase); err != nil {
		return err
	}
	if releaseError := run.reservation.release(); releaseError != nil {
		operationError = errors.Join(operationError, releaseError)
		auditDecision = "error"
		run.setFailure(operationError)
	}
	run.response.Assessment.StageLatency = cloneStageLatency(run.stageLatency)
	run.response.auditPhase = phase
	run.response.relatedAuditID = run.auditAnchorID
	log, err := mapAuditLog(
		run.request,
		run.agent,
		run.datasource,
		run.ast,
		run.response,
		run.executionResult,
		auditDecision,
		operationError,
		run.started,
	)
	if err != nil {
		run.setFailure(ErrAuditUnavailable)
		return ErrAuditUnavailable
	}
	if phase == auditPhaseOutcome && auditDecision == string(model.DecisionError) {
		log.RowsReturned = nil
	}
	auditStarted := time.Now()
	auditParent := ctx
	if ctx.Err() != nil {
		auditParent = context.Background()
	}
	auditContext, cancel, contextError := statementContext(
		auditParent,
		auditTimeoutMS(run.datasource),
	)
	if contextError != nil {
		run.setFailure(ErrAuditUnavailable)
		return ErrAuditUnavailable
	}
	recorded, auditError := run.pipeline.ports.Audit.Record(auditContext, log)
	cancel()
	auditLatency := time.Since(auditStarted).Milliseconds()
	if auditLatency < 0 {
		auditLatency = 0
	}
	run.stageLatency[StageAudit] += auditLatency
	run.response.Assessment.StageLatency = cloneStageLatency(run.stageLatency)
	if auditError != nil {
		run.setFailure(ErrAuditUnavailable)
		return ErrAuditUnavailable
	}
	if run.auditAttempts == 1 {
		run.auditAnchorID = recorded.ID
	}
	run.response.AuditID = recorded.ID
	return operationError
}

func (run *pipelineRun) beginAuditAttempt(phase auditPhase) error {
	switch phase {
	case auditPhaseSingle, auditPhaseIntent:
		if run.auditAttempts != 0 {
			return fmt.Errorf("pipeline attempted an unexpected additional %s audit", auditPhaseLabel(phase))
		}
	case auditPhaseOutcome:
		if run.auditAttempts != 1 || run.auditAnchorID <= 0 {
			return fmt.Errorf("pipeline attempted outcome audit without exactly one persisted anchor audit")
		}
	default:
		return fmt.Errorf("pipeline attempted audit with unsupported phase %q", phase)
	}
	run.auditAttempts++
	return nil
}

func auditPhaseLabel(phase auditPhase) string {
	if phase == auditPhaseSingle {
		return "single"
	}
	return string(phase)
}

func auditTimeoutMS(datasource *model.Datasource) int {
	if datasource == nil || datasource.StmtTimeoutMS == 0 {
		return defaultStatementTimeout
	}
	return datasource.StmtTimeoutMS
}

func (run *pipelineRun) approve(ctx context.Context) (Response, error) {
	var approval model.Approval
	if err := run.measure(StageExecute, func() error {
		approvalID, err := generateApprovalID()
		if err != nil {
			return err
		}
		approval = model.Approval{
			ID:      approvalID,
			AgentID: stringPointer(run.agent.ID),
			SQLRaw:  stringPointer(run.request.SQL),
			Reason:  stringPointer(run.response.Assessment.Reason),
			Status:  "pending",
		}
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}
	if err := run.reservation.release(); err != nil {
		return run.finish(ctx, err)
	}
	run.response.Assessment.StageLatency = cloneStageLatency(run.stageLatency)
	log, err := mapAuditLog(
		run.request,
		run.agent,
		run.datasource,
		run.ast,
		run.response,
		run.executionResult,
		string(model.DecisionApprove),
		nil,
		run.started,
	)
	if err != nil {
		return run.finish(ctx, err)
	}
	workflow, ok := run.pipeline.ports.Approvals.(ApprovalWorkflow)
	if !ok || isNilInterface(workflow) {
		return run.finish(ctx, fmt.Errorf("approval writer does not support atomic audit workflow"))
	}
	var created model.Approval
	var recorded model.AuditLog
	err = run.measure(StageAudit, func() error {
		approvalContext, cancel, err := statementContext(ctx, run.datasource.StmtTimeoutMS)
		if err != nil {
			return err
		}
		defer cancel()
		created, recorded, err = workflow.CreatePendingWithAudit(approvalContext, approval, log)
		return err
	})
	run.response.Assessment.StageLatency = cloneStageLatency(run.stageLatency)
	if err != nil {
		return run.finish(ctx, err)
	}
	if strings.TrimSpace(created.ID) == "" || recorded.ID <= 0 {
		err = fmt.Errorf("approval workflow returned an empty persisted identity")
		return run.finish(ctx, err)
	}
	run.auditAttempts = 1
	run.auditAnchorID = recorded.ID
	run.response.ApprovalID = created.ID
	run.response.AuditID = recorded.ID
	return run.finish(ctx, nil)
}

func validatePorts(ports Ports) error {
	dependencies := []struct {
		name  string
		value any
	}{
		{name: "Authenticator", value: ports.Authenticator},
		{name: "Datasources", value: ports.Datasources},
		{name: "Policies", value: ports.Policies},
		{name: "Executors", value: ports.Executors},
		{name: "Approvals", value: ports.Approvals},
		{name: "Audit", value: ports.Audit},
		{name: "Redactors", value: ports.Redactors},
	}
	for _, dependency := range dependencies {
		if isNilInterface(dependency.value) {
			return fmt.Errorf("construct pipeline: %s is required: %w", dependency.name, ErrInvalidPorts)
		}
	}
	return nil
}

func validateLoadedPolicies(policies []model.Policy, agentID, datasourceID string) error {
	for index, stored := range policies {
		if stored.AgentID != agentID || stored.DatasourceID != datasourceID {
			return fmt.Errorf(
				"policy %d belongs to agent %q datasource %q, expected %q/%q",
				index,
				stored.AgentID,
				stored.DatasourceID,
				agentID,
				datasourceID,
			)
		}
	}
	return nil
}

func columnAuthorizationConfigured(controller ColumnAuthorizationController) bool {
	configured, ok := controller.(ColumnAuthorizationConfiguration)
	return ok && configured.ColumnAuthorizationConfigured()
}

func hasB2ColumnPolicy(policies []model.Policy) bool {
	for _, stored := range policies {
		if strings.EqualFold(strings.TrimSpace(stored.ObjectType), "column") ||
			len(stored.ColumnPermissions) != 0 || len(stored.ColumnStaging) != 0 {
			return true
		}
	}
	return false
}

func shouldExplain(ast *model.AST) bool {
	if ast == nil {
		return false
	}
	switch ast.StmtType {
	case model.StmtType("SELECT"), model.StmtType("INSERT"), model.StmtType("UPDATE"), model.StmtType("DELETE"):
		return true
	default:
		return false
	}
}

func shouldBindDeniedSelect(ast *model.AST, assessment model.Assessment) bool {
	if ast == nil || ast.StmtType != model.StmtType("SELECT") || assessment.Decision != model.DecisionDeny {
		return false
	}
	foundObjectDeny := false
	for _, hit := range assessment.Hits {
		if hit.Decision != model.DecisionDeny {
			continue
		}
		if hit.RuleID != "R010" {
			return false
		}
		foundObjectDeny = true
	}
	return foundObjectDeny
}

func normalizedRowLimit(value int) (int, error) {
	if value == 0 {
		return defaultRowLimit, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("datasource row limit cannot be negative")
	}
	return value, nil
}

func statementContext(
	parent context.Context,
	timeoutMS int,
) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, fmt.Errorf("statement context parent is nil")
	}
	if timeoutMS == 0 {
		timeoutMS = defaultStatementTimeout
	}
	if timeoutMS < 0 || int64(timeoutMS) > int64((time.Duration(1<<63-1))/time.Millisecond) {
		return nil, nil, fmt.Errorf("invalid statement timeout %dms", timeoutMS)
	}
	ctx, cancel := executor.WithStatementDeadline(parent, time.Duration(timeoutMS)*time.Millisecond)
	return ctx, cancel, nil
}

func generateApprovalID() (string, error) {
	random := make([]byte, approvalIDRandomByteSize)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", fmt.Errorf("generate approval ID: %w", err)
	}
	return "apr_" + hex.EncodeToString(random), nil
}

func cloneStageLatency(source map[string]int64) map[string]int64 {
	cloned := make(map[string]int64, len(source))
	for stage, latency := range source {
		cloned[stage] = latency
	}
	return cloned
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
