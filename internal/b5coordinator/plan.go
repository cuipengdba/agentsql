package b5coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

type Failure struct {
	Code  b5.ErrorCode
	Cause error
}

func (failure *Failure) Error() string {
	if failure == nil {
		return ""
	}
	if failure.Cause == nil {
		return string(failure.Code)
	}
	return string(failure.Code) + ": " + failure.Cause.Error()
}
func (failure *Failure) Unwrap() error { return failure.Cause }

func fail(code b5.ErrorCode, cause error) error { return &Failure{Code: code, Cause: cause} }

func ErrorCode(err error) b5.ErrorCode {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	var sessionFailure *SessionFailure
	if errors.As(err, &sessionFailure) {
		return sessionFailure.Code
	}
	var reasoned interface{ AuthorizationReason() string }
	if errors.As(err, &reasoned) {
		code := b5.ErrorCode(reasoned.AuthorizationReason())
		switch code {
		case b5.ErrorAuthDMLBinderRequired,
			b5.ErrorAuthDMLActionMissing,
			b5.ErrorAuthDMLWriteTargetGrantMissing,
			b5.ErrorAuthDMLReferenceGrantMissing,
			b5.ErrorAuthImplicitObjectUnclosed,
			b5.ErrorAuthClosureUnsupported,
			b5.ErrorAuthConstraintClosureUnsupported,
			b5.ErrorAuthTypeClosureUnsupported,
			b5.ErrorAuthDefaultClosureUnsupported,
			b5.ErrorAuthExpressionClosureUnsupported,
			b5.ErrorAuthRewriteClosureUnsupported,
			b5.ErrorAuthRelationKindUnsupported,
			b5.ErrorAuthInternalObjectDenied,
			b5.ErrorAuthWholeRowUnsupported,
			b5.ErrorAuthCatalogRace,
			b5.ErrorPostgresVersionUnsupported,
			b5.ErrorPostgresCapabilityUnavailable,
			b5.ErrorTxPlanUnproven,
			b5.ErrorDialectTransactionUnsupported:
			return code
		}
	}
	return b5.ErrorNone
}

// Preflight analyzes the complete ordered plan before the coordinator is able
// to ask Engine for a connection. A single invalid operation rejects the whole
// plan and no partially analyzed plan is returned.
func Preflight(ctx context.Context, request PlanRequest, analyzer Analyzer) (Plan, error) {
	if ctx == nil || analyzer == nil {
		return Plan{}, fail(b5.ErrorTxPlanRequired, ErrInvalidPlan)
	}
	if resolver, ok := analyzer.(PlanAnalyzerResolver); ok {
		resolved, err := resolver.AnalyzerFor(ctx, request)
		if err != nil {
			return Plan{}, err
		}
		if resolved == nil {
			return Plan{}, fail(b5.ErrorAuthDMLBinderRequired, ErrCapabilityUnknown)
		}
		analyzer = resolved
	}
	limits := normalizeLimits(request.Limits)
	if request.Dialect != "postgres" {
		return Plan{}, fail(b5.ErrorDialectTransactionUnsupported, ErrInvalidPlan)
	}
	serverSupported := request.ServerMajor >= 14 && request.ServerMajor <= 18
	if request.ClosurePolicy == string(b5dml.BinderAttestationCatalogClosed) {
		serverSupported = request.ServerMajor > 0
	}
	if !serverSupported || request.BinderABI != b5dml.BinderABI ||
		!boundedIdentity(request.TenantID) || !boundedIdentity(request.PrincipalID) ||
		!boundedIdentity(request.AgentID) || !boundedIdentity(request.DatasourceID) ||
		request.KeyRevision == 0 || request.DatasourceRevision == 0 || request.PolicyRevision == 0 ||
		strings.TrimSpace(request.Isolation) == "" || strings.TrimSpace(request.ClosurePolicy) == "" ||
		len(request.Statements) == 0 || len(request.Statements) > limits.MaxStatements {
		return Plan{}, fail(b5.ErrorTxPlanRequired, ErrInvalidPlan)
	}

	approvedParser, err := parser.NewParser(model.DBDialect("postgres"))
	if err != nil {
		return Plan{}, fail(b5.ErrorTxDMLShapeUnsupported, err)
	}
	plan := Plan{
		SchemaID: PlanSchemaID, SchemaVersion: PlanSchemaVersion,
		TenantID: request.TenantID, PrincipalID: request.PrincipalID, AgentID: request.AgentID,
		DatasourceID: request.DatasourceID, KeyRevision: request.KeyRevision,
		DatasourceRevision: request.DatasourceRevision, PolicyRevision: request.PolicyRevision,
		Dialect: request.Dialect, ServerMajor: request.ServerMajor, Isolation: request.Isolation,
		BinderABI: request.BinderABI, ClosurePolicy: request.ClosurePolicy, Limits: limits,
		Statements: make([]PlannedStatement, 0, len(request.Statements)),
	}
	operationIDs := make(map[string]struct{}, len(request.Statements))
	var estimatedRows int64
	var estimatedWork uint64
	for ordinal, statement := range request.Statements {
		if strings.TrimSpace(statement.OperationID) == "" || len(statement.OperationID) > 128 ||
			len(statement.SQL) == 0 || len(statement.SQL) > limits.MaxSQLBytes || strings.TrimSpace(statement.SQL) != statement.SQL {
			return Plan{}, fail(b5.ErrorTxDMLShapeUnsupported, fmt.Errorf("%w: statement %d envelope", ErrInvalidPlan, ordinal))
		}
		if _, duplicate := operationIDs[statement.OperationID]; duplicate {
			return Plan{}, fail(b5.ErrorTxPlanMismatch, fmt.Errorf("%w: duplicate operation id", ErrInvalidPlan))
		}
		operationIDs[statement.OperationID] = struct{}{}
		ast, parseErr := approvedParser.Parse(statement.SQL)
		if parseErr != nil || ast == nil || ast.IsMulti {
			return Plan{}, fail(b5.ErrorTxControlStatementDenied, errors.Join(ErrInvalidPlan, parseErr))
		}
		switch ast.StmtType {
		case model.StmtType("SELECT"):
			return Plan{}, fail(b5.ErrorTxSelectUnsupported, ErrInvalidPlan)
		case model.StmtType("INSERT"), model.StmtType("UPDATE"), model.StmtType("DELETE"):
		default:
			return Plan{}, fail(b5.ErrorTxControlStatementDenied, ErrInvalidPlan)
		}

		analysis, analyzeErr := analyzer.Analyze(ctx, statement, ordinal)
		if analyzeErr != nil {
			return Plan{}, analyzeErr
		}
		semantic := b5dml.AnalyzeStatement(analysis.Facts)
		if !semantic.Allowed {
			code := b5.ErrorTxDMLShapeUnsupported
			if semantic.Reason == b5dml.StatementReturningUnsupported {
				code = b5.ErrorTxReturningUnsupported
			}
			return Plan{}, fail(code, fmt.Errorf("%w: %s", ErrPlanDenied, semantic.Reason))
		}
		if closure := b5dml.CheckClosure(analysis.Facts.Dialect, analysis.Facts.Shape, nil); !closure.Allowed {
			return Plan{}, fail(b5.ErrorAuthImplicitObjectUnclosed, fmt.Errorf("%w: %s", ErrPlanDenied, closure.Reason))
		}
		if analysis.TreeDigest == ([32]byte{}) || analysis.ManifestDigest == ([32]byte{}) || analysis.ClosureDigest == ([32]byte{}) {
			return Plan{}, fail(b5.ErrorAuthDMLBinderRequired, ErrCapabilityUnknown)
		}
		switch analysis.Decision {
		case DecisionAllow, DecisionWarn:
		case DecisionApprove:
			plan.RequiresApproval = true
		case DecisionMask:
			return Plan{}, fail(b5.ErrorTxMaskUnsupported, ErrPlanDenied)
		case DecisionDeny:
			return Plan{}, fail(b5.ErrorAuthDMLActionMissing, ErrPlanDenied)
		default:
			return Plan{}, fail(b5.ErrorAuthDMLBinderRequired, ErrCapabilityUnknown)
		}
		if analysis.EstimatedRows < 0 || analysis.EstimatedWork == 0 {
			return Plan{}, fail(b5.ErrorTxLimitExceeded, ErrInvalidPlan)
		}
		if analysis.EstimatedRows > limits.MaxAffectedRows-estimatedRows || analysis.EstimatedWork > limits.MaxEstimatedWork-estimatedWork {
			return Plan{}, fail(b5.ErrorTxLimitExceeded, ErrInvalidPlan)
		}
		estimatedRows += analysis.EstimatedRows
		estimatedWork += analysis.EstimatedWork
		plan.Statements = append(plan.Statements, PlannedStatement{
			Ordinal: ordinal, OperationID: statement.OperationID, SQL: statement.SQL,
			RawSQLDigest: sha256.Sum256([]byte(statement.SQL)), ReasonDigest: sha256.Sum256([]byte(statement.Reason)),
			TreeDigest: analysis.TreeDigest, ManifestDigest: analysis.ManifestDigest, ClosureDigest: analysis.ClosureDigest,
			Action: analysis.Facts.Action, Writes: append([]b5dml.WriteTarget(nil), semantic.Writes...),
			References: append([]b5dml.Reference(nil), semantic.References...), Decision: analysis.Decision,
			EstimatedRows: analysis.EstimatedRows, EstimatedWork: analysis.EstimatedWork, artifact: analysis.Artifact,
		})
	}

	canonical := encodePlan(plan)
	if len(canonical) > limits.MaxPlanBytes {
		return Plan{}, fail(b5.ErrorPlanByteLimitExceeded, ErrInvalidPlan)
	}
	plan.Digest = sha256.Sum256(canonical)
	return plan, nil
}

func normalizeLimits(limits ResourceLimits) ResourceLimits {
	defaults := DefaultResourceLimits()
	if limits.MaxStatements <= 0 || limits.MaxStatements > 32 {
		limits.MaxStatements = defaults.MaxStatements
	}
	if limits.MaxSQLBytes <= 0 || limits.MaxSQLBytes > 256<<10 {
		limits.MaxSQLBytes = defaults.MaxSQLBytes
	}
	if limits.MaxPlanBytes <= 0 || limits.MaxPlanBytes > 1<<20 {
		limits.MaxPlanBytes = defaults.MaxPlanBytes
	}
	if limits.MaxAffectedRows <= 0 || limits.MaxAffectedRows > 10_000 {
		limits.MaxAffectedRows = defaults.MaxAffectedRows
	}
	if limits.MaxEstimatedWork == 0 {
		limits.MaxEstimatedWork = defaults.MaxEstimatedWork
	}
	if limits.IdleTimeout <= 0 || limits.IdleTimeout > 30e9 {
		limits.IdleTimeout = defaults.IdleTimeout
	}
	if limits.WallTimeout <= 0 || limits.WallTimeout > 60e9 {
		limits.WallTimeout = defaults.WallTimeout
	}
	if limits.StatementTimeout <= 0 || limits.StatementTimeout > 5e9 {
		limits.StatementTimeout = defaults.StatementTimeout
	}
	if limits.OperationWatchdog <= 0 {
		limits.OperationWatchdog = defaults.OperationWatchdog
	}
	if limits.QuiesceGrace <= 0 || limits.QuiesceGrace > 50e6 {
		limits.QuiesceGrace = defaults.QuiesceGrace
	}
	return limits
}

func boundedIdentity(value string) bool {
	return len(value) > 0 && len(value) <= 128 && strings.TrimSpace(value) == value
}

func encodePlan(plan Plan) []byte {
	var output bytes.Buffer
	writeString(&output, PlanSchemaID)
	writeUint(&output, uint64(PlanSchemaVersion))
	for _, value := range []string{plan.TenantID, plan.PrincipalID, plan.AgentID, plan.DatasourceID} {
		writeString(&output, value)
	}
	for _, value := range []uint64{plan.KeyRevision, plan.DatasourceRevision, plan.PolicyRevision} {
		writeUint(&output, value)
	}
	writeString(&output, plan.Dialect)
	writeUint(&output, uint64(plan.ServerMajor))
	writeString(&output, plan.Isolation)
	writeString(&output, plan.BinderABI)
	writeString(&output, plan.ClosurePolicy)
	writeUint(&output, uint64(plan.Limits.MaxStatements))
	writeUint(&output, uint64(plan.Limits.MaxSQLBytes))
	writeUint(&output, uint64(plan.Limits.MaxPlanBytes))
	writeUint(&output, uint64(plan.Limits.MaxAffectedRows))
	writeUint(&output, plan.Limits.MaxEstimatedWork)
	writeUint(&output, uint64(plan.Limits.IdleTimeout))
	writeUint(&output, uint64(plan.Limits.WallTimeout))
	writeUint(&output, uint64(plan.Limits.StatementTimeout))
	writeUint(&output, uint64(plan.Limits.OperationWatchdog))
	writeUint(&output, uint64(plan.Limits.QuiesceGrace))
	writeUint(&output, uint64(len(plan.Statements)))
	for _, statement := range plan.Statements {
		writeUint(&output, uint64(statement.Ordinal))
		writeString(&output, statement.OperationID)
		writeBytes(&output, statement.RawSQLDigest[:])
		writeBytes(&output, statement.TreeDigest[:])
		writeBytes(&output, statement.ReasonDigest[:])
		writeString(&output, statement.Action.String())
		writeBytes(&output, statement.ManifestDigest[:])
		writeBytes(&output, statement.ClosureDigest[:])
		writeUint(&output, uint64(statement.Decision))
		writeUint(&output, uint64(statement.EstimatedRows))
		writeUint(&output, statement.EstimatedWork)
		writeUint(&output, uint64(len(statement.Writes)))
		for _, value := range statement.Writes {
			encodeWrite(&output, value)
		}
		writeUint(&output, uint64(len(statement.References)))
		for _, value := range statement.References {
			encodeReference(&output, value)
		}
	}
	return output.Bytes()
}

func encodeRelation(output *bytes.Buffer, value b5dml.RelationIdentity) {
	writeString(output, value.DatasourceID)
	writeUint(output, uint64(value.DatabaseOID))
	writeUint(output, uint64(value.RelationOID))
	writeUint(output, uint64(value.RelationKind))
	writeString(output, value.Schema)
	writeString(output, value.Name)
	writeString(output, value.CatalogFingerprint)
}
func encodeColumn(output *bytes.Buffer, value b5dml.ColumnIdentity) {
	encodeRelation(output, value.Relation)
	writeUint(output, uint64(uint16(value.Attnum)))
	writeString(output, value.Name)
	writeUint(output, uint64(value.TypeOID))
	writeUint(output, uint64(uint32(value.TypeModifier)))
	writeUint(output, uint64(value.CollationOID))
}
func encodeWrite(output *bytes.Buffer, value b5dml.WriteTarget) {
	writeUint(output, uint64(value.Kind))
	encodeRelation(output, value.Relation)
	encodeColumn(output, value.Column)
	writeUint(output, uint64(value.Source))
}
func encodeReference(output *bytes.Buffer, value b5dml.Reference) {
	writeUint(output, uint64(value.Kind))
	writeUint(output, uint64(value.Site))
	encodeRelation(output, value.Relation)
	encodeColumn(output, value.Column)
	writeString(output, value.SystemName)
}
func writeString(output *bytes.Buffer, value string) { writeBytes(output, []byte(value)) }
func writeBytes(output *bytes.Buffer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	output.Write(length[:])
	output.Write(value)
}
func writeUint(output *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	output.Write(encoded[:])
}
