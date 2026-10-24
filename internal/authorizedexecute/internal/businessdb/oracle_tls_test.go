package businessdb

import (
	"encoding/pem"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestOracleVerifiedTLSConfig(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	caPath := filepath.Join(t.TempDir(), "oracle-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	require.NoError(t, os.WriteFile(caPath, certificate, 0o600))
	datasource := model.Datasource{Host: "db.example.test", TLSMode: "verify-full", TLSServerName: "db.example.test", TLSCAFile: caPath}
	config, err := oracleVerifiedTLSConfig(datasource)
	require.NoError(t, err)
	require.Equal(t, "db.example.test", config.ServerName)
	require.NotNil(t, config.RootCAs)
	require.False(t, config.InsecureSkipVerify)

	dsn, err := url.Parse(buildOracleDSN(datasource, "secret"))
	require.NoError(t, err)
	require.Equal(t, "true", dsn.Query().Get("SSL"))
	require.Equal(t, "true", dsn.Query().Get("SSL VERIFY"))
	require.Equal(t, "false", dsn.Query().Get("FAST LOGIN"))

	for _, mutate := range []func(*model.Datasource){
		func(ds *model.Datasource) { ds.TrustServerCertificate = true },
		func(ds *model.Datasource) { ds.TLSServerName = "other.example.test" },
		func(ds *model.Datasource) { ds.TLSCAFile = "relative-ca.pem" },
		func(ds *model.Datasource) { ds.TLSMode = "disable" },
	} {
		invalid := datasource
		mutate(&invalid)
		_, err := oracleVerifiedTLSConfig(invalid)
		require.Error(t, err)
	}
	invalid := datasource
	invalid.TLSCAFile = filepath.Join(t.TempDir(), "missing.pem")
	_, err = oracleVerifiedTLSConfig(invalid)
	require.Error(t, err)
}
