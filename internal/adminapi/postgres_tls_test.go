package adminapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPostgresTLSUpdateReplacesCertificateSettings(t *testing.T) {
	fixture := newAdminFixture(t)
	path := "/api/v1/datasources/" + fixture.datasource.ID
	status, body := fixture.request(http.MethodPut, path, fixture.adminToken,
		`{"tls_mode":"verify-full","tls_ca_file":"/etc/agentsql/old-ca.crt","tls_server_name":"db.example.test"}`)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(http.MethodPut, path, fixture.adminToken,
		`{"tls_mode":"verify-full","tls_ca_file":"/etc/agentsql/new-ca.crt"}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err := fixture.store.Datasources().Get(context.Background(), fixture.datasource.ID)
	require.NoError(t, err)
	require.Equal(t, "/etc/agentsql/new-ca.crt", stored.TLSCAFile)
	require.Empty(t, stored.TLSServerName)
	status, body = fixture.request(http.MethodPut, path, fixture.adminToken,
		`{"tls_mode":"disable"}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err = fixture.store.Datasources().Get(context.Background(), fixture.datasource.ID)
	require.NoError(t, err)
	require.Equal(t, "disable", stored.TLSMode)
	require.Empty(t, stored.TLSCAFile)
	require.Empty(t, stored.TLSServerName)
}

func TestDatasourceFromInputPostgresVerifiedTLS(t *testing.T) {
	input := datasourceInput{
		ID: "kingbase-pg", Name: "Kingbase V9 PG", DBType: "postgres",
		Host: "db.internal.test", Port: 54321, Database: "kingbase", Username: "reader",
		TLSMode: "verify-full", TLSCAFile: "/etc/agentsql/certs/root.crt",
		TLSServerName: "db.internal.test",
	}
	datasource, err := datasourceFromInput(input)
	require.NoError(t, err)
	require.Equal(t, "verify-full", datasource.TLSMode)
	require.Equal(t, "/etc/agentsql/certs/root.crt", datasource.TLSCAFile)
	require.Equal(t, "db.internal.test", datasource.TLSServerName)
	require.False(t, datasource.TrustServerCertificate)
}

func TestDatasourceFromInputPostgresRejectsUnverifiedTLSOptions(t *testing.T) {
	base := datasourceInput{
		ID: "pg", Name: "PG", DBType: "postgres", Host: "db.internal.test",
		Database: "app", Username: "reader",
	}
	for _, test := range []struct {
		name string
		set  func(*datasourceInput)
	}{
		{"missing CA", func(input *datasourceInput) { input.TLSMode = "verify-full" }},
		{"relative CA", func(input *datasourceInput) { input.TLSMode = "verify-full"; input.TLSCAFile = "ca.crt" }},
		{"unverified trust", func(input *datasourceInput) {
			input.TLSMode = "verify-full"
			input.TLSCAFile = "ca.crt"
			input.TrustServerCertificate = true
		}},
		{"disabled with CA", func(input *datasourceInput) { input.TLSMode = "disable"; input.TLSCAFile = "ca.crt" }},
		{"implicit mode with name", func(input *datasourceInput) { input.TLSServerName = "db.internal.test" }},
		{"require without identity check", func(input *datasourceInput) { input.TLSMode = "require" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.set(&input)
			_, err := datasourceFromInput(input)
			require.Error(t, err)
		})
	}
}

func TestDatasourceFromInputHighGoRejectsUnverifiedTLS(t *testing.T) {
	_, err := datasourceFromInput(datasourceInput{
		ID: "hg", Name: "HighGo", DBType: "highgo", Host: "db.internal.test",
		Database: "app", Username: "reader", TLSMode: "verify-full", TLSCAFile: "ca.crt",
	})
	require.ErrorContains(t, err, "HighGo TLS")
}

func TestDatasourceFromInputVastbaseVerifiedTLS(t *testing.T) {
	input := datasourceInput{
		ID: "vastbase-g100", Name: "Vastbase G100 Build 9", DBType: "vastbase",
		Host: "vastbase.internal.test", Database: "vastbase", Username: "reader",
		TLSMode: "verify-full", TLSCAFile: "/etc/agentsql/certs/vastbase-ca.crt",
		TLSServerName: "vastbase.internal.test",
	}
	datasource, err := datasourceFromInput(input)
	require.NoError(t, err)
	require.Equal(t, "verify-full", datasource.TLSMode)
	require.Equal(t, input.TLSCAFile, datasource.TLSCAFile)
	require.Equal(t, input.TLSServerName, datasource.TLSServerName)
	require.False(t, datasource.TrustServerCertificate)

	for _, change := range []func(*datasourceInput){
		func(in *datasourceInput) { in.TLSCAFile = "" },
		func(in *datasourceInput) { in.TLSCAFile = "relative.crt" },
		func(in *datasourceInput) { in.TLSMode = "require" },
		func(in *datasourceInput) { in.TrustServerCertificate = true },
	} {
		invalid := input
		change(&invalid)
		_, err := datasourceFromInput(invalid)
		require.Error(t, err)
	}
}
