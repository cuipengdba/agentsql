package config

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/stretchr/testify/require"
)

func TestBuildRedactionAssembly(t *testing.T) {
	empty, err := BuildRedactionAssembly(RedactionConfig{})
	require.NoError(t, err)
	require.Equal(t, RedactionAssembly{}, empty)

	keyBytes := []byte("0123456789abcdef0123456789abcdef")
	key := string(keyBytes)
	assembly, err := BuildRedactionAssembly(RedactionConfig{HashKey: key})
	require.NoError(t, err)
	require.Len(t, assembly.PlanOptions, 1)
	require.NotNil(t, assembly.Verify)

	redactor, err := mask.NewRedactor([]mask.Rule{{
		Column: "secret", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash,
	}}, assembly.PlanOptions...)
	require.NoError(t, err)
	_ = redactor
	keyBytes[0] = 'X'
	candidate := "h.b27b652173ab834484d483e570d532d8"
	ok, err := assembly.Verify(1, "value", candidate)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = assembly.Verify(2, "value", candidate)
	require.Error(t, err)
	require.False(t, ok)
}
