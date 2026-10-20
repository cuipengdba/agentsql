package policy

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestKeyScopeBoundedAndDenyPriority(t *testing.T) {
	for _, invalid := range []string{"*", "db0:*", "db0:ab*", "db16:customers:*", "db0:customers:*:x", "db0:customers:?", "db0:customers:*:*", "db01:customers:1", "db0:customers:1 ", "db0:customers.1"} {
		require.Error(t, ValidateKeyScope(invalid), invalid)
	}
	decision, err := NewResolver().Resolve([]model.Policy{
		{ObjectType: "key", ObjectName: "db0:customers:*", Action: "allow"},
		{ObjectType: "key", ObjectName: "db0:customers:secret", Action: "deny"},
	}, "readonly")
	require.NoError(t, err)
	require.Empty(t, decision.AllowedTables)
	require.True(t, KeyScopeGranted(decision.AllowedKeys, decision.DeniedKeys, 0, "customers:1"))
	require.False(t, KeyScopeGranted(decision.AllowedKeys, decision.DeniedKeys, 0, "customers:secret"))
	require.False(t, KeyScopeGranted(decision.AllowedKeys, decision.DeniedKeys, 1, "customers:1"))
	require.False(t, KeyScopeGranted(decision.AllowedKeys, decision.DeniedKeys, 0, "private:1"))
	require.False(t, KeyScopeGranted(decision.AllowedKeys, decision.DeniedKeys, 0, "customers:*"))
}
