package authorizedexecute

import (
	"context"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

// PostgresRelationEnrollment is the feature-off S3 enrollment artifact. It is
// immutable request output, not an authorization proof; S4 must persist and
// compare it under the control->business two-phase protocol before execution.
type PostgresRelationEnrollment struct {
	Fingerprint        string
	BinderFingerprint  string
	CatalogFingerprint string
	Relations          []PostgresEnrolledRelation
	Columns            []PostgresEnrolledColumn
}

type PostgresEnrolledRelation struct {
	DatabaseOID, RelationOID, NamespaceOID uint32
	Schema, Name                           string
	Kind                                   byte
	ViewDepth                              int
	StableObjectID                         string
	CatalogFingerprint                     string
}

type PostgresEnrolledColumn struct {
	RelationOID  uint32
	Attnum       int16
	Name         string
	TypeOID      uint32
	CollationOID uint32
	TypeDigest   string
}

// EnrollPostgresSelect performs S3 discovery only. It intentionally does not
// activate protocol 3 or expose the prepared execution capability.
func (gateway *Gateway) EnrollPostgresSelect(ctx context.Context, datasource model.Datasource, secret []byte, rawSQL string, limits Limits) (PostgresRelationEnrollment, error) {
	if datasource.DBType != "postgres" {
		return PostgresRelationEnrollment{}, &AuthError{Reason: ReasonDatabaseFailure}
	}
	if err := ValidatePreParse(rawSQL, nil, limits); err != nil {
		return PostgresRelationEnrollment{}, err
	}
	executor, err := gateway.open(datasource, secret)
	if err != nil {
		return PostgresRelationEnrollment{}, fixedExecutionError(err)
	}
	postgres, ok := executor.(*businessdb.PostgresExecutor)
	if !ok {
		return PostgresRelationEnrollment{}, fmt.Errorf("PostgreSQL enrollment capability unavailable")
	}
	budget := NewBudget(limits)
	enrollment, err := postgres.EnrollPostgresSelect(ctx, rawSQL, budget)
	if err != nil {
		return PostgresRelationEnrollment{}, StableError(err)
	}
	result := PostgresRelationEnrollment{
		Fingerprint: enrollment.Fingerprint, BinderFingerprint: enrollment.BinderFingerprint,
		CatalogFingerprint: enrollment.Catalog.Fingerprint,
		Relations:          make([]PostgresEnrolledRelation, len(enrollment.Catalog.Relations)),
		Columns:            make([]PostgresEnrolledColumn, len(enrollment.Catalog.Columns)),
	}
	for index, relation := range enrollment.Catalog.Relations {
		result.Relations[index] = PostgresEnrolledRelation{
			DatabaseOID: relation.DatabaseOID, RelationOID: relation.OID, NamespaceOID: relation.NamespaceOID,
			Schema: relation.Schema, Name: relation.Name, Kind: relation.Kind, ViewDepth: relation.ViewDepth,
			StableObjectID: PostgresStableObjectID(relation.DatabaseOID, relation.OID), CatalogFingerprint: enrollment.Catalog.Fingerprint,
		}
	}
	for index, column := range enrollment.Catalog.Columns {
		result.Columns[index] = PostgresEnrolledColumn{
			RelationOID: column.RelationOID, Attnum: column.Attnum, Name: column.Name,
			TypeOID: column.TypeOID, CollationOID: column.Collation,
			TypeDigest: PostgresColumnTypeDigest(column.TypeOID, column.Typmod, column.Collation),
		}
	}
	return result, nil
}

var _ businessdb.PostgresCatalogBudget = (*Budget)(nil)
