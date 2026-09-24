package columnauth

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestB2ScopeIsSelectOnly(t *testing.T) {
	require.True(t, InScope(model.StmtType("SELECT")))
	for _, statement := range []model.StmtType{"INSERT", "UPDATE", "DELETE", "MERGE", "DDL", "ADMIN", "UNKNOWN"} {
		require.False(t, InScope(statement), statement)
	}
	require.Equal(t, 2, MetadataProtocol)
	require.Equal(t, 3, ExecutionProtocol)
}
