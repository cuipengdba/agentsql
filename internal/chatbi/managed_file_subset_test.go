package chatbi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedFileSubsetIsAppliedToSchemaAndSQL(t *testing.T) {
	const east = "agentsql_file_11111111111111111111111111111111"
	const west = "agentsql_file_22222222222222222222222222222222"
	config, err := encodeSQLBotExcelConfig("orders.xlsx", []importedFileSheet{
		{SheetName: "East", TableName: east, Rows: 1},
		{SheetName: "West", TableName: west, Rows: 1},
	})
	require.NoError(t, err)
	binding := Binding{Type: "excel", Dialect: "pg", ManagedFileID: "33333333333333333333333333333333", Configuration: config, SelectedTables: west}
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

func TestManagedFileDefaultSelectionNeverExposesAnotherImport(t *testing.T) {
	const own = "agentsql_file_11111111111111111111111111111111"
	const foreign = "agentsql_file_22222222222222222222222222222222"
	config, err := encodeSQLBotExcelConfig("own.csv", []importedFileSheet{{SheetName: "Sheet1", TableName: own, Rows: 1}})
	require.NoError(t, err)
	binding := Binding{Type: "excel", Dialect: "pg", ManagedFileID: "33333333333333333333333333333333", Configuration: config}
	columns := []Column{
		{Schema: "agentsql_files", Table: own, Name: "amount", DataType: "numeric"},
		{Schema: "agentsql_files", Table: foreign, Name: "private", DataType: "text"},
	}
	visible, err := filterManagedFileSchema(binding, columns)
	require.NoError(t, err)
	require.Equal(t, columns[:1], visible)
	require.NoError(t, authorizeManagedFileSQL(binding, `SELECT amount FROM agentsql_files.`+own+` LIMIT 1`))
	require.ErrorIs(t, authorizeManagedFileSQL(binding, `SELECT private FROM agentsql_files.`+foreign+` LIMIT 1`), ErrGateway)
	_, err = filterManagedFileSchema(binding, columns[1:])
	require.ErrorIs(t, err, ErrGateway)
	binding.SelectedTables = foreign
	_, err = managedFileSelectedTables(binding)
	require.ErrorIs(t, err, ErrGateway)
	binding.SelectedTables = ""
	binding.Configuration = ""
	_, err = managedFileSelectedTables(binding)
	require.ErrorIs(t, err, ErrGateway)
}

func TestGeneratedManagedFileSQLRejectsHallucinatedRelationBeforeGateway(t *testing.T) {
	const table = "agentsql_file_752687ad0e4cf6a22385c9444fb203c3"
	binding := Binding{Type: "excel", Dialect: "pg", ManagedFileID: "33333333333333333333333333333333"}
	columns := []Column{{Schema: "agentsql_files", Table: table, Name: "month", DataType: "text"},
		{Schema: "agentsql_files", Table: table, Name: "amount", DataType: "numeric"}}
	require.ErrorIs(t, validateGeneratedManagedFileSQL(binding, columns,
		`SELECT month, amount FROM "agentsql_files".agentsql_file_752687ad0e4cf6a22385c944fb203c3 LIMIT 100`), ErrModel)
	require.NoError(t, validateGeneratedManagedFileSQL(binding, columns,
		`SELECT month, amount FROM "agentsql_files".`+table+` ORDER BY month ASC LIMIT 100`))
	require.ErrorIs(t, validateGeneratedManagedFileSQL(binding, columns,
		`SELECT month, amount FROM public.`+table+` LIMIT 100`), ErrModel)
}
