package adminapi

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDMDatasourceVerifiedTLSValidation(t *testing.T) {
	base := datasourceInput{
		ID: "dm-test", Name: "DM test", DBType: "dm", Host: "dm.internal", Port: 5236,
		Database: "APP", Username: "reader", TLSMode: "verify-full",
		TLSServerName: "dm.example.test", TLSCAFile: filepath.Join(t.TempDir(), "ca.pem"),
		StmtTimeoutMS: 5000, ConnLimit: 1, RowLimit: 20,
	}
	datasource, err := datasourceFromInput(base)
	require.NoError(t, err)
	require.Equal(t, "verify-full", datasource.TLSMode)
	require.Equal(t, base.TLSCAFile, datasource.TLSCAFile)
	require.Equal(t, base.TLSServerName, datasource.TLSServerName)

	for name, change := range map[string]func(*datasourceInput){
		"relative CA":   func(input *datasourceInput) { input.TLSCAFile = "ca.pem" },
		"missing CA":    func(input *datasourceInput) { input.TLSCAFile = "" },
		"missing name":  func(input *datasourceInput) { input.TLSServerName = "" },
		"disabled TLS":  func(input *datasourceInput) { input.TLSMode = "disable" },
		"trust bypass":  func(input *datasourceInput) { input.TrustServerCertificate = true },
		"implicit mode": func(input *datasourceInput) { input.TLSMode = "" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			change(&input)
			_, err := datasourceFromInput(input)
			require.Error(t, err)
		})
	}
}
