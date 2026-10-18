package businessdb

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// This opt-in test targets only an isolated Vastbase G100 V3.0 Build 9 PG-mode
// instance. Its HBA must reject plaintext for the dedicated read-only role.
func TestVastbaseG100VerifiedTLSRealE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_VASTBASE_TLS_TEST") != "1" {
		t.Skip("set AGENTSQL_VASTBASE_TLS_TEST=1 for the isolated Vastbase TLS fixture")
	}
	port, err := strconv.Atoi(os.Getenv("AGENTSQL_VASTBASE_TLS_PORT"))
	require.NoError(t, err)
	password := os.Getenv("AGENTSQL_VASTBASE_TLS_RO_PASSWORD")
	ca := os.Getenv("AGENTSQL_VASTBASE_TLS_CA_FILE")
	wrongCA := os.Getenv("AGENTSQL_VASTBASE_TLS_WRONG_CA_FILE")
	require.NotEmpty(t, password)
	require.NotEmpty(t, ca)
	require.NotEmpty(t, wrongCA)

	ds := model.Datasource{
		ID: "vastbase-v180-tls", DBType: "vastbase", Host: "127.0.0.1", Port: port,
		Database: "vastbase", Username: "agentsql_v180_ro", ConnLimit: 2,
		StmtTimeoutMS: 5_000, RowLimit: 10, TLSMode: "verify-full", TLSCAFile: ca,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	executor, err := NewVastbaseExecutor(ctx, ds, password)
	require.NoError(t, err)
	defer executor.Close()
	result, err := executor.Query(ctx, "SELECT phone FROM agentsql_v060.contacts WHERE id > 0 LIMIT 1", 1)
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)

	ds.TLSCAFile = wrongCA
	bad, err := NewVastbaseExecutor(ctx, ds, password)
	require.Error(t, err, "wrong CA must reject the server")
	require.Nil(t, bad)
	ds.TLSCAFile = ca
	ds.TLSServerName = "wrong.example.test"
	bad, err = NewVastbaseExecutor(ctx, ds, password)
	require.Error(t, err, "wrong hostname must reject the server")
	require.Nil(t, bad)
	ds.TLSServerName = ""
	ds.TLSMode, ds.TLSCAFile = "disable", ""
	bad, err = NewVastbaseExecutor(ctx, ds, password)
	require.Error(t, err, "the server must reject plaintext")
	require.Nil(t, bad)
	ds.TLSMode, ds.TLSCAFile = "verify-full", ca
	ds.TrustServerCertificate = true
	bad, err = NewVastbaseExecutor(ctx, ds, password)
	require.Error(t, err, "unverified certificate trust must be rejected")
	require.Nil(t, bad)
}
