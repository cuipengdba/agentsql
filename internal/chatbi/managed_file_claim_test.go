package chatbi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagedFileClaimNameConflictRollsBackReceiptAndAlias(t *testing.T) {
	service, principal, _, _, _ := serviceFixture(t)
	ctx := context.Background()
	receipt := managedFileReceipt{OperationID: "11111111111111111111111111111111",
		BindingID:      "22222222-2222-4222-8222-222222222222",
		DatasourceName: "agentsql-import-33333333333333333333333333333333",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Filename:       "agentsql-file-123.csv", Created: time.Now().UTC(),
		Sheets: []importedFileSheet{{SheetName: "Sheet1", TableName: "agentsql_file_44444444444444444444444444444444", Rows: 1}}}
	key := managedFileReceiptKey(principal, receipt.Filename)
	require.NoError(t, service.Store.put(ctx, key, receipt, 0))
	nameKey := objectKey{"datasource-name", string(principal.Workspace), string(principal.User.ID), "sales csv"}
	require.NoError(t, service.Store.put(ctx, nameKey, ID("999"), 0))
	var revision int64
	var loaded managedFileReceipt
	revision, err := service.Store.get(ctx, key, &loaded)
	require.NoError(t, err)
	binding := Binding{Datasource: "123", Workspace: principal.Workspace, User: principal.User.ID, Name: "Sales CSV",
		Type: "excel", Dialect: "pg", ResolveName: receipt.DatasourceName, ManagedFileID: receipt.OperationID,
		BindingID: receipt.BindingID, ConnectorToken: receipt.ConnectorToken, AgentKey: "fixture-agent"}
	require.ErrorIs(t, service.Store.claimManagedFileReceipt(ctx, principal, receipt.Filename, revision, loaded, binding), ErrConflict)
	_, err = service.Store.get(ctx, key, &loaded)
	require.NoError(t, err)
	require.Empty(t, loaded.ClaimedDatasource)
	var alias Binding
	_, err = service.Store.get(ctx, bindingKey(principal, binding.Datasource), &alias)
	require.ErrorIs(t, err, ErrNotFound)
}
