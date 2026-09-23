package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

var auditChainColumnNames = []string{
	"chain_seq",
	"prev_hash",
	"self_hash",
	"chain_key_version",
	"chain_format_version",
}

func TestSQLiteAuditChainSchemaExpand(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	repository := &ChainStateRepository{repositoryBase: repositoryBase{db: opened.metaDB, dialect: DialectSQLite}}

	inserted, err := opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow"})
	require.NoError(t, err)

	t.Run("audit_logs chain columns remain null", func(t *testing.T) {
		var chainSeq, prevHash, selfHash, chainKeyVersion, chainFormatVersion sql.NullString
		require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT chain_seq, prev_hash, self_hash, chain_key_version, chain_format_version
FROM audit_logs
WHERE id = ?`, inserted.ID).Scan(
			&chainSeq,
			&prevHash,
			&selfHash,
			&chainKeyVersion,
			&chainFormatVersion,
		))
		for name, value := range map[string]sql.NullString{
			"chain_seq":            chainSeq,
			"prev_hash":            prevHash,
			"self_hash":            selfHash,
			"chain_key_version":    chainKeyVersion,
			"chain_format_version": chainFormatVersion,
		} {
			require.False(t, value.Valid, "%s must remain NULL while the chain is disabled", name)
		}
	})

	t.Run("management chain state is disabled", func(t *testing.T) {
		state, err := repository.Get(ctx, "management")
		require.NoError(t, err)
		require.Equal(t, "management", state.ChainID)
		require.Equal(t, "DISABLED", state.Status)
		require.Zero(t, state.HeadSeq)
		require.Zero(t, state.BuildEpoch)
		require.Nil(t, state.Mode)
		require.Nil(t, state.ChainInstanceID)
		require.False(t, state.UpdatedAt.IsZero())
	})

	t.Run("management verification row exists", func(t *testing.T) {
		verification, err := repository.GetVerification(ctx, "management")
		require.NoError(t, err)
		require.Equal(t, "management", verification.ChainID)
		require.Nil(t, verification.ObservedInstanceID)
		require.Nil(t, verification.Result)
	})
}

func TestChainStateRepositoryReadErrors(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	repository := &ChainStateRepository{repositoryBase: repositoryBase{db: opened.metaDB, dialect: DialectSQLite}}

	t.Run("not found", func(t *testing.T) {
		_, err := repository.Get(ctx, "traffic")
		require.ErrorIs(t, err, ErrNotFound)
		_, err = repository.GetVerification(ctx, "traffic")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("nil context", func(t *testing.T) {
		_, err := repository.Get(nil, "management")
		require.ErrorIs(t, err, ErrNilContext)
	})

	t.Run("uninitialized", func(t *testing.T) {
		var uninitialized *ChainStateRepository
		_, err := uninitialized.Get(ctx, "management")
		require.Error(t, err)
	})
}

func TestSQLiteToPostgresManifestExcludesAuditChainDerivedData(t *testing.T) {
	t.Run("derived tables", func(t *testing.T) {
		names := migrationTableNames(sqliteToPostgresTables)
		require.NotContains(t, names, "chain_state")
		require.NotContains(t, names, "chain_verification")
	})

	t.Run("derived audit columns", func(t *testing.T) {
		auditColumns := migrationColumnNames(sqliteToPostgresTableByName(t, "audit_logs"))
		for _, column := range auditChainColumnNames {
			require.NotContains(t, auditColumns, column, "derived column %s must not be copied", column)
		}
	})
}

func TestPostgres18AuditChainMigrationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 audit-chain migration E2E is an integration test")
	}
	ctx := dockerTestContext(t)

	t.Run("combined management", func(t *testing.T) {
		dsn := startPostgres18StoreContainer(t, ctx, "agentsql_chain_combined", "chain-combined-password")
		database, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		require.NoError(t, Migrate(ctx, database, DialectPostgres))

		repository := &ChainStateRepository{repositoryBase: repositoryBase{db: database, dialect: DialectPostgres}}
		assertDisabledChainRows(t, ctx, repository, "management")
		assertPostgresAuditChainColumns(t, ctx, database)
	})

	t.Run("separate management and traffic", func(t *testing.T) {
		metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_chain_metadata", "chain-metadata-password")
		auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_chain_audit", "chain-audit-password")
		metadataDB, err := sql.Open("pgx", metadataDSN)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, metadataDB.Close()) })
		auditDB, err := sql.Open("pgx", auditDSN)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, auditDB.Close()) })

		require.NoError(t, MigrateMetadata(ctx, metadataDB, DialectPostgres, true))
		require.NoError(t, MigrateAudit(ctx, auditDB, DialectPostgres))

		metadataRepository := &ChainStateRepository{repositoryBase: repositoryBase{db: metadataDB, dialect: DialectPostgres}}
		auditRepository := &ChainStateRepository{repositoryBase: repositoryBase{db: auditDB, dialect: DialectPostgres}}
		assertDisabledChainRows(t, ctx, metadataRepository, "management")
		assertDisabledChainRows(t, ctx, auditRepository, "traffic")
		assertPostgresAuditChainColumns(t, ctx, auditDB)
	})
}

func assertDisabledChainRows(
	t *testing.T,
	ctx context.Context,
	repository *ChainStateRepository,
	chainID string,
) {
	t.Helper()
	t.Run(chainID+" chain_state", func(t *testing.T) {
		state, err := repository.Get(ctx, chainID)
		require.NoError(t, err)
		require.Equal(t, "DISABLED", state.Status)
		require.Zero(t, state.HeadSeq)
		require.Zero(t, state.BuildEpoch)
		require.Nil(t, state.Mode)
	})
	t.Run(chainID+" chain_verification", func(t *testing.T) {
		verification, err := repository.GetVerification(ctx, chainID)
		require.NoError(t, err)
		require.Equal(t, chainID, verification.ChainID)
	})
}

func assertPostgresAuditChainColumns(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'audit_logs'
  AND column_name IN ('chain_seq','prev_hash','self_hash','chain_key_version','chain_format_version')
ORDER BY ordinal_position`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	wantTypes := map[string]string{
		"chain_seq":            "bigint",
		"prev_hash":            "text",
		"self_hash":            "text",
		"chain_key_version":    "integer",
		"chain_format_version": "integer",
	}
	seen := make(map[string]struct{}, len(wantTypes))
	for rows.Next() {
		var name, dataType, nullable string
		require.NoError(t, rows.Scan(&name, &dataType, &nullable))
		require.Equal(t, wantTypes[name], dataType, name)
		require.Equal(t, "YES", nullable, "%s must remain nullable in S2a", name)
		seen[name] = struct{}{}
	}
	require.NoError(t, rows.Err())
	for _, name := range auditChainColumnNames {
		_, exists := seen[name]
		require.True(t, exists, "audit_logs column %s is missing", name)
	}
}

func TestChainStateRepositoryHasNoWriteSurface(t *testing.T) {
	repositoryType := reflect.TypeOf((*ChainStateRepository)(nil))
	methods := make([]string, 0, repositoryType.NumMethod())
	for index := 0; index < repositoryType.NumMethod(); index++ {
		methods = append(methods, repositoryType.Method(index).Name)
	}
	require.ElementsMatch(t, []string{"Get", "GetVerification"}, methods)
}
