package authorizedexecute

import (
	"context"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/model"
)

// PostgresPolicyEnrollment is the non-executable catalog identity used by the
// deterministic Live Demo seeder. It contains no connection, prepared handle,
// transaction, SQL result, credential, or authorization decision.
type PostgresPolicyEnrollment struct {
	Mode           string
	StatementClass string
	Action         b5dml.Action
	Relations      []PostgresPolicyRelation
	ColumnUses     []PostgresPolicyColumnUse
	WriteTargets   []PostgresPolicyWriteTarget
}

type PostgresPolicyRelation struct {
	DatabaseOID        uint32
	RelationOID        uint32
	Schema             string
	Name               string
	Kind               byte
	CatalogFingerprint string
}

type PostgresPolicyColumnUse struct {
	RelationOID  uint32
	Attnum       int16
	Name         string
	TypeOID      uint32
	TypeModifier int32
	CollationOID uint32
	Usage        string
}

type PostgresPolicyWriteTarget struct {
	RelationOID  uint32
	Attnum       int16
	Name         string
	TypeOID      uint32
	TypeModifier int32
	CollationOID uint32
	Kind         b5dml.WriteTargetKind
}

// EnrollPostgresPolicySelect chooses the same native-or-closed mode used by
// production, binds the statement without executing it, and returns only the
// exact identities required to seed a formal column policy.
func (gateway *Gateway) EnrollPostgresPolicySelect(ctx context.Context, datasource model.Datasource, secret []byte, rawSQL string, limits Limits) (PostgresPolicyEnrollment, error) {
	capability, err := gateway.ProbePostgresB2Modes(ctx, datasource, secret)
	if err != nil {
		return PostgresPolicyEnrollment{}, err
	}
	opened, err := gateway.open(datasource, secret)
	if err != nil {
		return PostgresPolicyEnrollment{}, fixedExecutionError(err)
	}
	postgres, ok := opened.(*businessdb.PostgresExecutor)
	if !ok {
		return PostgresPolicyEnrollment{}, fmt.Errorf("PostgreSQL policy enrollment capability unavailable")
	}
	budget := NewBudget(limits)
	if capability.NativeAvailable {
		enrollment, enrollErr := postgres.EnrollPostgresSelect(ctx, rawSQL, budget)
		if enrollErr != nil {
			return PostgresPolicyEnrollment{}, StableError(enrollErr)
		}
		return publicNativeSelectPolicyEnrollment(capability.Mode, enrollment)
	}
	prepared, bindErr := postgres.BindClosedSelect(ctx, businessdb.BindRequest{RawSQL: rawSQL, Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID}}, budget)
	if bindErr != nil {
		return PostgresPolicyEnrollment{}, StableError(bindErr)
	}
	facts := prepared.Program().Facts
	err = prepared.Close(ctx)
	if err != nil {
		return PostgresPolicyEnrollment{}, StableError(err)
	}
	return publicPolicyEnrollment(capability.Mode, facts), nil
}

// Native SELECT enrollment is intentionally pre-seal and therefore has plan
// generation zero. It is an administrative identity-discovery artifact, not
// an executable authorization proof, so map the already validated manifest
// and catalog directly instead of pretending it is a sealed BoundProgram.
func publicNativeSelectPolicyEnrollment(mode string, enrollment businessdb.PostgresEnrollment) (PostgresPolicyEnrollment, error) {
	manifest, frame := enrollment.Manifest, enrollment.Catalog
	if mode == "" || manifest.CommandType != "SELECT" || manifest.PlanGeneration != 0 || manifest.Invalidated ||
		manifest.AnalyzedDigest == "" || manifest.DependencyDigest == "" || frame.DatabaseOID == 0 || frame.Fingerprint == "" {
		return PostgresPolicyEnrollment{}, &AuthError{Reason: ReasonBinderIncomplete}
	}
	result := PostgresPolicyEnrollment{Mode: mode, StatementClass: string(businessdb.BinderStatementSelect)}
	for _, relation := range frame.Relations {
		result.Relations = append(result.Relations, PostgresPolicyRelation{DatabaseOID: relation.DatabaseOID,
			RelationOID: relation.OID, Schema: relation.Schema, Name: relation.Name, Kind: relation.Kind,
			CatalogFingerprint: frame.Fingerprint})
	}
	columns := make(map[string]businessdb.PostgresColumnIdentity, len(frame.Columns))
	for _, column := range frame.Columns {
		columns[fmt.Sprintf("%d:%d", column.RelationOID, column.Attnum)] = column
	}
	for _, use := range manifest.Columns {
		column, ok := columns[fmt.Sprintf("%d:%d", use.RelationOID, use.Attnum)]
		if !ok || use.Attnum <= 0 || use.WholeRow || use.ResultComposite || !use.ContributorComplete {
			return PostgresPolicyEnrollment{}, &AuthError{Reason: ReasonBinderIncomplete}
		}
		usage := use.Usage
		if usage == "output" && use.QueryDepth > 0 {
			usage = "reference"
		}
		result.ColumnUses = append(result.ColumnUses, PostgresPolicyColumnUse{RelationOID: use.RelationOID,
			Attnum: use.Attnum, Name: column.Name, TypeOID: column.TypeOID, TypeModifier: column.Typmod,
			CollationOID: column.Collation, Usage: usage})
	}
	return result, nil
}

// EnrollPostgresPolicyDML performs the same mode selection for one planned DML
// statement. It never executes the statement and exposes only exact grant
// elements consumed by the B5 policy store.
func (gateway *Gateway) EnrollPostgresPolicyDML(ctx context.Context, datasource model.Datasource, secret []byte, rawSQL string, limits Limits) (PostgresPolicyEnrollment, error) {
	capability, err := gateway.ProbePostgresB2Modes(ctx, datasource, secret)
	if err != nil {
		return PostgresPolicyEnrollment{}, err
	}
	opened, err := gateway.open(datasource, secret)
	if err != nil {
		return PostgresPolicyEnrollment{}, fixedExecutionError(err)
	}
	postgres, ok := opened.(*businessdb.PostgresExecutor)
	if !ok {
		return PostgresPolicyEnrollment{}, fmt.Errorf("PostgreSQL DML policy enrollment capability unavailable")
	}
	budget := NewBudget(limits)
	if capability.NativeAvailable {
		enrollment, enrollErr := postgres.EnrollPostgresDML(ctx, rawSQL, datasource.ID, budget)
		if enrollErr != nil {
			return PostgresPolicyEnrollment{}, StableError(enrollErr)
		}
		return publicNativeDMLPolicyEnrollment(capability.Mode, enrollment)
	}
	prepared, bindErr := postgres.BindClosedDML(ctx, businessdb.BindRequest{RawSQL: rawSQL, Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID}}, budget)
	if bindErr != nil {
		return PostgresPolicyEnrollment{}, StableError(bindErr)
	}
	facts := prepared.Program().Facts
	err = prepared.Close(ctx)
	if err != nil {
		return PostgresPolicyEnrollment{}, StableError(err)
	}
	return publicPolicyEnrollment(capability.Mode, facts), nil
}

func publicNativeDMLPolicyEnrollment(mode string, enrollment businessdb.PostgresDMLEnrollment) (PostgresPolicyEnrollment, error) {
	manifest, facts := enrollment.Manifest, enrollment.Facts
	if mode == "" || manifest.PlanGeneration != 0 || manifest.Invalidated || manifest.AnalyzedDigest == "" ||
		manifest.DependencyDigest == "" || facts.Action == b5dml.ActionUnknown || facts.Target.DatabaseOID == 0 ||
		facts.Target.RelationOID == 0 || facts.Target.CatalogFingerprint == "" {
		return PostgresPolicyEnrollment{}, &AuthError{Reason: ReasonBinderIncomplete}
	}
	result := PostgresPolicyEnrollment{Mode: mode, Action: facts.Action}
	result.Relations = append(result.Relations, PostgresPolicyRelation{DatabaseOID: facts.Target.DatabaseOID,
		RelationOID: facts.Target.RelationOID, Schema: facts.Target.Schema, Name: facts.Target.Name,
		Kind: facts.Target.RelationKind, CatalogFingerprint: facts.Target.CatalogFingerprint})
	for _, write := range facts.Writes {
		target := PostgresPolicyWriteTarget{RelationOID: write.Relation.RelationOID, Kind: write.Kind}
		if write.Kind == b5dml.WriteTargetColumn {
			target.Attnum, target.Name, target.TypeOID = write.Column.Attnum, write.Column.Name, write.Column.TypeOID
			target.TypeModifier, target.CollationOID = write.Column.TypeModifier, write.Column.CollationOID
		}
		result.WriteTargets = append(result.WriteTargets, target)
	}
	for _, reference := range facts.References {
		if reference.Kind != b5dml.ReferenceColumn || reference.Column.Attnum <= 0 {
			return PostgresPolicyEnrollment{}, &AuthError{Reason: ReasonBinderIncomplete}
		}
		result.ColumnUses = append(result.ColumnUses, PostgresPolicyColumnUse{RelationOID: reference.Relation.RelationOID,
			Attnum: reference.Column.Attnum, Name: reference.Column.Name, TypeOID: reference.Column.TypeOID,
			TypeModifier: reference.Column.TypeModifier, CollationOID: reference.Column.CollationOID, Usage: "reference"})
	}
	return result, nil
}

func publicPolicyEnrollment(mode string, facts businessdb.SemanticFacts) PostgresPolicyEnrollment {
	result := PostgresPolicyEnrollment{Mode: mode, StatementClass: string(facts.StatementClass), Action: facts.Action}
	for _, relation := range facts.Relations {
		result.Relations = append(result.Relations, PostgresPolicyRelation{DatabaseOID: relation.DatabaseOID,
			RelationOID: relation.RelationOID, Schema: relation.Schema, Name: relation.Name, Kind: relation.Kind,
			CatalogFingerprint: relation.CatalogFingerprint})
	}
	for _, column := range facts.ColumnUses {
		result.ColumnUses = append(result.ColumnUses, PostgresPolicyColumnUse{RelationOID: column.RelationOID,
			Attnum: column.Attnum, Name: column.Name, TypeOID: column.TypeOID, TypeModifier: column.TypeModifier,
			CollationOID: column.CollationOID, Usage: string(column.Usage)})
	}
	for _, target := range facts.WriteTargets {
		result.WriteTargets = append(result.WriteTargets, PostgresPolicyWriteTarget{RelationOID: target.RelationOID,
			Attnum: target.Attnum, Name: target.Name, TypeOID: target.TypeOID, TypeModifier: target.TypeModifier,
			CollationOID: target.CollationOID, Kind: target.Kind})
	}
	return result
}
