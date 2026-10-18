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

// This opt-in test uses the isolated KingbaseES V9R1C10 PG-mode instance with
// SSL enabled, a CA-signed certificate and hostnossl rejection rules. It never
// runs against a production database.
func TestKingbaseV9VerifiedTLSRealE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_KINGBASE_TLS_TEST") != "1" {
		t.Skip("set AGENTSQL_KINGBASE_TLS_TEST=1 for the isolated Kingbase TLS fixture")
	}
	port, err := strconv.Atoi(os.Getenv("AGENTSQL_KINGBASE_TLS_PORT"))
	require.NoError(t, err)
	password := os.Getenv("AGENTSQL_KINGBASE_TLS_RO_PASSWORD")
	ca := os.Getenv("AGENTSQL_KINGBASE_TLS_CA_FILE")
	wrongCA := os.Getenv("AGENTSQL_KINGBASE_TLS_WRONG_CA_FILE")
	host := os.Getenv("AGENTSQL_KINGBASE_TLS_HOST")
	require.NotEmpty(t, password)
	require.NotEmpty(t, ca)
	require.NotEmpty(t, wrongCA)
	require.NotEmpty(t, host)

	ds := model.Datasource{
		ID: "kingbase-v170-tls", DBType: "postgres", Host: host, Port: port,
		Database: "kingbase", Username: "agentsql_tls_ro", ConnLimit: 2,
		StmtTimeoutMS: 5_000, RowLimit: 10, TLSMode: "verify-full", TLSCAFile: ca,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	executor, err := NewPostgresExecutor(ctx, ds, password, true)
	require.NoError(t, err)
	defer executor.Close()
	result, err := executor.Query(ctx, "SELECT phone FROM public.agentsql_batch72_verify LIMIT 1", 1)
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)

	ds.TLSCAFile = wrongCA
	bad, err := NewPostgresExecutor(ctx, ds, password, true)
	require.Error(t, err, "wrong CA must reject the server")
	require.Nil(t, bad)
	ds.TLSCAFile = ca
	ds.TLSServerName = "wrong.example.test"
	bad, err = NewPostgresExecutor(ctx, ds, password, true)
	require.Error(t, err, "wrong hostname must reject the server")
	require.Nil(t, bad)
	ds.TLSServerName = ""
	ds.TLSMode = "disable"
	ds.TLSCAFile = ""
	bad, err = NewPostgresExecutor(ctx, ds, password, true)
	require.Error(t, err, "server HBA must reject plaintext")
	require.Nil(t, bad)
	ds.TLSMode = "verify-full"
	ds.TLSCAFile = ca
	ds.TrustServerCertificate = true
	bad, err = NewPostgresExecutor(ctx, ds, password, true)
	require.Error(t, err, "unverified certificate trust must be rejected")
	require.Nil(t, bad)
}
