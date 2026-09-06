package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func openTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv(secretEnvironmentVariable, testSecret)
	opened, err := Open(context.Background(), filepath.Join(t.TempDir(), "agentsql.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, opened.Close())
	})
	return opened
}

func createPolicyDependencies(t *testing.T, opened *Store) (model.Agent, model.Datasource) {
	t.Helper()
	_, apiKeyHash, err := GenerateAPIKey()
	require.NoError(t, err)
	agent, err := opened.Agents().Create(context.Background(), model.Agent{
		ID:         "ag_policy",
		Name:       "Policy Agent",
		Status:     "active",
		APIKeyHash: apiKeyHash,
		Level:      "readonly",
	})
	require.NoError(t, err)
	datasource, err := opened.Datasources().Create(context.Background(), model.Datasource{
		ID:            "ds_policy",
		Name:          "Policy Database",
		DBType:        "postgres",
		Host:          "127.0.0.1",
		Port:          5432,
		Database:      "app",
		Username:      "agentsql",
		ConnLimit:     5,
		StmtTimeoutMS: 5000,
		RowLimit:      1000,
	}, "database-password")
	require.NoError(t, err)
	return agent, datasource
}

func pointer[T any](value T) *T {
	return &value
}
