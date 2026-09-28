package businessdb

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"sort"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
)

// b5ClosedArtifact contains only candidate proof material. It has no pool,
// connection, transaction, prepared statement name, or executable closure.
type b5ClosedArtifact struct {
	program       BoundProgram
	frame         PostgresCatalogFrame
	refs          []ClosedRelationRef
	authorization PostgresDMLAuthorization
}

func (runtime *B5PostgresRuntime) analyzeClosed(ctx context.Context, statement b5coordinator.StatementRequest, ordinal int) (b5coordinator.Analysis, error) {
	budget := newB5CatalogBudget(runtime.budgetLimits)
	prepared, err := runtime.executor.BindClosedDML(ctx, BindRequest{RawSQL: statement.SQL, Identity: SemanticIdentity{DatasourceIdentity: enrollmentDatasource(runtime.authorizer)}}, budget)
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	program, frame := prepared.Program(), prepared.Fpre()
	closeErr := prepared.Close(ctx)
	if closeErr != nil {
		return b5coordinator.Analysis{}, closeErr
	}
	facts, err := b5StatementFactsFromSemantic(program.Facts)
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	authorization, err := runtime.authorizer.AuthorizeCandidate(ctx, facts, statement, ordinal)
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	auth := PostgresDMLAuthorization{
		PrincipalID: authorizationPrincipal(authorization.Policies), DatasourceID: facts.Target.DatasourceID,
		Policies: append([]b5dml.Policy(nil), authorization.Policies...), PreliminaryAllowed: authorization.PreliminaryAllowed,
		DatasourceSupported: authorization.DatasourceSupported, ReservedTarget: authorization.ReservedTarget,
		PolicySnapshotDigest: authorization.PolicySnapshotDigest, Attestations: PostgresClosedDMLBinderAttestations(),
	}
	decision, err := AuthorizeB5(program.Facts, b5dml.AuthorizationInput{
		PrincipalID: auth.PrincipalID, DatasourceID: auth.DatasourceID, Dialect: b5dml.DialectPostgreSQL,
		CurrentServerMajor: program.Capability.ServerMajor, Policies: auth.Policies,
		PreliminaryAllowed: auth.PreliminaryAllowed, DatasourceSupported: auth.DatasourceSupported,
		ReservedTarget: auth.ReservedTarget, CatalogConsistent: true, ClosureProven: true,
		PolicySnapshotDigest: auth.PolicySnapshotDigest, ClosureDigest: program.SemanticFactsDigest,
		Attestations: auth.Attestations,
	})
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	if !decision.Allowed() {
		return b5coordinator.Analysis{}, &b5coordinator.Failure{Code: authorizationFailureCode(decision.Reason()), Cause: errors.New(string(decision.Reason()))}
	}
	parsed, err := parseClosedDMLForBinding(statement.SQL, newB5CatalogBudget(runtime.budgetLimits))
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	refs := []ClosedRelationRef{{Schema: parsed.Relation.Schema, Name: parsed.Relation.Name}}
	manifest, err := canonicalB5ClosedManifest(program, frame)
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	tree := sha256.Sum256([]byte(program.SemanticFactsDigest))
	closure := sha256.Sum256([]byte(program.SemanticFactsDigest + "\x00" + frame.Fingerprint))
	return b5coordinator.Analysis{Facts: facts, TreeDigest: tree, ManifestDigest: manifest, ClosureDigest: closure,
		Decision: authorization.Decision, EstimatedRows: 0, EstimatedWork: uint64(len(program.Facts.ColumnUses) + len(program.Facts.WriteTargets) + 1),
		Artifact: &b5ClosedArtifact{program: program, frame: frame, refs: refs, authorization: auth}}, nil
}

const b5ClosedManifestSchema = "agentsql.b5.closed-manifest.v1"

// canonicalB5ClosedManifest seals only stable, catalog-bound proof material.
// Capability collections and semantic collections are normalized by their
// canonical digest functions; random prepared names and transaction-local
// identifiers are intentionally absent.
func canonicalB5ClosedManifest(program BoundProgram, frame PostgresCatalogFrame) ([32]byte, error) {
	if program.Mode != BinderModeCatalogClosedV1 || frame.Fingerprint == "" {
		return [32]byte{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	factsDigest, err := program.Facts.Digest()
	if err != nil {
		return [32]byte{}, err
	}
	capabilityDigest, err := program.Capability.CanonicalDigest()
	if err != nil {
		return [32]byte{}, err
	}
	engineDigest := closedEngineEvidenceDigest(factsDigest, frame.Fingerprint)
	if factsDigest != program.SemanticFactsDigest || capabilityDigest != program.Capability.Digest || engineDigest != program.EngineEvidenceDigest {
		return [32]byte{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	h := sha256.New()
	for _, value := range []string{b5ClosedManifestSchema, factsDigest, frame.Fingerprint, capabilityDigest, engineDigest} {
		writeEDString(h, value)
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, nil
}

func b5StatementFactsFromSemantic(facts SemanticFacts) (b5dml.StatementFacts, error) {
	if len(facts.Relations) != 1 || facts.Identity.DatasourceIdentity == "" {
		return b5dml.StatementFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	relationValue := facts.Relations[0]
	relation := b5dml.RelationIdentity{DatasourceID: relationValue.DatasourceID, DatabaseOID: relationValue.DatabaseOID,
		RelationOID: relationValue.RelationOID, RelationKind: relationValue.Kind, Schema: relationValue.Schema,
		Name: relationValue.Name, CatalogFingerprint: relationValue.CatalogFingerprint}
	result := b5dml.StatementFacts{Dialect: b5dml.DialectPostgreSQL, Action: facts.Action, Shape: b5dml.ShapeSimple, Target: relation}
	for _, value := range facts.WriteTargets {
		if value.RelationOID != relation.RelationOID {
			return b5dml.StatementFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		if value.Kind == b5dml.WriteTargetRow {
			result.Writes = append(result.Writes, b5dml.RowDelete(relation))
			continue
		}
		column := b5dml.ColumnIdentity{Relation: relation, Attnum: value.Attnum, Name: value.Name, TypeOID: value.TypeOID, TypeModifier: value.TypeModifier, CollationOID: value.CollationOID}
		result.Writes = append(result.Writes, b5dml.ColumnWrite(column, value.Source))
		if facts.Action == b5dml.ActionInsert {
			assignment := b5dml.AssignmentExplicitValue
			if value.Source == b5dml.WriteSourceImplicitNull {
				assignment = b5dml.AssignmentOmitted
			}
			result.InsertColumns = append(result.InsertColumns, b5dml.InsertColumnFact{Column: column, Assignment: assignment})
		}
	}
	for _, value := range facts.ColumnUses {
		if value.Usage != SemanticUsageReference || value.RelationOID != relation.RelationOID {
			continue
		}
		column := b5dml.ColumnIdentity{Relation: relation, Attnum: value.Attnum, Name: value.Name, TypeOID: value.TypeOID, TypeModifier: value.TypeModifier, CollationOID: value.CollationOID}
		result.References = append(result.References, b5dml.ColumnReference(column, b5dml.ReferenceExpression))
	}
	return result, nil
}

// PostgresClosedDMLBinderAttestations domain-separates the five supported
// closed-mode builds. These values attest the embedded grammar/query-pack
// contract, not the optional C extension.
func PostgresClosedDMLBinderAttestations() []b5dml.BinderAttestation {
	values := make([]b5dml.BinderAttestation, 0, 5)
	for major := 14; major <= 18; major++ {
		digest := func(label string) string {
			return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("agentsql-b5-closed-%s-pg%d", label, major))))
		}
		values = append(values, b5dml.BinderAttestation{ServerMajor: major, ABI: b5dml.BinderABI,
			BuildHash: digest("build"), ExtensionHash: digest("grammar"), NodeManifestHash: digest("catalog"), AllowlistHash: digest("builtins")})
	}
	return values
}

// bindClosedPlan re-resolves and seals every statement in the already-open
// native write transaction. No raw SQL reaches Execute after this method.
func (capability *b5PGCoordinatorCapability) bindClosedPlan(ctx context.Context, plan b5coordinator.Plan) ([32]byte, error) {
	if capability.tx == nil || !capability.closedMode || plan.ServerMajor < 14 || plan.ServerMajor > 18 {
		return [32]byte{}, errors.New("businessdb: invalid closed B5 transaction")
	}
	for _, statement := range plan.Statements {
		artifact, ok := statement.Artifact().(*b5ClosedArtifact)
		if !ok || artifact == nil || artifact.program.Capability.ServerMajor != plan.ServerMajor {
			return [32]byte{}, errors.New("businessdb: closed B5 capability changed")
		}
		budget := newB5CatalogBudget(capability.runtime.budgetLimits)
		if err := lockClosedRelations(ctx, capability.tx, artifact.frame.Relations, budget); err != nil {
			return [32]byte{}, err
		}
		frame, err := scanClosedCatalog(ctx, capability.tx, artifact.refs, budget)
		if err != nil || frame.Fingerprint != artifact.frame.Fingerprint || !sameFrameRelationOIDs(frame, artifact.frame) {
			return [32]byte{}, errors.Join(err, NewCatalogFailure("AUTH_CATALOG_RACE"))
		}
		if err := rejectClosedDMLIndexes(ctx, capability.tx, frame, budget); err != nil {
			return [32]byte{}, err
		}
		parsed, err := parseClosedDMLForBinding(statement.SQL, budget)
		if err != nil {
			return [32]byte{}, err
		}
		locked, err := resolvePostgresClosedDML(ctx, parsed, frame, plan.DatasourceID,
			func(operatorContext context.Context, name string, left, right uint32, operatorBudget PostgresCatalogBudget) (closedOperatorIdentity, error) {
				return lookupClosedOperator(operatorContext, capability.tx, name, left, right, operatorBudget)
			}, budget)
		if err != nil {
			return [32]byte{}, errors.Join(err, NewCatalogFailure("AUTH_CATALOG_RACE"))
		}
		identity, _, err := readClosedSessionIdentity(ctx, capability.tx, budget)
		if err != nil {
			return [32]byte{}, err
		}
		locked, err = canonicalizeClosedFacts(locked, identity, frame)
		if err != nil || CompareSemanticFacts(artifact.program.Facts, locked) != nil {
			return [32]byte{}, errors.Join(err, NewCatalogFailure("AUTH_CATALOG_RACE"))
		}
		factsDigest, err := locked.Digest()
		if err != nil {
			return [32]byte{}, err
		}
		lockedProgram := BoundProgram{Mode: BinderModeCatalogClosedV1, Facts: locked, SemanticFactsDigest: factsDigest,
			EngineEvidenceDigest: closedEngineEvidenceDigest(factsDigest, frame.Fingerprint), Capability: artifact.program.Capability}
		lockedManifest, err := canonicalB5ClosedManifest(lockedProgram, frame)
		if err != nil || subtle.ConstantTimeCompare(lockedManifest[:], statement.ManifestDigest[:]) != 1 {
			return [32]byte{}, errors.Join(err, NewCatalogFailure("AUTH_CATALOG_RACE"))
		}
		lockedFacts, factsErr := b5StatementFactsFromSemantic(locked)
		if factsErr != nil {
			return [32]byte{}, factsErr
		}
		fresh, refreshErr := capability.runtime.authorizer.AuthorizeCandidate(ctx, lockedFacts,
			b5coordinator.StatementRequest{OperationID: statement.OperationID, SQL: statement.SQL}, statement.Ordinal)
		if refreshErr != nil || fresh.PolicySnapshotDigest == "" || fresh.PolicySnapshotDigest != artifact.authorization.PolicySnapshotDigest || fresh.Decision != statement.Decision {
			return [32]byte{}, errors.Join(refreshErr, &b5coordinator.Failure{Code: b5.ErrorAuthCatalogRace, Cause: errors.New("businessdb: B5 policy snapshot changed before begin")})
		}
		auth := PostgresDMLAuthorization{PrincipalID: authorizationPrincipal(fresh.Policies), DatasourceID: lockedFacts.Target.DatasourceID,
			Policies: append([]b5dml.Policy(nil), fresh.Policies...), PreliminaryAllowed: fresh.PreliminaryAllowed,
			DatasourceSupported: fresh.DatasourceSupported, ReservedTarget: fresh.ReservedTarget,
			PolicySnapshotDigest: fresh.PolicySnapshotDigest, Attestations: PostgresClosedDMLBinderAttestations()}
		auth.PlanDigest = fmt.Sprintf("%x", plan.Digest)
		decision, err := AuthorizeB5(locked, b5dml.AuthorizationInput{PrincipalID: auth.PrincipalID, DatasourceID: auth.DatasourceID,
			Dialect: b5dml.DialectPostgreSQL, CurrentServerMajor: plan.ServerMajor, Policies: auth.Policies,
			PreliminaryAllowed: auth.PreliminaryAllowed, DatasourceSupported: auth.DatasourceSupported, ReservedTarget: auth.ReservedTarget,
			CatalogConsistent: true, ClosureProven: true, PolicySnapshotDigest: auth.PolicySnapshotDigest,
			ClosureDigest: artifact.program.SemanticFactsDigest, PlanDigest: auth.PlanDigest, Attestations: auth.Attestations})
		if err != nil || !decision.Allowed() {
			return [32]byte{}, errors.Join(err, errors.New("businessdb: closed B5 authorization denied"))
		}
		name, err := randomClosedPreparedName()
		if err != nil {
			return [32]byte{}, err
		}
		if _, err := capability.tx.Exec(ctx, "PREPARE "+quoteInternalPreparedName(name)+" AS "+statement.SQL); err != nil {
			return [32]byte{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		for _, oid := range frameRelationOIDs(frame) {
			capability.sealedOIDs[oid] = struct{}{}
		}
		actual, err := readPostgresUserLocks(ctx, capability.tx, budget)
		if err != nil || !sameOIDSet(actual, sortedClosedOIDs(capability.sealedOIDs)) {
			return [32]byte{}, errors.Join(err, NewCatalogFailure("AUTH_BIND_CLOSURE_MISMATCH"))
		}
		capability.prepared[statement.Ordinal] = name
	}
	return plan.Digest, nil
}

func sortedClosedOIDs(values map[uint32]struct{}) []uint32 {
	result := make([]uint32, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// executeClosedLocked is called with capability.mu held and releases it before
// returning. It executes only the random server-side handle sealed at begin.
func (capability *b5PGCoordinatorCapability) executeClosedLocked(ctx context.Context, statement b5coordinator.PlannedStatement) (b5coordinator.StatementResult, error) {
	name, ok := capability.prepared[statement.Ordinal]
	tx := capability.tx
	capability.mu.Unlock()
	if !ok || tx == nil {
		return b5coordinator.StatementResult{}, errors.New("businessdb: closed B5 statement is not sealed")
	}
	tag, err := tx.Exec(ctx, "EXECUTE "+quoteInternalPreparedName(name))
	evidence := sha256.Sum256([]byte(fmt.Sprintf("closed:%s:%d:%s", statement.OperationID, tag.RowsAffected(), name)))
	return b5coordinator.StatementResult{AffectedRows: tag.RowsAffected(), EvidenceDigest: evidence,
		Decision: b5coordinator.DecisionAllow, CapabilityCertain: true}, err
}

var _ b5coordinator.Analyzer = (*B5PostgresRuntime)(nil)
var _ b5coordinator.Engine = (*B5PostgresRuntime)(nil)
