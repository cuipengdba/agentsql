package adminapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatasourceFromInputSQLServerDefaults(t *testing.T) {
	datasource, err := datasourceFromInput(datasourceInput{
		ID: " sqlserver ", Name: " SQL Server 2025 ", DBType: "sqlserver", Host: "db.example.test",
		Database: "app", Username: "agent", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100,
	})
	require.NoError(t, err)
	require.Equal(t, 1433, datasource.Port)
	require.Equal(t, "strict", datasource.TLSMode)
	require.False(t, datasource.TrustServerCertificate)
	require.Equal(t, "sqlserver", datasource.ID)
}

func TestDatasourceFromInputSQLServerRejectsTLSDisableAndStrictTrust(t *testing.T) {
	base := datasourceInput{ID: "sqlserver", Name: "SQL Server", DBType: "sqlserver", Host: "db", Port: 1433, Database: "app", Username: "agent"}
	base.TLSMode = "disable"
	_, err := datasourceFromInput(base)
	require.ErrorContains(t, err, "tls_mode")
	base.TLSMode = "strict"
	base.TrustServerCertificate = true
	_, err = datasourceFromInput(base)
	require.ErrorContains(t, err, "cannot trust")
}

func TestDatasourceFromInputDMOracleRejectsIgnoredTLSOptions(t *testing.T) {
	for _, dialect := range []string{"dm", "oracle"} {
		for _, setTLS := range []func(*datasourceInput){
			func(input *datasourceInput) { input.TLSMode = "strict" },
			func(input *datasourceInput) { input.TLSServerName = "db.example.test" },
			func(input *datasourceInput) { input.TLSCAFile = "ca.pem" },
			func(input *datasourceInput) { input.TrustServerCertificate = true },
		} {
			input := datasourceInput{ID: dialect, Name: dialect, DBType: dialect, Host: "db.example.test", Port: 1521, Database: "APP", Username: "agent"}
			setTLS(&input)
			_, err := datasourceFromInput(input)
			require.ErrorContains(t, err, "TLS options are not supported")
		}
	}
}
