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

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/executor"
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
	}, nil
}

// Process runs auth, load, parse, two guard gates, execute, redact, and audit.
func (pipeline *Pipeline) Process(ctx context.Context, request Request) (Response, error) {
	if pipeline == nil {
		return Response{}, fmt.Errorf("process SQL request: %w", ErrInvalidRequest)
	}
	run := newPipelineRun(pipeline, request)
	if ctx == nil {
		return run.finish(
			context.Background(),
			fmt.Errorf("process SQL request with nil context: %w", ErrInvalidRequest),
		)
	}

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
		decision, err := pipeline.resolver.Resolve(storedPolicies, run.agent.Level)
		if err != nil {
			return err
		}
		run.policy = decision
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}

	if err := run.measure(StageParse, func() error {
		sqlParser, err := parser.NewParser(model.DBDialect(run.datasource.DBType))
		if err != nil {
			return err
		}
		ast, err := sqlParser.Parse(request.SQL)
		if err != nil {
			return err
		}
		if ast == nil {
			return fmt.Errorf("parser returned nil AST")
		}
		run.ast = ast
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}

	if err := run.measure(StageGuardStatic, func() error {
		allRules, err := assembleRules(run.ast.Dialect, run.reservation, panicMetadataProvider{})
		if err != nil {
			return err
		}
		staticRules, _ := splitRules(allRules)
		requestLayers := mergeGlobalLayers(pipeline.layers, run.ruleLayer)
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
		unauthorized, err := policy.AuthorizeColumns(run.ast, run.policy)
		if err != nil {
			return err
		}
		if len(unauthorized) > 0 {
			run.response.Assessment.Hits = append(
				run.response.Assessment.Hits,
				model.RuleHit{
					RuleID:     "POLICY_COLUMN",
					Risk:       model.RiskDeny,
					Decision:   model.DecisionDeny,
					Message:    "查询包含未授权列: " + strings.Join(unauthorized, ", "),
					Suggestion: "请仅查询策略允许的列，或联系管理员补充列级授权",
				},
			)
			recomputeAssessmentDecision(&run.response.Assessment)
			run.response.Decision = run.response.Assessment.Decision
		}
		return nil
	}); err != nil {
		return run.finish(ctx, err)
	}
	staticAssessment := run.response.Assessment
	if staticAssessment.Decision == model.DecisionDeny {
		return run.finish(ctx, nil)
	}

	if err := run.measure(StageGuardDynamic, func() (stageError error) {
		dynamicContext, cancel, err := statementContext(ctx, run.datasource.StmtTimeoutMS)
		if err != nil {
			return err
		}
		defer cancel()
		openedSession := false
		defer func() {
			if stageError == nil || !openedSession || run.session == nil {
				return
			}
			if closeError := run.session.Close(); closeError != nil {
				stageError = errors.Join(stageError, closeError)
			}
			run.session = nil
		}()

		databaseExecutor, err := pipeline.ports.Executors.GetOrOpen(
			*run.datasource,
			append([]byte(nil), pipeline.secret...),
		)
		if err != nil {
			return err
		}
		if isNilInterface(databaseExecutor) {
			return fmt.Errorf("executor provider returned nil executor")
		}
		if databaseExecutor.Dialect() != run.datasource.DBType {
			return fmt.Errorf(
				"executor dialect %q does not match datasource %q",
				databaseExecutor.Dialect(),
				run.datasource.DBType,
			)
		}
		run.executor = databaseExecutor
		metadata := any(databaseExecutor)
		if request.SessionID != "" {
			session, err := databaseExecutor.OpenSession(dynamicContext, request.SessionID)
			if err != nil {
				return err
			}
			if isNilInterface(session) {
				return fmt.Errorf("executor returned nil session")
			}
			run.session = session
			openedSession = true
			metadata = session
		}
		if shouldExplain(run.ast) {
			var explain model.ExplainInfo
			if run.session != nil {
				explain, err = run.session.Explain(dynamicContext, request.SQL)
			} else {
				explain, err = databaseExecutor.Explain(dynamicContext, request.SQL)
			}
			if err != nil {
				return err
			}
			run.ast.Explain = &explain
		}

		allRules, err := assembleRules(run.ast.Dialect, run.reservation, metadata)
		if err != nil {
			return err
		}
		_, dynamicRules := splitRules(allRules)
		requestLayers := mergeGlobalLayers(pipeline.layers, run.ruleLayer)
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

	returnsRows := run.ast.StmtType == model.StmtType("SELECT")
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
			if run.session != nil {
				result, err = run.session.Query(executionContext, request.SQL, rowLimit)
			} else {
				result, err = run.executor.Query(executionContext, request.SQL, rowLimit)
			}
		} else if run.session != nil {
			result, err = run.session.Execute(executionContext, request.SQL)
		} else {
			result, err = run.executor.Execute(executionContext, request.SQL)
		}
		if err != nil {
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
			redactor, err := pipeline.ports.Redactors.RedactorFor(ctx, run.datasource.ID)
			if err != nil {
				return err
			}
			if isNilInterface(redactor) {
				return fmt.Errorf("redactor builder returned nil redactor")
			}
			redacted, report := redactor.Apply(*run.response.Result)
			run.response.Result = &redacted
			run.response.Redact = report
			return nil
		}); err != nil {
			return run.finish(ctx, err)
		}
	}
	return run.finish(ctx, nil)
}

func (run *pipelineRun) closeUnusedSession() error {
	if run.session == nil {
		return nil
	}
	session := run.session
	run.session = nil
	if err := session.Close(); err != nil {
		return fmt.Errorf("close non-executing pipeline session: %w", err)
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
	executor        executor.Executor
	session         executor.Session
	executionResult *model.QueryResult
	reservation     *requestLimiter
	audited         bool
}

func newPipelineRun(pipeline *Pipeline, request Request) *pipelineRun {
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
	run.response.Decision = model.DecisionDeny
	run.response.Result = nil
	run.response.Redact = mask.RedactReport{}
	run.response.ApprovalID = ""
	run.response.Assessment.Decision = model.DecisionDeny
	run.response.Assessment.Risk = model.RiskDeny
	run.response.Assessment.Reason = cause.Error()
	run.response.Assessment.Suggestion = "请修正请求或内部错误后重试"
	run.response.Assessment.StageLatency = run.stageLatency
}

func (run *pipelineRun) finish(ctx context.Context, operationError error) (Response, error) {
	auditDecision := string(run.response.Decision)
	if operationError != nil {
		run.setFailure(operationError)
		auditDecision = "error"
	}
	finalError := operationError
	if !run.audited {
		finalError = run.audit(ctx, auditDecision, operationError)
	}
	run.observe()
	return run.response, finalError
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
) error {
	if run.audited {
		return fmt.Errorf("pipeline attempted to audit one request more than once")
	}
	run.audited = true
	if releaseError := run.reservation.release(); releaseError != nil {
		operationError = errors.Join(operationError, releaseError)
		auditDecision = "error"
		run.setFailure(operationError)
	}
	run.response.Assessment.StageLatency = cloneStageLatency(run.stageLatency)
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
		combined := errors.Join(operationError, err)
		run.setFailure(combined)
		return combined
	}
	auditStarted := time.Now()
	recorded, auditError := run.pipeline.ports.Audit.Record(ctx, log)
	auditLatency := time.Since(auditStarted).Milliseconds()
	if auditLatency < 0 {
		auditLatency = 0
	}
	run.stageLatency[StageAudit] += auditLatency
	run.response.Assessment.StageLatency = cloneStageLatency(run.stageLatency)
	if auditError != nil {
		if operationError == nil {
			run.setFailure(auditError)
			return auditError
		}
		combined := errors.Join(operationError, auditError)
		run.setFailure(combined)
		return combined
	}
	run.response.AuditID = recorded.ID
	return operationError
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
	if err := run.audit(ctx, string(model.DecisionApprove), nil); err != nil {
		return run.finish(ctx, err)
	}
	approval.AuditID = int64Pointer(run.response.AuditID)
	var created model.Approval
	err := run.measure(StageExecute, func() error {
		approvalContext, cancel, err := statementContext(ctx, run.datasource.StmtTimeoutMS)
		if err != nil {
			return err
		}
		defer cancel()
		created, err = run.pipeline.ports.Approvals.Create(approvalContext, approval)
		return err
	})
	run.response.Assessment.StageLatency = cloneStageLatency(run.stageLatency)
	if err != nil {
		return run.finish(ctx, err)
	}
	if strings.TrimSpace(created.ID) == "" {
		err = fmt.Errorf("approval writer returned an empty approval ID")
		return run.finish(ctx, err)
	}
	run.response.ApprovalID = created.ID
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
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeoutMS)*time.Millisecond)
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
