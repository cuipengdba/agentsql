package chatbi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type managedFileSQLModel struct{ sql string }

func (m managedFileSQLModel) Generate(context.Context, ModelConfig, string, string, []Column) (SQLGeneration, error) {
	return SQLGeneration{SQL: m.sql}, nil
}

func TestAskRejectsHallucinatedManagedFileTableWithoutGatewayQuery(t *testing.T) {
	ctx := context.Background()
	service, principal, fake, _, token := serviceFixture(t)
	const table = "agentsql_file_752687ad0e4cf6a22385c9444fb203c3"
	service.Gateway = &fakeManagedFileGateway{fakeGateway: fake, columns: []Column{
		{Schema: "agentsql_files", Table: table, Name: "month", DataType: "text"},
		{Schema: "agentsql_files", Table: table, Name: "amount", DataType: "numeric"},
	}}
	service.Model = managedFileSQLModel{sql: `SELECT month, amount FROM "agentsql_files".agentsql_file_752687ad0e4cf6a22385c944fb203c3 LIMIT 100`}
	configuration, err := encodeSQLBotExcelConfig("agentsql-file-123.csv", []importedFileSheet{
		{SheetName: "Sheet1", TableName: table, Rows: 4},
	})
	require.NoError(t, err)
	binding := Binding{Datasource: "11", Workspace: principal.Workspace, User: principal.User.ID,
		Name: "Forecast", ResolveName: "agentsql-import-33333333333333333333333333333333",
		Type: "excel", Configuration: configuration, ManagedFileID: "22222222222222222222222222222222",
		Dialect: "pg", BindingID: "22222222-2222-4222-8222-222222222222",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", AgentKey: "fixture-agent"}
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, binding.Datasource), binding, 0))
	chat, err := service.Start(ctx, principal, binding.Datasource, "month amount", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "month amount", check, nil)
	require.ErrorIs(t, err, ErrModel)
	require.Zero(t, fake.queries)
	stored, _, err := service.chat(ctx, principal, chat.ID)
	require.NoError(t, err)
	require.Len(t, stored.Records, 1)
	require.Equal(t, "model", stored.Records[0].ErrorStage)
	require.Nil(t, stored.Records[0].Data)
	require.Empty(t, stored.Records[0].SQL)
}
