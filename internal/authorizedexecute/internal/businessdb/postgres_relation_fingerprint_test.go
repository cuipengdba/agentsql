package businessdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinCatalogKeepsEachTableEnrollmentFingerprint(t *testing.T) {
	frame := PostgresCatalogFrame{ServerVersion: 160000, DatabaseOID: 9,
		Relations: []PostgresRelationIdentity{
			{DatabaseOID: 9, OID: 101, NamespaceOID: 2200, Schema: "public", Name: "sales", Kind: 'r', Persistence: 'p'},
			{DatabaseOID: 9, OID: 102, NamespaceOID: 2200, Schema: "public", Name: "regions", Kind: 'r', Persistence: 'p'},
		},
		Columns: []PostgresColumnIdentity{
			{RelationOID: 101, Attnum: 1, Name: "region", TypeOID: 25},
			{RelationOID: 102, Attnum: 1, Name: "region", TypeOID: 25},
		},
	}
	frame.Fingerprint = fingerprintPostgresCatalog(frame)
	sales := PostgresCatalogFrame{ServerVersion: frame.ServerVersion, DatabaseOID: frame.DatabaseOID,
		Relations: frame.Relations[:1], Columns: frame.Columns[:1]}
	regions := PostgresCatalogFrame{ServerVersion: frame.ServerVersion, DatabaseOID: frame.DatabaseOID,
		Relations: frame.Relations[1:], Columns: frame.Columns[1:]}
	for _, test := range []struct {
		oid  uint32
		want string
	}{{101, fingerprintPostgresCatalog(sales)}, {102, fingerprintPostgresCatalog(regions)}} {
		actual, err := RelationEnrollmentFingerprint(frame, test.oid)
		require.NoError(t, err)
		require.Equal(t, test.want, actual)
		require.NotEqual(t, frame.Fingerprint, actual)
	}
	facts := SemanticFacts{StatementClass: BinderStatementSelect}
	require.NoError(t, sealFactsWithFrame(&facts, frame))
	require.Equal(t, fingerprintPostgresCatalog(sales), facts.Relations[0].CatalogFingerprint)
	require.Equal(t, fingerprintPostgresCatalog(regions), facts.Relations[1].CatalogFingerprint)

	// A view needs its whole closure proof. Never reduce a multi-relation
	// view frame to a base-table fingerprint.
	withView := frame
	withView.Relations = append([]PostgresRelationIdentity(nil), frame.Relations...)
	withView.Relations[1].Kind = 'v'
	withView.Fingerprint = fingerprintPostgresCatalog(withView)
	withViewFingerprint, err := RelationEnrollmentFingerprint(withView, 101)
	require.NoError(t, err)
	require.Equal(t, withView.Fingerprint, withViewFingerprint)
	withDependency := frame
	withDependency.Dependencies = []PostgresDependency{{ClassID: 1259, ObjectID: 101, RefClassID: 1259, RefObjectID: 102}}
	withDependency.Fingerprint = fingerprintPostgresCatalog(withDependency)
	sales.Dependencies = withDependency.Dependencies
	actual, err := RelationEnrollmentFingerprint(withDependency, 101)
	require.NoError(t, err)
	require.Equal(t, fingerprintPostgresCatalog(sales), actual)
	drifted := frame
	drifted.Columns = append([]PostgresColumnIdentity(nil), frame.Columns...)
	drifted.Columns[1].Name = "region_changed"
	drifted.Fingerprint = fingerprintPostgresCatalog(drifted)
	unchangedSales, err := RelationEnrollmentFingerprint(drifted, 101)
	require.NoError(t, err)
	salesWithoutDependency := sales
	salesWithoutDependency.Dependencies = nil
	require.Equal(t, fingerprintPostgresCatalog(salesWithoutDependency), unchangedSales)
	changedRegions, err := RelationEnrollmentFingerprint(drifted, 102)
	require.NoError(t, err)
	require.NotEqual(t, fingerprintPostgresCatalog(regions), changedRegions)
	tampered := frame
	tampered.Fingerprint = "not-the-locked-catalog"
	_, err = RelationEnrollmentFingerprint(tampered, 101)
	require.Error(t, err)
}
