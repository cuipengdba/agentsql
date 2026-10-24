package policy

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestHashFieldsRequireExactKeyAndEveryField(t *testing.T) {
	policies := []model.Policy{
		{ObjectType: "key", ObjectName: "db0:customers:*", Action: "allow"},
		{ObjectType: "key_field", ObjectName: "db0:customers:1#phone", Action: "allow"},
	}
	decision, err := NewResolver().Resolve(policies, "readonly")
	require.NoError(t, err)
	require.False(t, HashFieldsGranted(decision, 0, "customers:1", []string{"phone"}), "a prefix grant must not authorize Hash values")

	policies = append(policies, model.Policy{ObjectType: "key", ObjectName: "db0:customers:1", Action: "allow"})
	decision, err = NewResolver().Resolve(policies, "readonly")
	require.NoError(t, err)
	require.True(t, HashFieldsGranted(decision, 0, "customers:1", []string{"phone"}))
	require.False(t, HashFieldsGranted(decision, 0, "customers:1", []string{"phone", "secret"}))
	require.False(t, HashFieldsGranted(decision, 1, "customers:1", []string{"phone"}))

	policies = append(policies, model.Policy{ObjectType: "key", ObjectName: "db0:customers:1", Action: "deny"})
	decision, err = NewResolver().Resolve(policies, "readonly")
	require.NoError(t, err)
	require.False(t, HashFieldsGranted(decision, 0, "customers:1", []string{"phone"}))
}

func TestHashFieldPolicyRejectsWildcardAndSQLConstraints(t *testing.T) {
	for _, scope := range []string{"db0:customers:*#phone", "db0:customers:1#*", "db0:customers:1#phone#other", "db16:customers:1#phone"} {
		_, _, err := ParseHashFieldScope(scope)
		require.Error(t, err, scope)
	}
	_, err := NewResolver().Resolve([]model.Policy{{ObjectType: "key_field", ObjectName: "db0:customers:1#phone", Action: "deny"}}, "readonly")
	require.Error(t, err)
	columns := "phone"
	_, err = NewResolver().Resolve([]model.Policy{{ObjectType: "key_field", ObjectName: "db0:customers:1#phone", Columns: &columns, Action: "allow"}}, "readonly")
	require.Error(t, err)
}
