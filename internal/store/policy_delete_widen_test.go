package store

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMySQLColumnPolicyDeleteCannotRestoreTableOnlyAccess(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	agent, _ := createPolicyDependencies(t, opened)
	datasource, err := opened.Datasources().Create(ctx, model.Datasource{
		ID: "mysql-delete-fence", Name: "MySQL delete fence", DBType: "mysql",
		Host: "127.0.0.1", Port: 3306, Database: "app", Username: "reader",
		ConnLimit: 2, StmtTimeoutMS: 5000, RowLimit: 100,
	}, "synthetic-password")
	require.NoError(t, err)
	table, err := opened.Policies().Create(ctx, model.Policy{ID: "mysql-table-allow",
		AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table",
		ObjectName: "app.orders", Action: "allow"})
	require.NoError(t, err)
	column, err := opened.Policies().Create(ctx, model.Policy{ID: "mysql-column-allow",
		AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "column",
		ObjectName: "app.orders", Action: "allow"})
	require.NoError(t, err)

	err = opened.Policies().DeleteIfRevision(ctx, column.ID, column.Revision)
	require.ErrorIs(t, err, ErrPolicyDeleteWouldWiden)
	_, err = opened.Policies().Get(ctx, column.ID)
	require.NoError(t, err, "denied deletion must leave the column policy in place")

	require.NoError(t, opened.Policies().DeleteIfRevision(ctx, table.ID, table.Revision))
	require.NoError(t, opened.Policies().DeleteIfRevision(ctx, column.ID, column.Revision))
}
