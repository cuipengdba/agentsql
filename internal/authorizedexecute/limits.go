package authorizedexecute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
)

// Reason is a stable, non-sensitive authorization failure reason.
type Reason string

const (
	ReasonRequestTooLarge           Reason = "AUTH_REQUEST_TOO_LARGE"
	ReasonParameterLimit            Reason = "AUTH_PARAMETER_LIMIT"
	ReasonTokenLimit                Reason = "AUTH_TOKEN_LIMIT"
	ReasonNestingLimit              Reason = "AUTH_NESTING_LIMIT"
	ReasonASTLimit                  Reason = "AUTH_AST_LIMIT"
	ReasonQueryBlockLimit           Reason = "AUTH_QUERY_BLOCK_LIMIT"
	ReasonProjectionLimit           Reason = "AUTH_PROJECTION_LIMIT"
	ReasonRelationLimit             Reason = "AUTH_RELATION_LIMIT"
	ReasonViewDepthLimit            Reason = "AUTH_VIEW_DEPTH_LIMIT"
	ReasonCatalogRTLimit            Reason = "AUTH_CATALOG_ROUND_TRIP_LIMIT"
	ReasonDefinitionLimit           Reason = "AUTH_DEFINITION_LIMIT"
	ReasonBinderLimit               Reason = "AUTH_BINDER_LIMIT"
	ReasonCatalogLimit              Reason = "AUTH_CATALOG_LIMIT"
	ReasonCatalogRowLimit           Reason = "AUTH_CATALOG_ROW_LIMIT"
	ReasonColumnLimit               Reason = "AUTH_COLUMN_METADATA_LIMIT"
	ReasonDependencyLimit           Reason = "AUTH_DEPENDENCY_LIMIT"
	ReasonPathLimit                 Reason = "AUTH_PATH_LIMIT"
	ReasonWorkLimit                 Reason = "AUTH_WORK_LIMIT"
	ReasonFrameLimit                Reason = "AUTH_FRAME_LIMIT"
	ReasonCellLimit                 Reason = "AUTH_CELL_LIMIT"
	ReasonRowLimit                  Reason = "AUTH_ROW_LIMIT"
	ReasonResultLimit               Reason = "AUTH_RESULT_LIMIT"
	ReasonEnvelopeLimit             Reason = "AUTH_ENVELOPE_LIMIT"
	ReasonDeadlineExceeded          Reason = "AUTH_DEADLINE_EXCEEDED"
	ReasonConcurrencyLimit          Reason = "AUTH_CONCURRENCY_LIMIT"
	ReasonMemoryLimit               Reason = "AUTH_MEMORY_LIMIT"
	ReasonDatabaseFailure           Reason = "AUTH_DATABASE_ERROR"
	ReasonCatalogIncomplete         Reason = "AUTH_CATALOG_INCOMPLETE"
	ReasonImplicitObject            Reason = "AUTH_IMPLICIT_OBJECT_UNSUPPORTED"
	ReasonRelationShape             Reason = "AUTH_RELATION_SHAPE_UNSUPPORTED"
	ReasonColumnShape               Reason = "AUTH_COLUMN_SHAPE_UNSUPPORTED"
	ReasonExpressionShape           Reason = "AUTH_EXPRESSION_IDENTITY_UNSUPPORTED"
	ReasonBinderIncomplete          Reason = "AUTH_BINDER_INCOMPLETE"
	ReasonBinderCapability          Reason = "AUTH_BINDER_CAPABILITY_MISMATCH"
	ReasonBinderModeRequired        Reason = "AUTH_BINDER_MODE_REQUIRED"
	ReasonBinderModeUnsupported     Reason = "AUTH_BINDER_MODE_UNSUPPORTED"
	ReasonBinderDivergence          Reason = "AUTH_BINDER_DIVERGENCE"
	ReasonBindLockFailed            Reason = "AUTH_BIND_LOCK_FAILED"
	ReasonBindIdentityDrift         Reason = "AUTH_BIND_IDENTITY_DRIFT"
	ReasonBindClosure               Reason = "AUTH_BIND_CLOSURE_MISMATCH"
	ReasonCatalogRace               Reason = "AUTH_CATALOG_RACE"
	ReasonPreparedInvalid           Reason = "AUTH_PREPARED_INVALIDATED"
	ReasonPreparedState             Reason = "AUTH_PREPARED_STATE_INVALID"
	ReasonAgentDenied               Reason = "AUTH_AGENT_DENIED"
	ReasonStatementClassDenied      Reason = "AUTH_STATEMENT_CLASS_DENIED"
	ReasonDatasourceUnsupported     Reason = "AUTH_DATASOURCE_UNSUPPORTED"
	ReasonColumnAuthUnsupported     Reason = "AUTH_COLUMN_AUTHORIZATION_UNSUPPORTED"
	ReasonColumnAuthUnavailable     Reason = "AUTH_COLUMN_AUTHORIZATION_UNAVAILABLE"
	ReasonRelationDenied            Reason = "AUTH_RELATION_DENIED"
	ReasonRelationGrantMissing      Reason = "AUTH_RELATION_GRANT_MISSING"
	ReasonIdentityUnproven          Reason = "AUTH_IDENTITY_UNPROVEN"
	ReasonColumnGrantMissing        Reason = "AUTH_COLUMN_GRANT_MISSING"
	ReasonMaskMeetUndefined         Reason = "AUTH_MASK_MEET_UNDEFINED"
	ReasonMaskCapabilityMissing     Reason = "AUTH_MASK_CAPABILITY_MISSING"
	ReasonAuthorizationProofInvalid Reason = "AUTH_PROOF_INVALID"
	ReasonAuditUnavailable          Reason = "AUTH_AUDIT_UNAVAILABLE"
	ReasonFinalFenceFailed          Reason = "AUTH_FINAL_FENCE_FAILED"
	ReasonDeliverySealFailed        Reason = "AUTH_DELIVERY_SEAL_FAILED"
)

// AuthError intentionally carries no underlying error or dynamic text.
type AuthError struct{ Reason Reason }

func (err *AuthError) Error() string {
	if err == nil || err.Reason == "" {
		return string(ReasonDatabaseFailure)
	}
	return string(err.Reason)
}

func limitError(reason Reason) error { return &AuthError{Reason: reason} }

// Limits is the non-expandable S2 resource envelope. A zero field selects the
// default. Values above the defaults are clamped, so configuration can only
// tighten the security boundary.
type Limits struct {
	RawSQLBytes       int
	ParameterCount    int
	ParameterBytes    int
	ParametersBytes   int
	Tokens            int
	LexicalDepth      int
	ASTNodes          int
	QueryBlocks       int
	Projections       int
	OutputColumns     int
	Relations         int
	ViewDepth         int
	CatalogRoundTrips int
	DefinitionBytes   int
	BinderBytes       int
	CatalogBytes      int
	CatalogRows       int
	ColumnMetadata    int
	DependencyEdges   int
	ExpandedPaths     int
	WorkUnits         int
	FrameBytes        int
	RawCellBytes      int
	RawRowBytes       int
	RawResultBytes    int
	MaskedCellBytes   int
	MaskedResultBytes int
	EnvelopeBytes     int
}

var DefaultLimits = Limits{
	RawSQLBytes: 256 << 10, ParameterCount: 256, ParameterBytes: 64 << 10, ParametersBytes: 1 << 20,
	Tokens: 100_000, LexicalDepth: 128, ASTNodes: 50_000, QueryBlocks: 256,
	Projections: 4_096, OutputColumns: 256, Relations: 256, ViewDepth: 16, CatalogRoundTrips: 32,
	DefinitionBytes: 1 << 20, BinderBytes: 8 << 20, CatalogBytes: 8 << 20,
	CatalogRows: 50_000, ColumnMetadata: 16_384, DependencyEdges: 32_768,
	ExpandedPaths: 65_536, WorkUnits: 250_000, FrameBytes: 1 << 20,
	RawCellBytes: 64 << 10, RawRowBytes: 1 << 20, RawResultBytes: 16 << 20,
	MaskedCellBytes: 128 << 10, MaskedResultBytes: 16 << 20, EnvelopeBytes: 20 << 20,
}

func NormalizeLimits(candidate Limits) Limits {
	result := candidate
	clamp := func(value *int, maximum int) {
		if *value <= 0 || *value > maximum {
			*value = maximum
		}
	}
	clamp(&result.RawSQLBytes, DefaultLimits.RawSQLBytes)
	clamp(&result.ParameterCount, DefaultLimits.ParameterCount)
	clamp(&result.ParameterBytes, DefaultLimits.ParameterBytes)
	clamp(&result.ParametersBytes, DefaultLimits.ParametersBytes)
	clamp(&result.Tokens, DefaultLimits.Tokens)
	clamp(&result.LexicalDepth, DefaultLimits.LexicalDepth)
	clamp(&result.ASTNodes, DefaultLimits.ASTNodes)
	clamp(&result.QueryBlocks, DefaultLimits.QueryBlocks)
	clamp(&result.Projections, DefaultLimits.Projections)
	clamp(&result.OutputColumns, DefaultLimits.OutputColumns)
	clamp(&result.Relations, DefaultLimits.Relations)
	clamp(&result.ViewDepth, DefaultLimits.ViewDepth)
	clamp(&result.CatalogRoundTrips, DefaultLimits.CatalogRoundTrips)
	clamp(&result.DefinitionBytes, DefaultLimits.DefinitionBytes)
	clamp(&result.BinderBytes, DefaultLimits.BinderBytes)
	clamp(&result.CatalogBytes, DefaultLimits.CatalogBytes)
	clamp(&result.CatalogRows, DefaultLimits.CatalogRows)
	clamp(&result.ColumnMetadata, DefaultLimits.ColumnMetadata)
	clamp(&result.DependencyEdges, DefaultLimits.DependencyEdges)
	clamp(&result.ExpandedPaths, DefaultLimits.ExpandedPaths)
	clamp(&result.WorkUnits, DefaultLimits.WorkUnits)
	clamp(&result.FrameBytes, DefaultLimits.FrameBytes)
	clamp(&result.RawCellBytes, DefaultLimits.RawCellBytes)
	clamp(&result.RawRowBytes, DefaultLimits.RawRowBytes)
	clamp(&result.RawResultBytes, DefaultLimits.RawResultBytes)
	clamp(&result.MaskedCellBytes, DefaultLimits.MaskedCellBytes)
	clamp(&result.MaskedResultBytes, DefaultLimits.MaskedResultBytes)
	clamp(&result.EnvelopeBytes, DefaultLimits.EnvelopeBytes)
	return result
}

// ValidatePreParse applies byte, parameter, token and lexical-depth bounds
// without allocating proportionally to attacker-controlled token counts.
func ValidatePreParse(rawSQL string, parameters [][]byte, limits Limits) error {
	limits = NormalizeLimits(limits)
	if !utf8.ValidString(rawSQL) || len(rawSQL) > limits.RawSQLBytes {
		return limitError(ReasonRequestTooLarge)
	}
	if len(parameters) > limits.ParameterCount {
		return limitError(ReasonParameterLimit)
	}
	total := 0
	for _, parameter := range parameters {
		if len(parameter) > limits.ParameterBytes || total > limits.ParametersBytes-len(parameter) {
			return limitError(ReasonParameterLimit)
		}
		total += len(parameter)
	}
	return scanSQLShape(rawSQL, limits)
}

func scanSQLShape(rawSQL string, limits Limits) error {
	tokens, depth, queryBlocks := 0, 0, 0
	inSingle, inDouble, inBacktick, inLineComment, inBlockComment := false, false, false, false, false
	for index := 0; index < len(rawSQL); index++ {
		current := rawSQL[index]
		next := byte(0)
		if index+1 < len(rawSQL) {
			next = rawSQL[index+1]
		}
		if inLineComment {
			if current == '\n' || current == '\r' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if current == '*' && next == '/' {
				inBlockComment = false
				index++
			}
			continue
		}
		if inSingle {
			if current == '\'' {
				if next == '\'' {
					index++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			if current == '"' {
				if next == '"' {
					index++
				} else {
					inDouble = false
				}
			}
			continue
		}
		if inBacktick {
			if current == '`' {
				if next == '`' {
					index++
				} else {
					inBacktick = false
				}
			}
			continue
		}
		if current == '-' && next == '-' {
			inLineComment = true
			index++
			continue
		}
		if current == '/' && next == '*' {
			inBlockComment = true
			index++
			continue
		}
		switch current {
		case '\'':
			inSingle = true
			tokens++
		case '"':
			inDouble = true
			tokens++
		case '`':
			inBacktick = true
			tokens++
		case '(':
			depth++
			if depth > limits.LexicalDepth {
				return limitError(ReasonNestingLimit)
			}
			tokens++
		case ')':
			if depth > 0 {
				depth--
			}
			tokens++
		default:
			if isSQLSpace(current) {
				continue
			}
			if isSQLWordByte(current) {
				// Consume every identifier/keyword/numeric run once. Looking only
				// at whitespace or a tiny punctuation set undercounted compact
				// attacker input such as a+a+a by orders of magnitude.
				tokens++
				end := index + 1
				for end < len(rawSQL) && isSQLWordByte(rawSQL[end]) {
					end++
				}
				if end-index == len("SELECT") && strings.EqualFold(rawSQL[index:end], "SELECT") {
					queryBlocks++
					if queryBlocks > limits.QueryBlocks {
						return limitError(ReasonQueryBlockLimit)
					}
				}
				index = end - 1
			} else {
				// Every remaining non-space byte is an operator/delimiter token.
				// Counting unknown UTF-8 bytes conservatively is fail-closed and
				// avoids allocating or needing a dialect lexer at this boundary.
				tokens++
			}
		}
		if tokens > limits.Tokens {
			return limitError(ReasonTokenLimit)
		}
	}
	return nil
}

func isSQLWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func isSQLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f'
}

// Budget is a single checked counter shared by AST, lineage and catalog work.
type Budget struct {
	limits                    Limits
	work, nodes, edges, paths uint64
	relations, catalogTrips   uint64
	definitionBytes           uint64
	binderBytes, catalogBytes uint64
	catalogRows, columns      uint64
}

func NewBudget(limits Limits) *Budget { return &Budget{limits: NormalizeLimits(limits)} }

func (budget *Budget) charge(value *uint64, delta int, maximum int, reason Reason) error {
	if budget == nil || delta < 0 {
		return limitError(ReasonWorkLimit)
	}
	amount := uint64(delta)
	if *value > math.MaxUint64-amount || *value+amount > uint64(maximum) {
		return limitError(reason)
	}
	*value += amount
	return nil
}

func (budget *Budget) ChargeNodes(count int) error {
	return budget.charge(&budget.nodes, count, budget.limits.ASTNodes, ReasonASTLimit)
}
func (budget *Budget) ChargeEdges(count int) error {
	return budget.charge(&budget.edges, count, budget.limits.DependencyEdges, ReasonDependencyLimit)
}
func (budget *Budget) ChargePaths(count int) error {
	return budget.charge(&budget.paths, count, budget.limits.ExpandedPaths, ReasonPathLimit)
}
func (budget *Budget) ChargeWork(count int) error {
	return budget.charge(&budget.work, count, budget.limits.WorkUnits, ReasonWorkLimit)
}

// ChargeRelations counts every distinct relation admitted to a candidate or
// locked closure. Callers must charge before appending to an attacker-sized
// slice so the limit also bounds allocation.
func (budget *Budget) ChargeRelations(count int) error {
	return budget.charge(&budget.relations, count, budget.limits.Relations, ReasonRelationLimit)
}

func (budget *Budget) CheckViewDepth(depth int) error {
	if budget == nil || depth < 0 || depth > budget.limits.ViewDepth {
		return limitError(ReasonViewDepthLimit)
	}
	return nil
}

func (budget *Budget) ChargeCatalogRoundTrips(count int) error {
	return budget.charge(&budget.catalogTrips, count, budget.limits.CatalogRoundTrips, ReasonCatalogRTLimit)
}

func (budget *Budget) ChargeDefinitionBytes(count int) error {
	return budget.charge(&budget.definitionBytes, count, budget.limits.DefinitionBytes, ReasonDefinitionLimit)
}

func (budget *Budget) ChargeBinderBytes(count int) error {
	return budget.charge(&budget.binderBytes, count, budget.limits.BinderBytes, ReasonBinderLimit)
}

func (budget *Budget) ChargeCatalogBytes(count int) error {
	return budget.charge(&budget.catalogBytes, count, budget.limits.CatalogBytes, ReasonCatalogLimit)
}

func (budget *Budget) ChargeCatalogRows(count int) error {
	return budget.charge(&budget.catalogRows, count, budget.limits.CatalogRows, ReasonCatalogRowLimit)
}

func (budget *Budget) ChargeColumnMetadata(count int) error {
	return budget.charge(&budget.columns, count, budget.limits.ColumnMetadata, ReasonColumnLimit)
}

func (budget *Budget) ChargeAST(ast *model.AST) error {
	if ast == nil {
		return limitError(ReasonASTLimit)
	}
	projectionCount := len(ast.DirectProjections)
	if len(ast.ProjectionLineages) > projectionCount {
		projectionCount = len(ast.ProjectionLineages)
	}
	if projectionCount > budget.limits.Projections {
		return limitError(ReasonProjectionLimit)
	}
	if len(ast.Tables) > budget.limits.Relations {
		return limitError(ReasonRelationLimit)
	}
	nodes := 1 + len(ast.Tables) + len(ast.Columns) + len(ast.Functions) + len(ast.Operations) + len(ast.DirectProjections)
	edges, paths := 0, 0
	for _, projection := range ast.ProjectionLineages {
		nodes++
		for _, arm := range projection.Arms {
			nodes++
			edges += len(arm.Dependencies)
			paths += len(arm.PossibleRelations)
		}
	}
	if err := budget.ChargeNodes(nodes); err != nil {
		return err
	}
	if err := budget.ChargeEdges(edges); err != nil {
		return err
	}
	if err := budget.ChargePaths(paths); err != nil {
		return err
	}
	return budget.ChargeWork(nodes + edges + paths)
}

// ValidateResult applies independent cell, row and aggregate limits. It does
// not truncate: any overflow invalidates the entire buffered result.
func ValidateResult(result model.QueryResult, masked bool, limits Limits) error {
	limits = NormalizeLimits(limits)
	if len(result.Columns) > limits.OutputColumns {
		return limitError(ReasonProjectionLimit)
	}
	cellLimit, resultLimit := limits.RawCellBytes, limits.RawResultBytes
	if masked {
		cellLimit, resultLimit = limits.MaskedCellBytes, limits.MaskedResultBytes
	}
	total := 0
	for _, column := range result.Columns {
		if len(column) > cellLimit || total > resultLimit-len(column) {
			return limitError(ReasonResultLimit)
		}
		total += len(column)
	}
	for _, row := range result.Rows {
		rowBytes := 0
		for _, cell := range row {
			if len(cell) > cellLimit {
				return limitError(ReasonCellLimit)
			}
			if rowBytes > limits.RawRowBytes-len(cell) {
				return limitError(ReasonRowLimit)
			}
			rowBytes += len(cell)
			if total > resultLimit-len(cell) {
				return limitError(ReasonResultLimit)
			}
			total += len(cell)
		}
	}
	return nil
}

// EncodeEnvelope fully encodes and caps a final response before any transport
// can observe it.
func EncodeEnvelope(value any, limits Limits) ([]byte, error) {
	limits = NormalizeLimits(limits)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, limitError(ReasonDatabaseFailure)
	}
	if len(encoded) > limits.EnvelopeBytes {
		return nil, limitError(ReasonEnvelopeLimit)
	}
	return encoded, nil
}

// StableError maps every internal/database failure to a fixed public error.
func StableError(err error) *AuthError {
	if err == nil {
		return nil
	}
	var authError *AuthError
	if errors.As(err, &authError) {
		return &AuthError{Reason: authError.Reason}
	}
	var reasoned interface{ AuthorizationReason() string }
	if errors.As(err, &reasoned) {
		reason := Reason(reasoned.AuthorizationReason())
		switch reason {
		case ReasonProjectionLimit, ReasonCellLimit, ReasonRowLimit, ReasonResultLimit, ReasonFrameLimit,
			ReasonCatalogIncomplete, ReasonImplicitObject, ReasonRelationShape, ReasonColumnShape,
			ReasonExpressionShape, ReasonBinderIncomplete, ReasonBinderCapability, ReasonBinderModeRequired,
			ReasonBinderModeUnsupported, ReasonBinderDivergence, ReasonBindLockFailed, ReasonBindIdentityDrift, ReasonBindClosure,
			ReasonCatalogRace, ReasonPreparedInvalid, ReasonPreparedState:
			return &AuthError{Reason: reason}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &AuthError{Reason: ReasonDeadlineExceeded}
	}
	return &AuthError{Reason: ReasonDatabaseFailure}
}

func (reason Reason) GoString() string { return fmt.Sprintf("Reason(%q)", string(reason)) }
