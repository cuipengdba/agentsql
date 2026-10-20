package businessdb

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// This test uses a separate MongoDB 7.0 instance with a certificate and a
// reader account granted find on app.customers only. It is skipped in CI unless
// that isolated instance is explicitly supplied.
func TestMongoControlledReadRealTLSAndLeastPrivilege(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_MONGO_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_MONGO_HOST for isolated MongoDB 7.0 testing")
	}
	password := os.Getenv("AGENTSQL_TEST_MONGO_RO_PASSWORD")
	ca := os.Getenv("AGENTSQL_TEST_MONGO_CA")
	require.NotEmpty(t, password)
	require.NotEmpty(t, ca)
	port := 27017
	if raw := os.Getenv("AGENTSQL_TEST_MONGO_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	serverName := os.Getenv("AGENTSQL_TEST_MONGO_SERVER_NAME")
	if serverName == "" {
		serverName = host
	}
	datasource := model.Datasource{ID: "mongo-controlled-live", DBType: "mongodb", Host: host, Port: port,
		Database: "app", Username: "agent_reader", TLSMode: "strict", TLSCAFile: ca,
		TLSServerName: serverName, StmtTimeoutMS: 3000, ConnLimit: 2, RowLimit: 100}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	reader, err := NewMongoExecutorWithContext(ctx, datasource, password, true)
	require.NoError(t, err)
	defer reader.Close()
	version, err := reader.ServerVersion(ctx)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(version, "7.0."), version)
	result, err := reader.FindControlled(ctx, MongoFindRequest{Collection: "customers", Fields: []string{"name", "phone"},
		Filter: map[string]string{"name": "alice"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, [][]string{{"alice", "13812345678"}}, result.Rows)
	require.Equal(t, 1, result.RowCount)
	_, err = reader.FindControlled(ctx, MongoFindRequest{Collection: "private_collection", Fields: []string{"name"},
		Filter: map[string]string{"name": "alice"}, Limit: 1})
	require.Error(t, err, "database role must not read an ungranted collection")
	_, err = reader.NativeQuery(ctx, NativeQueryRequest{Command: "INSERTONE", Args: []string{`{"collection":"customers","document":{"name":"forbidden"}}`}})
	require.Error(t, err, "read-only adapter must reject writes")

	wrongCA := datasource
	wrongCA.TLSCAFile = ca + ".missing"
	_, err = NewMongoExecutorWithContext(ctx, wrongCA, password, true)
	require.Error(t, err)
	wrongPassword := datasource
	_, err = NewMongoExecutorWithContext(ctx, wrongPassword, password+"-wrong", true)
	require.Error(t, err)
}
