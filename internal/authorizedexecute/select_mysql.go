package authorizedexecute

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/columnauth"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

// authorizedMySQLSelect is a strictly limited sibling of the PostgreSQL B2
// path. It shares the same control snapshot, authorizer, masking, durable
// audit, final fence, and delivery seal, but uses MySQL's read-only transaction
// and metadata lock to bind one base table.
func (gateway *Gateway) authorizedMySQLSelect(ctx context.Context, request authorizedSelectRequest) (
	result AuthorizedSelectResult, returnedErr error,
) {
	if gateway == nil || gateway.manager == nil || request.DurableAudit == nil || request.FinalFence == nil ||
		request.Datasource.DBType != "mysql" || request.MySQLMetadataUsername == "" {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	limits := NormalizeLimits(request.Limits)
	shape, err := parser.ParseMySQLColumnSelectShape(request.SQL)
	if err != nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonColumnShape}
	}
	if shape.Schema == "" {
		shape.Schema = request.Datasource.Database
	}
	if shape.Schema == "" {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonColumnShape}
	}
	rowLimit := request.RowLimit
	if rowLimit <= 0 {
		rowLimit = 1_000
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonDatabaseFailure}
	}
	preflightInput := columnauth.Input{Agent: request.Agent, DatasourceID: request.Datasource.ID,
		Statement: model.StmtType("SELECT"), PreliminaryAllowed: request.PreliminaryAllowed,
		DatasourceSupported: true, Nonce: append([]byte(nil), nonce...), Now: time.Now()}
	preflight := columnauth.Authorize(preflightInput)
	if shouldFinishSelectPreflight(request, preflight.Reason()) {
		audit := buildColumnAuthorizationAudit(preflightInput, preflight)
		observe(request.Observe, PhaseAudit)
		if err := request.DurableAudit(ctx, audit, nil, mask.RedactReport{}); err != nil {
			return AuthorizedSelectResult{}, &AuthError{Reason: ReasonAuditUnavailable}
		}
		return AuthorizedSelectResult{Allowed: false, Reason: reasonFromColumn(preflight.Reason()), Audit: audit}, nil
	}
	scope := scopeFromContext(ctx)
	reservation, err := gateway.reservations.Reserve(scope.agent, scope.tenant, request.Datasource.ID, defaultStatementMemoryReservation())
	if err != nil {
		return AuthorizedSelectResult{}, err
	}
	defer reservation.Release()
	redactor, ok := request.Redactor.(mask.IdentityOnlyRedactor)
	if !ok || redactor == nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonMaskCapabilityMissing}
	}
	opened, err := gateway.manager.OpenFreshReadOnly(request.Datasource, request.Secret)
	if err != nil {
		return AuthorizedSelectResult{}, fixedExecutionError(err)
	}
	defer opened.Close()
	mysql, ok := opened.(*businessdb.MySQLExecutor)
	if !ok {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	inspector, err := businessdb.NewMySQLColumnIdentityInspector(ctx, request.Datasource,
		request.MySQLMetadataUsername, request.MySQLMetadataPassword)
	if err != nil {
		return AuthorizedSelectResult{}, fixedExecutionError(err)
	}
	defer inspector.Close()
	observe(request.Observe, PhaseBusinessBegin)
	prepared, err := mysql.PrepareMySQLColumnSelect(ctx, request.SQL, shape, inspector)
	if err != nil {
		return AuthorizedSelectResult{}, fixedExecutionError(err)
	}
	businessClosed := false
	defer func() {
		if !businessClosed {
			if closeErr := prepared.Close(context.Background(), false); closeErr != nil && returnedErr == nil {
				returnedErr = fixedExecutionError(closeErr)
				result = AuthorizedSelectResult{}
			}
		}
	}()

	input, identityRequests, err := mysqlColumnAuthorizationInput(request, shape, prepared.Catalog(), nonce)
	if err != nil {
		return AuthorizedSelectResult{}, err
	}
	maskPlans, maskErr := redactor.BuildIdentityPlan(identityRequests)
	if maskErr == nil {
		input.Masks = columnMaskCandidates(maskPlans, input.Uses)
	} else {
		input.Masks = failedMaskCandidate(input.Uses, errors.Is(maskErr, mask.ErrIdentityCapability))
	}
	plan := columnauth.Authorize(input)
	audit := buildColumnAuthorizationAudit(input, plan)
	if !plan.Allowed() {
		if err := prepared.Close(ctx, false); err != nil {
			return AuthorizedSelectResult{}, fixedExecutionError(err)
		}
		businessClosed = true
		observe(request.Observe, PhaseBusinessEnd)
		if err := proveBusinessEnded(ctx); err != nil {
			return AuthorizedSelectResult{}, &AuthError{Reason: ReasonFinalFenceFailed}
		}
		observe(request.Observe, PhaseAudit)
		if err := request.DurableAudit(ctx, audit, nil, mask.RedactReport{}); err != nil {
			return AuthorizedSelectResult{}, &AuthError{Reason: ReasonAuditUnavailable}
		}
		return AuthorizedSelectResult{Allowed: false, Reason: reasonFromColumn(plan.Reason()), Audit: audit}, nil
	}
	if !plan.Verify(input) || !sameProtectionMasks(plan.Masks(), maskPlans) {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonAuthorizationProofInvalid}
	}
	observe(request.Observe, PhaseExecute)
	raw, err := prepared.Execute(ctx, rowLimit)
	if err != nil {
		return AuthorizedSelectResult{}, fixedExecutionError(err)
	}
	if err := ValidateResult(raw, false, limits); err != nil {
		return AuthorizedSelectResult{}, err
	}
	observe(request.Observe, PhaseMask)
	masked, report, err := redactor.ApplyIdentityPlan(raw, maskPlans)
	if err != nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonMaskCapabilityMissing}
	}
	if err := ValidateResult(masked, true, limits); err != nil {
		return AuthorizedSelectResult{}, err
	}
	observe(request.Observe, PhaseEncode)
	encoded, err := json.Marshal(masked)
	if err != nil || len(encoded) > limits.EnvelopeBytes {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonEnvelopeLimit}
	}
	observe(request.Observe, PhaseFpost)
	postDigest, err := prepared.VerifyPost(ctx)
	if err != nil || postDigest != input.CatalogDigest {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonBinderIncomplete}
	}
	audit.FpostDigest = postDigest
	if err := prepared.Close(ctx, true); err != nil {
		return AuthorizedSelectResult{}, fixedExecutionError(err)
	}
	businessClosed = true
	observe(request.Observe, PhaseBusinessEnd)
	if err := proveBusinessEnded(ctx); err != nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonFinalFenceFailed}
	}
	observe(request.Observe, PhaseAudit)
	if err := request.DurableAudit(ctx, audit, &masked, report); err != nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonAuditUnavailable}
	}
	observe(request.Observe, PhaseFinalFence)
	if err := request.FinalFence(ctx); err != nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonFinalFenceFailed}
	}
	observe(request.Observe, PhaseSeal)
	seal := newDeliverySeal(encoded)
	if !seal.Verify(encoded) {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonDeliverySealFailed}
	}
	return AuthorizedSelectResult{Allowed: true, Reason: Reason("ALLOW"), Result: masked, Redact: report,
		Audit: audit, Encoded: encoded, Seal: seal}, nil
}

func mysqlColumnAuthorizationInput(request authorizedSelectRequest, shape parser.MySQLColumnSelectShape,
	catalog businessdb.MySQLBoundCatalog, nonce []byte,
) (columnauth.Input, []mask.IdentityRequest, error) {
	if catalog.ServerUUID == "" || catalog.TableID <= 0 || catalog.Fingerprint == "" ||
		catalog.Schema != shape.Schema || catalog.Table != shape.Table || len(shape.Uses) == 0 ||
		len(shape.Outputs) == 0 {
		return columnauth.Input{}, nil, &AuthError{Reason: ReasonBinderIncomplete}
	}
	relation := columnauth.RelationIdentity{
		DatabaseID: catalog.ServerUUID, StableObjectID: fmt.Sprintf("mysql:%s:%d", catalog.ServerUUID, catalog.TableID),
		Schema: catalog.Schema, Name: catalog.Table, CatalogFingerprint: catalog.Fingerprint,
		BindingAlias: shape.Alias,
	}
	byName := make(map[string]businessdb.MySQLBoundColumn, len(catalog.Columns))
	for _, column := range catalog.Columns {
		if _, duplicate := byName[column.Name]; duplicate || column.Ordinal <= 0 || column.TypeDigest == "" {
			return columnauth.Input{}, nil, &AuthError{Reason: ReasonBinderIncomplete}
		}
		byName[column.Name] = column
	}
	binderBytes, _ := json.Marshal(struct {
		SQL     string
		Shape   parser.MySQLColumnSelectShape
		Catalog businessdb.MySQLBoundCatalog
	}{request.SQL, shape, catalog})
	binderHash := sha256.Sum256(binderBytes)
	input := columnauth.Input{Agent: request.Agent, DatasourceID: request.Datasource.ID,
		Statement: model.StmtType("SELECT"), PreliminaryAllowed: request.PreliminaryAllowed,
		DatasourceSupported: true, CatalogConsistent: true, CatalogDigest: catalog.Fingerprint,
		BinderDigest: hex.EncodeToString(binderHash[:]), ControlRevisionDigest: request.ControlRevisionDigest,
		Policies: append([]model.Policy(nil), request.Policies...), Relations: []columnauth.RelationIdentity{relation},
		Nonce: append([]byte(nil), nonce...), Now: time.Now()}
	requests := make([]mask.IdentityRequest, len(shape.Outputs))
	for index := range requests {
		requests[index].OutputIndex = index
	}
	for _, use := range shape.Uses {
		column, exists := byName[use.Name]
		if !exists {
			return columnauth.Input{}, nil, &AuthError{Reason: ReasonColumnShape}
		}
		bound := columnauth.BoundColumnUse{Column: columnauth.ColumnIdentity{Relation: relation,
			Ordinal: column.Ordinal, Name: column.Name, TypeDigest: column.TypeDigest},
			Usage: columnauth.Usage(use.Usage), Site: use.Site, OutputIndex: use.OutputIndex,
			BindingAlias: strings.TrimSpace(shape.Alias)}
		if bound.Usage != columnauth.UsageOutput && bound.Usage != columnauth.UsageReference {
			return columnauth.Input{}, nil, &AuthError{Reason: ReasonColumnShape}
		}
		input.Uses = append(input.Uses, bound)
		if bound.Usage == columnauth.UsageOutput {
			if use.OutputIndex < 0 || use.OutputIndex >= len(requests) {
				return columnauth.Input{}, nil, &AuthError{Reason: ReasonColumnShape}
			}
			requests[use.OutputIndex].Sources = append(requests[use.OutputIndex].Sources,
				mask.PhysicalColumn{Schema: catalog.Schema, Table: catalog.Table,
					Column: column.Name, InputType: column.TypeDigest})
		}
	}
	for _, item := range requests {
		if len(item.Sources) != 1 {
			return columnauth.Input{}, nil, &AuthError{Reason: ReasonColumnShape}
		}
	}
	return input, requests, nil
}
