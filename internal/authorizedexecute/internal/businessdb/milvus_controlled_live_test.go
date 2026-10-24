package businessdb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// Run against the isolated 2.6.25 fixture with TLS and a collection-scoped
// Search role. The CA and password are mounted from the local test fixture.
func TestMilvusControlledRealTLSAndRBAC(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_MILVUS_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_MILVUS_HOST for isolated Milvus testing")
	}
	passwordBytes, err := os.ReadFile(os.Getenv("AGENTSQL_TEST_MILVUS_PASSWORD_FILE"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	ds := model.Datasource{ID: "milvus-live", DBType: "milvus", Host: host, Port: 19530,
		Database: "default", Username: "agent_reader", TLSMode: "strict", TLSCAFile: os.Getenv("AGENTSQL_TEST_MILVUS_CA"),
		TLSServerName: host, StmtTimeoutMS: 5000}
	request := MilvusSearchRequest{Collection: "events", VectorField: "embedding", Vector: []float32{1, 0, 0, 0},
		Fields: []string{"name", "phone"}, TopK: 2}
	password := strings.TrimSpace(string(passwordBytes))
	result, err := MilvusSearchControlled(ctx, ds, password, request)
	require.NoError(t, err)
	require.Equal(t, 2, result.RowCount)
	require.Equal(t, []string{"name", "phone"}, result.Columns)
	request.Collection = "private_notes"
	_, err = MilvusSearchControlled(ctx, ds, password, request)
	require.Error(t, err)
	request.Collection = "events"
	request.Vector = []float32{1, 0, 0}
	_, err = MilvusSearchControlled(ctx, ds, password, request)
	require.Error(t, err)
	request.Vector = []float32{1, 0, 0, 0}
	ds.TLSServerName = "wrong.invalid"
	badContext, badCancel := context.WithTimeout(ctx, 4*time.Second)
	_, err = MilvusSearchControlled(badContext, ds, password, request)
	badCancel()
	require.Error(t, err)
	ds.TLSServerName = host
	_, err = MilvusSearchControlled(ctx, ds, password, request)
	require.NoError(t, err)
}

func TestMilvusScopedRealTLSAndRBAC(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_MILVUS_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_MILVUS_HOST for isolated Milvus testing")
	}
	passwordBytes, err := os.ReadFile(os.Getenv("AGENTSQL_TEST_MILVUS_PASSWORD_FILE"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	ds := model.Datasource{ID: "milvus-scoped-live", DBType: "milvus", Host: host, Port: 19530,
		Database: "default", Username: "agent_reader", TLSMode: "strict", TLSCAFile: os.Getenv("AGENTSQL_TEST_MILVUS_CA"),
		TLSServerName: host, StmtTimeoutMS: 5000,
		ReadControlJSON: `{"milvus":{"tenant_field":"tenant_id","filter_fields":["status","region"]}}`}
	request := MilvusSearchRequest{Collection: "events_scoped", VectorField: "embedding", Vector: []float32{1, 0, 0, 0},
		Fields: []string{"name", "phone"}, Filter: map[string]string{"region": "east", "status": "active"},
		TenantID: "tenant_default", TopK: 20}
	password := strings.TrimSpace(string(passwordBytes))
	result, err := MilvusSearchControlled(ctx, ds, password, request)
	require.NoError(t, err)
	require.Equal(t, []string{"name", "phone"}, result.Columns)
	require.Equal(t, 1, result.RowCount)
	require.Equal(t, "default-east", result.Rows[0][0])
	request.Filter = map[string]string{"region": "west", "status": "active"}
	result, err = MilvusSearchControlled(ctx, ds, password, request)
	require.NoError(t, err)
	require.Equal(t, 1, result.RowCount)
	require.Equal(t, "default-west", result.Rows[0][0])
	request.Filter = map[string]string{"status": "inactive"}
	result, err = MilvusSearchControlled(ctx, ds, password, request)
	require.NoError(t, err)
	require.Zero(t, result.RowCount)
	request.TenantID = "tenant-b"
	request.Filter = map[string]string{"region": "east"}
	result, err = MilvusSearchControlled(ctx, ds, password, request)
	require.NoError(t, err)
	require.Equal(t, 1, result.RowCount)
	require.Equal(t, "tenant-b-east", result.Rows[0][0])
	request.Filter = map[string]string{"tenant_id": "tenant-a"}
	_, err = MilvusSearchControlled(ctx, ds, password, request)
	require.Error(t, err)
}
