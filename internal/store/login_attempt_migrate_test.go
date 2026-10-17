package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSharedLoginBudgetSQLiteMigrationAndDown(t *testing.T) {
	for _, scenario := range []struct {
		name, directory string
		separate        bool
		previous, next  int
	}{
		{name: "combined", directory: "migrations/sqlite", previous: 21, next: 22},
		{name: "metadata", directory: "migrations/metadata/sqlite", separate: true, previous: 20, next: 21},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			database := openSQLiteMigrationTestDB(t)
			migrateSQLiteThroughVersion(t, ctx, database, scenario.directory, scenario.previous)
			require.NotContains(t, businessTableNames(t, database), "login_attempt_budgets")
			require.NoError(t, MigrateMetadata(ctx, database, DialectSQLite, scenario.separate))
			current, latest, err := MetadataMigrationVersions(ctx, database, DialectSQLite, scenario.separate)
			require.NoError(t, err)
			require.Equal(t, scenario.next, current)
			require.Equal(t, scenario.next, latest)
			require.Equal(t, []string{"key_hash", "started_at", "attempts"}, tableColumnNames(t, database, "login_attempt_budgets"))
			require.NoError(t, RollbackMetadataMigration(ctx, database, DialectSQLite, scenario.separate, scenario.next))
			require.NotContains(t, businessTableNames(t, database), "login_attempt_budgets")
			current, _, err = MetadataMigrationVersions(ctx, database, DialectSQLite, scenario.separate)
			require.NoError(t, err)
			require.Equal(t, scenario.previous, current)
		})
	}
}
