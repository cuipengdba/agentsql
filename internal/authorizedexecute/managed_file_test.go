package authorizedexecute

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestManagedFileProvisionerRejectsSharedOrMismatchedTargets(t *testing.T) {
	reader := model.Datasource{ID: "managed-read", DBType: "postgres", Host: "db.example", Port: 5432,
		Database: "managed", Username: "file_reader", TLSMode: "verify-full", TLSCAFile: "/trusted/ca.pem"}
	writer := reader
	writer.ID = "managed-provisioner"
	writer.Username = "file_writer"
	provisioner, err := NewManagedFileProvisioner(reader, writer, "secret")
	require.NoError(t, err)
	require.NotNil(t, provisioner)
	for _, mutate := range []func(*model.Datasource){
		func(d *model.Datasource) { d.ID = reader.ID },
		func(d *model.Datasource) { d.Username = reader.Username },
		func(d *model.Datasource) { d.Host = "another-host" },
		func(d *model.Datasource) { d.Port = 5433 },
		func(d *model.Datasource) { d.Database = "other" },
		func(d *model.Datasource) { d.TLSMode = "disable" },
		func(d *model.Datasource) { d.DBType = "vastbase" },
	} {
		candidate := writer
		mutate(&candidate)
		_, err := NewManagedFileProvisioner(reader, candidate, "secret")
		require.ErrorIs(t, err, ErrManagedFileTarget)
	}
	_, err = NewManagedFileProvisioner(reader, writer, "")
	require.ErrorIs(t, err, ErrManagedFileTarget)
	reader.TLSMode, writer.TLSMode = "", ""
	_, err = NewManagedFileProvisioner(reader, writer, "secret")
	require.ErrorIs(t, err, ErrManagedFileTarget)
	_, err = provisioner.Stage(context.Background(), "../../fake", nil)
	require.Error(t, err, "invalid operation ID must fail before opening a connection")
	require.ErrorIs(t, (*ManagedFileProvisioner)(nil).Drop(context.Background(), "", 1), ErrManagedFileTarget)
	require.ErrorIs(t, (*ManagedFileProvisioner)(nil).GrantRead(context.Background(), "", 1), ErrManagedFileTarget)
	require.Error(t, provisioner.GrantRead(context.Background(), "../../fake", 1), "invalid operation ID must fail before opening a connection")
}
