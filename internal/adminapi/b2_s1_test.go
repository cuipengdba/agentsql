package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestB2PolicyAPIETagCompatibilityAndWildcardGate(t *testing.T) {
	fixture := newAdminFixture(t)
	request := func(method, path, body string, headers ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", fixture.adminToken)
		req.Header.Set("Content-Type", "application/json")
		for index := 0; index+1 < len(headers); index += 2 {
			req.Header.Add(headers[index], headers[index+1])
		}
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, req)
		return recorder
	}

	createBody := `{"id":"b2-api","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.orders","action":"allow","column_permissions":[{"relation_enrollment_id":"e1","column_ordinal":1,"column_name":"id","column_type_digest":"int8","usage":"output"},{"relation_enrollment_id":"e1","column_ordinal":1,"column_name":"id","column_type_digest":"int8","usage":"reference"}]}`
	created := request(http.MethodPost, "/api/v1/policies", createBody)
	require.Equal(t, http.StatusOK, created.Code)
	etag := created.Header().Get("ETag")
	require.NotEmpty(t, etag)
	got := request(http.MethodGet, "/api/v1/policies/b2-api", "")
	require.Equal(t, http.StatusOK, got.Code)
	require.Equal(t, etag, got.Header().Get("ETag"))

	updateBody := strings.Replace(createBody, `"id":"b2-api",`, "", 1)
	require.Equal(t, http.StatusPreconditionRequired, request(http.MethodPut, "/api/v1/policies/b2-api", updateBody).Code)
	for _, malicious := range []string{"*", "W/" + etag, etag + "," + etag, " " + etag, strings.Replace(etag, "-r1", "-r01", 1)} {
		require.Equal(t, http.StatusBadRequest, request(http.MethodPut, "/api/v1/policies/b2-api", updateBody, "If-Match", malicious).Code, malicious)
	}
	require.Equal(t, http.StatusBadRequest, request(http.MethodPut, "/api/v1/policies/b2-api", updateBody, "If-Match", etag, "If-Match", etag).Code)
	updated := request(http.MethodPut, "/api/v1/policies/b2-api", updateBody, "If-Match", etag)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	require.NotEqual(t, etag, updated.Header().Get("ETag"))
	require.Equal(t, http.StatusPreconditionFailed, request(http.MethodPut, "/api/v1/policies/b2-api", updateBody, "If-Match", etag).Code)

	conflictBody := `{"agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.orders","action":"allow","columns":"email","column_permissions":[{"relation_enrollment_id":"e1","column_ordinal":1,"column_name":"id","column_type_digest":"int8","usage":"output"},{"relation_enrollment_id":"e1","column_ordinal":1,"column_name":"id","column_type_digest":"int8","usage":"reference"}]}`
	require.Equal(t, http.StatusUnprocessableEntity, request(http.MethodPut, "/api/v1/policies/b2-api", conflictBody, "If-Match", updated.Header().Get("ETag")).Code)
	starBody := `{"id":"star","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.orders","action":"allow","columns":"*"}`
	require.Equal(t, http.StatusUnprocessableEntity, request(http.MethodPost, "/api/v1/policies", starBody).Code)

	asymBody := `{"id":"asym","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.orders","action":"allow","column_permissions":[{"relation_enrollment_id":"e1","column_ordinal":1,"column_name":"id","column_type_digest":"int8","usage":"output"}]}`
	asym := request(http.MethodPost, "/api/v1/policies", asymBody)
	require.Equal(t, http.StatusOK, asym.Code)
	var envelope struct {
		Data policyView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(asym.Body.Bytes(), &envelope))
	require.True(t, envelope.Data.LegacyUnrepresentable)
	legacyOnly := `{"agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.orders","action":"allow","columns":"id"}`
	require.Equal(t, http.StatusConflict, request(http.MethodPut, "/api/v1/policies/asym", legacyOnly, "If-Match", asym.Header().Get("ETag")).Code)
}
