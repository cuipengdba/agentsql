package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAgentRepositoryGetByAPIKeyHash(t *testing.T) {
	opened := openTestStore(t)
	plaintext, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	created, err := opened.Agents().Create(context.Background(), model.Agent{
		ID:         "ag_key_lookup",
		Name:       "Key Lookup Agent",
		Status:     "active",
		APIKeyHash: hash,
		Level:      "readonly",
	})
	require.NoError(t, err)

	found, err := opened.Agents().GetByAPIKeyHash(context.Background(), hash)
	require.NoError(t, err)
	require.Equal(t, created.ID, found.ID)
	require.Equal(t, hash, found.APIKeyHash)
	require.NotEqual(t, plaintext, found.APIKeyHash)

	_, err = opened.Agents().GetByAPIKeyHash(
		context.Background(),
		HashAPIKey(APIKeyPrefix+"missing"),
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAgentNotFound))
	require.True(t, errors.Is(err, ErrNotFound))

	planRows, err := opened.db.QueryContext(context.Background(), `
EXPLAIN QUERY PLAN
SELECT id
FROM agents INDEXED BY idx_agents_keyhash
WHERE api_key_hash = ?`, hash)
	require.NoError(t, err)
	foundIndex := false
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, planRows.Scan(&id, &parent, &unused, &detail))
		if strings.Contains(detail, "idx_agents_keyhash") {
			foundIndex = true
		}
	}
	require.NoError(t, planRows.Err())
	require.NoError(t, planRows.Close())
	require.True(t, foundIndex)
}
