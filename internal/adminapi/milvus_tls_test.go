package adminapi

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMilvusControlledDatasourceTLSValidation(t *testing.T) {
	base := datasourceInput{ID: "milvus-test", Name: "Milvus test", DBType: "milvus", Host: "milvus.internal",
		Port: 19530, Database: "default", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: filepath.Join(t.TempDir(), "ca.pem"), StmtTimeoutMS: 5000, ConnLimit: 1, RowLimit: 20}
	_, err := datasourceFromInput(base)
	require.NoError(t, err)
	for name, change := range map[string]func(*datasourceInput){
		"relative CA":           func(input *datasourceInput) { input.TLSCAFile = "ca.pem" },
		"missing CA":            func(input *datasourceInput) { input.TLSCAFile = "" },
		"disabled TLS":          func(input *datasourceInput) { input.TLSMode = "disable" },
		"trust-any certificate": func(input *datasourceInput) { input.TrustServerCertificate = true },
		"root account":          func(input *datasourceInput) { input.Username = "root" },
		"other database":        func(input *datasourceInput) { input.Database = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			change(&input)
			_, err := datasourceFromInput(input)
			require.Error(t, err)
		})
	}
}
