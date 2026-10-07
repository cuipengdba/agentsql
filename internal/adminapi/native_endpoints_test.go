package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestDatasourceTypesEndpoint(t *testing.T) {
	fixture := newAdminFixture(t)
	status, _ := fixture.request(http.MethodGet, "/api/v1/datasource-types", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, body := fixture.request(http.MethodGet, "/api/v1/datasource-types", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	var envelope struct {
		Data []datasourceTypeView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	require.Len(t, envelope.Data, 27)
	require.Equal(t, "postgres", envelope.Data[0].Key)
	require.Equal(t, "weaviate", envelope.Data[26].Key)
	for _, item := range envelope.Data {
		spec, ok := model.TypeSpecFor(item.Key)
		require.True(t, ok)
		require.Equal(t, spec.Category, item.Category)
		require.Equal(t, spec.DefaultPort, item.DefaultPort)
		if model.IsNative(item.Key) {
			require.Equal(t, "nosql-connect", item.Capability)
		} else {
			require.Equal(t, "relational", item.Capability)
		}
	}
}

func TestNativeEndpointsFailClosed(t *testing.T) {
	fixture := newAdminFixture(t)
	_, err := fixture.store.Datasources().Create(context.Background(), model.Datasource{
		ID: "redis-1", Name: "Redis", DBType: "redis", Host: "localhost", Port: 6379,
		ConnLimit: 5, StmtTimeoutMS: 5000, RowLimit: 1000,
	}, "")
	require.NoError(t, err)
	status, _ := fixture.request(http.MethodPost, "/api/v1/datasources/redis-1/native-ping", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, body := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/native-ping", fixture.adminToken, "")
	require.Equal(t, http.StatusBadRequest, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-1/native-ping", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"ok":true`)
	require.Equal(t, 1, fixture.pinger.calls)
	fixture.pinger.err = errors.New("private-host secret-password")
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-1/native-ping", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"ok":false`)
	require.NotContains(t, body, "private-host")
	require.NotContains(t, body, "secret-password")
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/redis-1/native-schema", fixture.adminToken, "")
	require.Equal(t, http.StatusServiceUnavailable, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-1/native-query", fixture.adminToken, `{"command":"GET","args":["item"]}`)
	require.Equal(t, http.StatusServiceUnavailable, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-1/native-query", fixture.adminToken, `{"command":"SET","read_only":false}`)
	require.Equal(t, http.StatusForbidden, status, body)
	action, id := "native.query", "redis-1"
	audits, err := fixture.store.AuditLogs().FilteredPage(context.Background(), model.AuditFilter{Action: &action, DatasourceID: &id}, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), audits.Total)
	for _, entry := range audits.List {
		require.Equal(t, "deny", entry.Decision)
		require.NotContains(t, *entry.DetailsJSON, "item")
	}
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-1/native-query", fixture.adminToken, `{"command":"GET","args":["`+strings.Repeat("x", 4097)+`"]}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-1/native-query", fixture.adminToken, `{"command":"GET","unknown":true}`)
	require.Equal(t, http.StatusBadRequest, status, body)
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/missing/native-schema", fixture.adminToken, "")
	require.Equal(t, http.StatusNotFound, status, body)
}

type fakeNativeBackend struct {
	queryCalls, discoverCalls, versionCalls int
	result                                  executor.NativeQueryResult
	catalog                                 executor.NativeCatalog
	queryErr, discoverErr, versionErr       error
}

func (backend *fakeNativeBackend) NativeQueryRead(_ context.Context, _ model.Datasource, _ executor.NativeQueryRequest) (executor.NativeQueryResult, error) {
	backend.queryCalls++
	return backend.result, backend.queryErr
}
func (backend *fakeNativeBackend) NativeDiscover(_ context.Context, _ model.Datasource) (executor.NativeCatalog, error) {
	backend.discoverCalls++
	return backend.catalog, backend.discoverErr
}
func (backend *fakeNativeBackend) NativeServerVersion(_ context.Context, _ model.Datasource) (string, error) {
	backend.versionCalls++
	return "redis-test", backend.versionErr
}

func TestNativeEndpointsWithReadBridge(t *testing.T) {
	fixture := newAdminFixture(t)
	_, err := fixture.store.Datasources().Create(context.Background(), model.Datasource{
		ID: "redis-bridge", Name: "Redis", DBType: "redis", Host: "localhost", Port: 6379,
		ConnLimit: 5, StmtTimeoutMS: 5000, RowLimit: 1000,
	}, "")
	require.NoError(t, err)
	rows := make([][]string, 120)
	for i := range rows {
		rows[i] = []string{strings.Repeat("v", 5000)}
	}
	backend := &fakeNativeBackend{result: executor.NativeQueryResult{Columns: []string{"value"}, Rows: rows},
		catalog: executor.NativeCatalog{Category: model.CategoryKeyValue, Namespaces: []executor.NativeNamespace{{Name: "db0", Kind: "database", ItemCount: 3}}}}
	fixture.handler, err = NewHandler(Deps{Runtime: fixture.runtime, Config: adminTestConfig("unused.db"),
		AdminUsername: "admin", AdminPassword: "password", TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
		DatasourcePinger: fixture.pinger, Native: backend}, zerolog.New(fixture.logs))
	require.NoError(t, err)
	status, body := fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-query", "", `{"command":"GET","args":["item"]}`)
	require.Equal(t, http.StatusUnauthorized, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-query", fixture.adminToken, `{"command":"GET","args":["item"]}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, 1, backend.queryCalls)
	var envelope struct {
		Data nativeQueryView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	require.True(t, envelope.Data.Truncated)
	require.LessOrEqual(t, len(envelope.Data.Rows), 100)
	require.LessOrEqual(t, len(body), nativeResponseBytes+200)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-query", fixture.adminToken, `{"command":"SET","args":["item","secret"]}`)
	require.Equal(t, http.StatusForbidden, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-query", fixture.adminToken, `{"command":"GET","read_only":false}`)
	require.Equal(t, http.StatusForbidden, status, body)
	require.Equal(t, 1, backend.queryCalls)
	action, id := "native.query", "redis-bridge"
	audits, err := fixture.store.AuditLogs().FilteredPage(context.Background(), model.AuditFilter{Action: &action, DatasourceID: &id}, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(3), audits.Total)
	for _, entry := range audits.List {
		require.NotContains(t, *entry.DetailsJSON, "secret")
	}
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-query", fixture.adminToken, `{"command":"UNKNOWN"}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-query", fixture.adminToken, `{"command":"GET","args":["`+strings.Repeat("x", 4097)+`"]}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	require.Equal(t, 1, backend.queryCalls)
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/redis-bridge/native-schema", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, 1, backend.discoverCalls)
	require.Contains(t, body, `"name":"db0"`)
	backend.catalog.Namespaces = make([]executor.NativeNamespace, 110)
	for i := range backend.catalog.Namespaces {
		backend.catalog.Namespaces[i] = executor.NativeNamespace{Name: "space", Kind: "database"}
	}
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/redis-bridge/native-schema", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	var schemaEnvelope struct {
		Data nativeSchemaView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &schemaEnvelope))
	require.True(t, schemaEnvelope.Data.Truncated)
	require.Len(t, schemaEnvelope.Data.Namespaces, 100)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-ping", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"version":"redis-test"`)
	backend.versionErr = errors.New("private-host")
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/redis-bridge/native-ping", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"version":""`)
	require.NotContains(t, body, "private-host")
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/redis-bridge/native-schema", "", "")
	require.Equal(t, http.StatusUnauthorized, status, body)
	backend.discoverErr = errors.New("private-host")
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/redis-bridge/native-schema", fixture.adminToken, "")
	require.Equal(t, http.StatusBadGateway, status, body)
	require.Contains(t, body, "NATIVE_SCHEMA_FAILED")
	require.NotContains(t, body, "private-host")
	backend.discoverErr = executor.ErrNativeUnsupported
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/redis-bridge/native-schema", fixture.adminToken, "")
	require.Equal(t, http.StatusNotImplemented, status, body)
	require.Contains(t, body, "NATIVE_SCHEMA_UNSUPPORTED")
	status, body = fixture.request(http.MethodGet, "/api/v1/datasources/ds-1/native-schema", fixture.adminToken, "")
	require.Equal(t, http.StatusBadRequest, status, body)
}
