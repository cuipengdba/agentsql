package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatasourceSupportCoversRegistryWithoutClaimingPreviewExecution(t *testing.T) {
	require.Len(t, datasourceTypes, 30)
	for dbType := range datasourceTypes {
		support, ok := SupportFor(dbType)
		require.True(t, ok, dbType)
		require.NotEmpty(t, support.Scope, dbType)
		require.NotEmpty(t, support.EvidenceDoc, dbType)
		_, err := os.Stat(filepath.Join("..", "..", support.EvidenceDoc))
		require.NoError(t, err, dbType)
		switch support.Level {
		case SupportControlledSQL, SupportBoundedSQL, SupportControlledReadTool:
			require.NotEmpty(t, support.AgentTools, dbType)
		case SupportConnectionPreview:
			require.Empty(t, support.AgentTools, dbType)
		default:
			t.Fatalf("unknown support level for %s: %s", dbType, support.Level)
		}
	}
	_, ok := SupportFor("unknown")
	require.False(t, ok)
	require.Equal(t, []string{"find_documents"}, mustSupport(t, "mongodb").AgentTools)
	require.Equal(t, []string{"read_key", "read_hash_fields"}, mustSupport(t, "redis").AgentTools)
	require.Equal(t, []string{"read_key", "read_hash_fields"}, mustSupport(t, "valkey").AgentTools)
	require.Equal(t, []string{"query_analytics"}, mustSupport(t, "clickhouse").AgentTools)
	require.Equal(t, []string{"search_vectors"}, mustSupport(t, "milvus").AgentTools)

	copy := mustSupport(t, "postgres")
	copy.AgentTools[0] = "execute_write"
	require.Equal(t, "query", mustSupport(t, "postgres").AgentTools[0])
}

func mustSupport(t *testing.T, dbType string) DatasourceSupport {
	t.Helper()
	support, ok := SupportFor(dbType)
	require.True(t, ok)
	return support
}
