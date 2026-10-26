package chatbi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedFileSubsetIsAppliedToSchemaAndSQL(t *testing.T) {
	const east = "agentsql_file_11111111111111111111111111111111"
	const west = "agentsql_file_22222222222222222222222222222222"
	binding := Binding{Type: "excel", Dialect: "pg", ManagedFileID: "33333333333333333333333333333333", SelectedTables: west}
	columns := []Column{
		{Schema: "agentsql_files", Table: east, Name: "region"},
		{Schema: "agentsql_files", Table: west, Name: "region"},
		{Schema: "agentsql_files", Table: west, Name: "amount"},
	}
	visible, err := filterManagedFileSchema(binding, columns)
	require.NoError(t, err)
	require.Len(t, visible, 2)
	for _, column := range visible {
		require.Equal(t, west, column.Table)
	}
	_, err = filterManagedFileSchema(binding, columns[:1])
	require.ErrorIs(t, err, ErrGateway, "revocation must hide the whole selected table")

	require.NoError(t, authorizeManagedFileSQL(binding,
		`SELECT region, amount FROM agentsql_files.agentsql_file_22222222222222222222222222222222 WHERE region = 'west' LIMIT 100`))
	for _, sql := range []string{
		`SELECT region FROM agentsql_files.agentsql_file_11111111111111111111111111111111`,
		`SELECT w.region FROM agentsql_files.agentsql_file_22222222222222222222222222222222 w JOIN agentsql_files.agentsql_file_11111111111111111111111111111111 e ON w.region=e.region`,
		`SELECT region FROM agentsql_file_22222222222222222222222222222222`,
		`SELECT pg_catalog.sum(amount) FROM agentsql_files.agentsql_file_22222222222222222222222222222222`,
	} {
		require.ErrorIs(t, authorizeManagedFileSQL(binding, sql), ErrGateway, sql)
	}
	binding.SelectedTables = west + "," + west
	_, err = filterManagedFileSchema(binding, columns)
	require.ErrorIs(t, err, ErrGateway)
}
