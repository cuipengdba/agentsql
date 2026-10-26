package bootstrap

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestDerivedSQLBotImportIdentityStableAndScoped(t *testing.T) {
	runtime := &Runtime{secret: []byte("0123456789abcdef0123456789abcdef")}
	parent := store.SQLBotBinding{ID: "parent", Revision: 2, TenantID: "tenant", AgentSQLUserID: "human", SchemaName: "agentsql_files", Enabled: true}
	principal := rbac.Principal{TenantID: "tenant", UserID: "human"}
	name, token, err := runtime.DeriveSQLBotImportIdentity(parent, principal, "agentsql-file-123.xlsx")
	require.NoError(t, err)
	require.Regexp(t, `^agentsql-import-[0-9a-f]{32}$`, name)
	require.Len(t, token, 43)
	againName, againToken, err := runtime.DeriveSQLBotImportIdentity(parent, principal, "agentsql-file-123.xlsx")
	require.NoError(t, err)
	require.Equal(t, name, againName)
	require.Equal(t, token, againToken)
	otherName, otherToken, err := runtime.DeriveSQLBotImportIdentity(parent, principal, "agentsql-file-124.xlsx")
	require.NoError(t, err)
	require.NotEqual(t, name, otherName)
	require.NotEqual(t, token, otherToken)
	parent.Revision++
	otherName, otherToken, err = runtime.DeriveSQLBotImportIdentity(parent, principal, "agentsql-file-123.xlsx")
	require.NoError(t, err)
	require.NotEqual(t, name, otherName)
	require.NotEqual(t, token, otherToken)
	parent.Revision--
	for _, path := range []string{"", "../agentsql-file-123.xlsx", "agentsql-file-0.csv", "agentsql-file-123.xls", "agentsql-file-123.csv/"} {
		_, _, err = runtime.DeriveSQLBotImportIdentity(parent, principal, path)
		require.ErrorIs(t, err, ErrManagedFileStage)
	}
	principal.UserID = "foreign"
	_, _, err = runtime.DeriveSQLBotImportIdentity(parent, principal, "agentsql-file-123.xlsx")
	require.ErrorIs(t, err, ErrManagedFileStage)
	principal.UserID = "human"
	parent.ParentBindingID = "grandparent"
	_, _, err = runtime.DeriveSQLBotImportIdentity(parent, principal, "agentsql-file-123.xlsx")
	require.ErrorIs(t, err, ErrManagedFileStage)
}
