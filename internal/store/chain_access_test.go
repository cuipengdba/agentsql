package store

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChainAccessSelectsDomainDatabase(t *testing.T) {
	metadataDB := &sql.DB{}
	auditDB := &sql.DB{}
	separate := &Store{
		metaDB: metadataDB, auditDB: auditDB, metaDriver: DialectSQLite,
		auditDriver: DialectPostgres, auditSeparate: true,
	}

	management, err := separate.Chain("management")
	require.NoError(t, err)
	require.Same(t, metadataDB, management.db)
	require.Equal(t, DialectSQLite, management.dialect)
	require.Equal(t, "management", management.domain)

	traffic, err := separate.Chain("traffic")
	require.NoError(t, err)
	require.Same(t, auditDB, traffic.db)
	require.Equal(t, DialectPostgres, traffic.dialect)
	require.Equal(t, "traffic", traffic.domain)

	combined := &Store{
		metaDB: metadataDB, auditDB: metadataDB, metaDriver: DialectSQLite, auditDriver: DialectSQLite,
	}
	traffic, err = combined.Chain("traffic")
	require.NoError(t, err)
	require.Same(t, metadataDB, traffic.db)

	_, err = separate.Chain("invalid")
	require.ErrorContains(t, err, "unsupported domain")
}
