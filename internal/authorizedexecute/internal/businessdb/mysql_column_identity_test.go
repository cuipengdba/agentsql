package businessdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMySQLColumnIdentityInspectorRequiresProcessOnlyAccount(t *testing.T) {
	valid := []string{"GRANT PROCESS ON *.* TO `identity`@`%`"}
	require.True(t, mysqlProcessOnlyGrants(valid))
	require.True(t, mysqlProcessOnlyGrants(append(valid, "GRANT USAGE ON *.* TO `identity`@`%`")))
	for _, grants := range [][]string{
		nil,
		{"GRANT USAGE ON *.* TO `identity`@`%`"},
		{"GRANT PROCESS, SELECT ON *.* TO `identity`@`%`"},
		{"GRANT PROCESS ON *.* TO `identity`@`%` WITH GRANT OPTION"},
		{"GRANT ALL PRIVILEGES ON *.* TO `identity`@`%`"},
		{"GRANT PROCESS ON *.* TO `identity`@`%`", "GRANT SELECT ON app.* TO `identity`@`%`"},
	} {
		require.False(t, mysqlProcessOnlyGrants(grants), "%v", grants)
	}
	require.True(t, mysqlColumnIdentityIdentifier("app_1"))
	for _, name := range []string{"", "app/customers", "app;DROP", "white space", "9starts_with_digit"} {
		require.False(t, mysqlColumnIdentityIdentifier(name), name)
	}
}
