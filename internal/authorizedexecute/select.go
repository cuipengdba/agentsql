package authorizedexecute

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/columnauth"
	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	ColumnAuditVersion    = 2
	columnAuditMaxDetails = 256
	columnAuditMaxBytes   = 32 << 10
)

// ColumnAuthorizationRequest contains control-snapshot facts and callbacks,
// but no SQL, credentials, datasource, or SQL-capable handle. It is attached
// to the AuthorizedExecute call and cannot create a second raw SQL ingress.
type ColumnAuthorizationRequest struct {
	Agent                 model.Agent
	Policies              []model.Policy
	Redactor              mask.Redactor
	RowLimit              int
	PreliminaryAllowed    bool
	ControlRevisionDigest string
	Limits                Limits
	DurableAudit          func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error
	FinalFence            func(context.Context) error
	Observe               func(SelectPhase)
}

type authorizedSelectRequest struct {
	ColumnAuthorizationRequest
	Datasource model.Datasource
	Secret     []byte
	SQL        string
}

type SelectPhase string

const (
	PhaseBusinessBegin SelectPhase = "business_begin"
	PhaseExecute       SelectPhase = "execute"
	PhaseMask          SelectPhase = "mask"
	PhaseEncode        SelectPhase = "encode"
	PhaseFpost         SelectPhase = "fpost"
	PhaseBusinessEnd   SelectPhase = "business_end"
	PhaseAudit         SelectPhase = "audit"
	PhaseFinalFence    SelectPhase = "final_fence"
	PhaseSeal          SelectPhase = "seal"
)

type ColumnAuditDetail struct {
	Identity string `json:"identity"`
	Usage    string `json:"usage"`
	Site     string `json:"site"`
	Reason   string `json:"reason"`
}

type ColumnAuthorizationAudit struct {
	Version        int                 `json:"version"`
	Decision       string              `json:"decision"`
	Reason         string              `json:"reason"`
	PlanDigest     string              `json:"plan_digest"`
	BinderDigest   string              `json:"binder_digest"`
	FpreDigest     string              `json:"fpre_digest"`
	FpostDigest    string              `json:"fpost_digest,omitempty"`
	Details        []ColumnAuditDetail `json:"details,omitempty"`
	Truncated      bool                `json:"truncated,omitempty"`
	CompleteDigest string              `json:"complete_digest,omitempty"`
}

// DeliverySeal has no exported constructor or fields. It binds the exact
// encoded candidate to this invocation after audit and final fence complete.
type DeliverySeal struct {
	nonce  [32]byte
	digest [32]byte
}

func (seal DeliverySeal) Verify(encoded []byte) bool {
	h := sha256.New()
	h.Write(seal.nonce[:])
	h.Write(encoded)
	return string(seal.digest[:]) == string(h.Sum(nil))
}

type AuthorizedSelectResult struct {
	Allowed bool
	Reason  Reason
	Result  model.QueryResult
	Redact  mask.RedactReport
	Audit   ColumnAuthorizationAudit
	Encoded []byte
	Seal    DeliverySeal
}

// AuthorizedSelect is the S4 SELECT-only path. It consumes S3's exact locked
// prepared tree and enforces P0-E: business end, durable audit, final control
// fence, then seal. No callback capable of touching control state runs while
// the business transaction/lock is held.
func (gateway *Gateway) authorizedSelect(ctx context.Context, request authorizedSelectRequest) (result AuthorizedSelectResult, returnedErr error) {
	if gateway == nil || request.DurableAudit == nil || request.FinalFence == nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	limits := NormalizeLimits(request.Limits)
	if err := ValidatePreParse(request.SQL, nil, limits); err != nil {
		return AuthorizedSelectResult{}, err
	}
	rowLimit := request.RowLimit
	if rowLimit <= 0 {
		rowLimit = 1_000
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonDatabaseFailure}
	}
	preflightInput := columnauth.Input{
		Agent: request.Agent, DatasourceID: request.Datasource.ID, Statement: model.StmtType("SELECT"),
		PreliminaryAllowed: request.PreliminaryAllowed, DatasourceSupported: request.Datasource.DBType == "postgres",
		Nonce: append([]byte(nil), nonce...), Now: time.Now(),
	}
	preflight := columnauth.Authorize(preflightInput)
	if preflight.Reason() == columnauth.ReasonAgentDenied || preflight.Reason() == columnauth.ReasonStatementDenied || preflight.Reason() == columnauth.ReasonDatasourceUnsupported {
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
	executor, err := gateway.open(request.Datasource, request.Secret)
	if err != nil {
		return AuthorizedSelectResult{}, fixedExecutionError(err)
	}
	postgres, ok := executor.(*businessdb.PostgresExecutor)
	if !ok {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	budget := NewBudget(limits)
	observe(request.Observe, PhaseBusinessBegin)
	handshake, handshakeOK := gateway.postgresB2Handshake(request.Datasource.ID)
	if !handshakeOK {
		if _, err := gateway.ProbePostgresB2Modes(ctx, request.Datasource, request.Secret); err != nil {
			return AuthorizedSelectResult{}, StableError(err)
		}
		handshake, handshakeOK = gateway.postgresB2Handshake(request.Datasource.ID)
	}
	if !handshakeOK || !handshake.Closed.Available || handshake.Closed.Digest == "" {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonBinderCapability}
	}
	var facts businessdb.SemanticFacts
	var executePrepared func(context.Context, int) (model.QueryResult, error)
	var verifyPrepared func(context.Context, businessdb.PostgresCatalogBudget) (string, error)
	var closePrepared func(context.Context, bool) error
	if handshake.NativeHealth == "healthy" && handshake.Native.Available {
		prepared, prepareErr := postgres.PrepareBoundPostgresSelect(ctx, request.SQL, nil, budget)
		if prepareErr != nil {
			return AuthorizedSelectResult{}, StableError(prepareErr)
		}
		manifest, fpre := prepared.Manifest(), prepared.Fpre()
		facts, err = businessdb.NativeSelectSemanticFacts(request.Datasource.ID, businessdb.SemanticIdentity{
			DatasourceIdentity: request.Datasource.ID,
		}, manifest, fpre)
		if err != nil {
			_ = prepared.Close(context.Background(), false)
			return AuthorizedSelectResult{}, StableError(err)
		}
		executePrepared = prepared.Execute
		verifyPrepared = func(verifyContext context.Context, verifyBudget businessdb.PostgresCatalogBudget) (string, error) {
			frame, verifyErr := prepared.VerifyPost(verifyContext, verifyBudget)
			return frame.Fingerprint, verifyErr
		}
		closePrepared = prepared.Close
	} else {
		prepared, prepareErr := postgres.BindClosedSelect(ctx, businessdb.BindRequest{RawSQL: request.SQL,
			Identity: businessdb.SemanticIdentity{DatasourceIdentity: request.Datasource.ID}}, budget)
		if prepareErr != nil {
			return AuthorizedSelectResult{}, StableError(prepareErr)
		}
		facts = prepared.Program().Facts
		executePrepared = prepared.Execute
		verifyPrepared = func(verifyContext context.Context, verifyBudget businessdb.PostgresCatalogBudget) (string, error) {
			proof, verifyErr := prepared.VerifyPost(verifyContext, verifyBudget)
			return proof.CatalogPostDigest, verifyErr
		}
		closePrepared = func(closeContext context.Context, _ bool) error { return prepared.Close(closeContext) }
	}
	businessClosed := false
	defer func() {
		if businessClosed {
			return
		}
		if closeErr := closePrepared(context.Background(), false); closeErr != nil && returnedErr == nil {
			returnedErr = fixedExecutionError(closeErr)
			result = AuthorizedSelectResult{}
		}
	}()

	columnInput, identityRequests, err := postgresColumnAuthorizationInputFromFacts(request, facts, nonce)
	if err != nil {
		return AuthorizedSelectResult{}, err
	}
	maskPlans, maskPlanErr := redactor.BuildIdentityPlan(identityRequests)
	if maskPlanErr == nil {
		columnInput.Masks = columnMaskCandidates(maskPlans, columnInput.Uses)
	} else {
		columnInput.Masks = failedMaskCandidate(columnInput.Uses, errors.Is(maskPlanErr, mask.ErrIdentityCapability))
	}
	plan, err := businessdb.AuthorizeB2(facts, columnInput)
	if err != nil {
		return AuthorizedSelectResult{}, StableError(err)
	}
	audit := buildColumnAuthorizationAudit(columnInput, plan)
	if !plan.Allowed() {
		if err := closePrepared(ctx, false); err != nil {
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
	if !plan.Verify(columnInput) {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonAuthorizationProofInvalid}
	}
	if !sameProtectionMasks(plan.Masks(), maskPlans) {
		return AuthorizedSelectResult{}, &AuthError{Reason: ReasonAuthorizationProofInvalid}
	}

	observe(request.Observe, PhaseExecute)
	raw, err := executePrepared(ctx, rowLimit)
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
	// Fpre and Fpost are independently bounded catalog frames. Reusing the
	// discovery/Fpre counter here makes a valid multi-relation statement fail
	// merely because the same bounded catalog proof is repeated at Fpost.
	fpostDigest, err := verifyPrepared(ctx, NewBudget(limits))
	if err != nil {
		return AuthorizedSelectResult{}, StableError(err)
	}
	audit.FpostDigest = fpostDigest
	if err := closePrepared(ctx, true); err != nil {
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
	return AuthorizedSelectResult{Allowed: true, Reason: Reason("ALLOW"), Result: masked, Redact: report, Audit: audit, Encoded: encoded, Seal: seal}, nil
}

func sameProtectionMasks(protected []columnauth.OutputMask, executable []mask.IdentityPlan) bool {
	if len(protected) != len(executable) {
		return false
	}
	for index := range protected {
		left, right := protected[index], executable[index]
		if left.OutputIndex != right.OutputIndex || left.Identity != (columnauth.MaskIdentity{
			AlgorithmID: right.Identity.AlgorithmID, SemanticVersion: right.Identity.SemanticVersion,
			KeyID: right.Identity.KeyID, KeyVersion: right.Identity.KeyVersion,
			CanonicalParameters: right.Identity.CanonicalParameters,
			InputType:           right.Identity.InputType, OutputType: right.Identity.OutputType,
		}) {
			return false
		}
	}
	return true
}

func proveBusinessEnded(ctx context.Context) error {
	lease, err := lockrank.Acquire(ctx, lockrank.Control)
	if err != nil {
		return err
	}
	lease.Release()
	return nil
}

func observe(observer func(SelectPhase), phase SelectPhase) {
	if observer != nil {
		observer(phase)
	}
}

func newDeliverySeal(encoded []byte) DeliverySeal {
	var seal DeliverySeal
	_, _ = rand.Read(seal.nonce[:])
	h := sha256.New()
	h.Write(seal.nonce[:])
	h.Write(encoded)
	copy(seal.digest[:], h.Sum(nil))
	return seal
}

func postgresColumnAuthorizationInputFromFacts(request authorizedSelectRequest, facts businessdb.SemanticFacts, nonce []byte) (columnauth.Input, []mask.IdentityRequest, error) {
	factsDigest, err := facts.Digest()
	if err != nil || facts.StatementClass != businessdb.BinderStatementSelect || facts.Identity.CatalogDigest == "" {
		return columnauth.Input{}, nil, &AuthError{Reason: ReasonBinderIncomplete}
	}
	input := columnauth.Input{
		Agent: request.Agent, DatasourceID: request.Datasource.ID, Statement: model.StmtType("SELECT"),
		PreliminaryAllowed: request.PreliminaryAllowed, DatasourceSupported: true,
		CatalogConsistent: true, CatalogDigest: facts.Identity.CatalogDigest, BinderDigest: factsDigest,
		ControlRevisionDigest: request.ControlRevisionDigest, Policies: append([]model.Policy(nil), request.Policies...),
		Nonce: append([]byte(nil), nonce...), Now: time.Now(),
	}
	relations := make(map[uint32]columnauth.RelationIdentity, len(facts.Relations))
	for _, relation := range facts.Relations {
		identity := columnauth.RelationIdentity{
			DatabaseID:     strconv.FormatUint(uint64(relation.DatabaseOID), 10),
			StableObjectID: PostgresStableObjectID(relation.DatabaseOID, relation.RelationOID),
			Schema:         relation.Schema, Name: relation.Name, CatalogFingerprint: relation.CatalogFingerprint,
			BindingAlias: relation.BindingAlias, ViewPath: relation.ViewPath,
		}
		relations[relation.RelationOID] = identity
		input.Relations = append(input.Relations, identity)
	}
	requestsByOutput := make(map[int]*mask.IdentityRequest)
	for _, use := range facts.ColumnUses {
		relation, ok := relations[use.RelationOID]
		if !ok || use.Attnum <= 0 || use.WholeRow || use.SystemColumn {
			return columnauth.Input{}, nil, &AuthError{Reason: ReasonColumnShape}
		}
		usage := columnauth.Usage(use.Usage)
		bound := columnauth.BoundColumnUse{
			Column: columnauth.ColumnIdentity{Relation: relation, Ordinal: int(use.Attnum), Name: use.Name,
				TypeDigest: PostgresColumnTypeDigest(use.TypeOID, use.TypeModifier, use.CollationOID)},
			Usage: usage, Site: use.Site, OutputIndex: use.OutputIndex,
			BindingAlias: use.BindingAlias, ViewPath: use.ViewPath,
		}
		input.Uses = append(input.Uses, bound)
		if usage != columnauth.UsageOutput || use.OutputIndex < 0 {
			continue
		}
		entry := requestsByOutput[use.OutputIndex]
		if entry == nil {
			entry = &mask.IdentityRequest{OutputIndex: use.OutputIndex}
			requestsByOutput[use.OutputIndex] = entry
		}
		entry.Sources = append(entry.Sources, mask.PhysicalColumn{Schema: relation.Schema, Table: relation.Name,
			Column: use.Name, InputType: bound.Column.TypeDigest})
	}
	positions := make([]int, 0, len(requestsByOutput))
	for position := range requestsByOutput {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	requests := make([]mask.IdentityRequest, 0, len(positions))
	for _, position := range positions {
		requests = append(requests, *requestsByOutput[position])
	}
	return input, requests, nil
}

func columnMaskCandidates(plans []mask.IdentityPlan, uses []columnauth.BoundColumnUse) []columnauth.MaskCandidate {
	byPosition := make(map[int][]columnauth.BoundColumnUse)
	for _, use := range uses {
		if use.Usage == columnauth.UsageOutput && use.OutputIndex >= 0 {
			byPosition[use.OutputIndex] = append(byPosition[use.OutputIndex], use)
		}
	}
	var result []columnauth.MaskCandidate
	for _, plan := range plans {
		identity := columnauth.MaskIdentity{
			AlgorithmID: plan.Identity.AlgorithmID, SemanticVersion: plan.Identity.SemanticVersion,
			KeyID: plan.Identity.KeyID, KeyVersion: plan.Identity.KeyVersion,
			CanonicalParameters: plan.Identity.CanonicalParameters,
			InputType:           plan.Identity.InputType, OutputType: plan.Identity.OutputType,
		}
		matched := false
		for _, use := range byPosition[plan.OutputIndex] {
			// BuildIdentityPlan has already met the identities of all rules
			// applicable to this output. Do not bind that mask identity to an
			// unrelated contributor (for example an unused integer view column)
			// merely because the binder assigned it the same contributor group.
			if use.Column.TypeDigest != identity.InputType {
				continue
			}
			result = append(result, columnauth.MaskCandidate{OutputIndex: plan.OutputIndex, Column: use.Column, Identity: identity, Capable: true})
			matched = true
		}
		if !matched {
			result = append(result, columnauth.MaskCandidate{OutputIndex: plan.OutputIndex, Column: columnauth.ColumnIdentity{TypeDigest: "invalid"}, Identity: identity, Capable: true})
		}
	}
	return result
}

func failedMaskCandidate(uses []columnauth.BoundColumnUse, capability bool) []columnauth.MaskCandidate {
	for _, use := range uses {
		if use.Usage != columnauth.UsageOutput {
			continue
		}
		identity := columnauth.MaskIdentity{AlgorithmID: "invalid", SemanticVersion: "0", InputType: "invalid", OutputType: "invalid"}
		return []columnauth.MaskCandidate{{OutputIndex: use.OutputIndex, Column: use.Column, Identity: identity, Capable: !capability}}
	}
	return []columnauth.MaskCandidate{{OutputIndex: 0, Identity: columnauth.MaskIdentity{AlgorithmID: "invalid"}, Capable: !capability}}
}

func buildColumnAuthorizationAudit(input columnauth.Input, plan columnauth.ProtectionPlan) ColumnAuthorizationAudit {
	audit := ColumnAuthorizationAudit{Version: ColumnAuditVersion, Decision: "deny", Reason: string(plan.Reason()), PlanDigest: plan.Digest(), BinderDigest: input.BinderDigest, FpreDigest: input.CatalogDigest}
	if plan.Allowed() {
		audit.Decision = "allow"
	}
	all := make([]ColumnAuditDetail, 0, len(input.Uses))
	for _, use := range input.Uses {
		all = append(all, ColumnAuditDetail{Identity: hashColumnIdentity(use.Column), Usage: string(use.Usage), Site: stableSite(use.Site), Reason: string(plan.Reason())})
	}
	canonical, _ := json.Marshal(all)
	digest := sha256.Sum256(canonical)
	completeDigest := hex.EncodeToString(digest[:])
	for _, detail := range all {
		if len(audit.Details) >= columnAuditMaxDetails {
			audit.Truncated = true
			audit.CompleteDigest = completeDigest
			break
		}
		candidate := append(append([]ColumnAuditDetail(nil), audit.Details...), detail)
		candidateAudit := audit
		candidateAudit.Details = candidate
		// Reserve the truncation marker and complete digest at the boundary,
		// so setting them cannot push the final event beyond 32 KiB.
		candidateAudit.Truncated = true
		candidateAudit.CompleteDigest = completeDigest
		encoded, _ := json.Marshal(candidateAudit)
		if len(encoded) > columnAuditMaxBytes {
			audit.Truncated = true
			audit.CompleteDigest = completeDigest
			break
		}
		audit.Details = candidate
	}
	return audit
}

func hashColumnIdentity(column columnauth.ColumnIdentity) string {
	value := strings.Join([]string{column.Relation.DatabaseID, column.Relation.StableObjectID,
		strconv.Itoa(column.Ordinal), column.TypeDigest}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func stableSite(site string) string {
	switch {
	case strings.HasPrefix(site, "target."):
		return "output"
	case site == "join_where":
		return "join_or_where"
	case site == "having", site == "group", site == "sort", site == "limit", site == "offset", site == "security_qual":
		return site
	default:
		return "reference"
	}
}

func reasonFromColumn(reason columnauth.Reason) Reason {
	switch reason {
	case columnauth.ReasonAgentDenied:
		return ReasonAgentDenied
	case columnauth.ReasonStatementDenied:
		return ReasonStatementClassDenied
	case columnauth.ReasonDatasourceUnsupported:
		return ReasonDatasourceUnsupported
	case columnauth.ReasonRelationDenied:
		return ReasonRelationDenied
	case columnauth.ReasonRelationGrantMissing:
		return ReasonRelationGrantMissing
	case columnauth.ReasonIdentityUnproven:
		return ReasonIdentityUnproven
	case columnauth.ReasonColumnGrantMissing:
		return ReasonColumnGrantMissing
	case columnauth.ReasonMaskMeetUndefined:
		return ReasonMaskMeetUndefined
	case columnauth.ReasonMaskCapabilityMissing:
		return ReasonMaskCapabilityMissing
	default:
		return ReasonAuthorizationProofInvalid
	}
}

func PostgresStableObjectID(databaseOID, relationOID uint32) string {
	return fmt.Sprintf("pg:%d:%d", databaseOID, relationOID)
}

func PostgresColumnTypeDigest(typeOID uint32, typmod int32, collationOID uint32) string {
	return fmt.Sprintf("pg:type=%d;typmod=%d;collation=%d", typeOID, typmod, collationOID)
}
