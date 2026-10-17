package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMySQLColumnBindingEndpointRequiresAdminAndPolicyRevision(t *testing.T) {
	fixture := newAdminFixture(t)
	body := `{"id":"mysql-bind-test","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"app.customers","action":"allow"}`
	request := func(method, path, contents, token, etag string) *httptest.ResponseRecorder {
		t.Helper()
		item := httptest.NewRequest(method, path, strings.NewReader(contents))
		item.Header.Set("Content-Type", "application/json")
		if token != "" {
			item.Header.Set("Authorization", token)
		}
		if etag != "" {
			item.Header.Set("If-Match", etag)
		}
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, item)
		return response
	}
	created := request(http.MethodPost, "/api/v1/policies", body, fixture.adminToken, "")
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	etag := created.Header().Get("ETag")
	require.NotEmpty(t, etag)
	path := "/api/v1/policies/mysql-bind-test/bind-mysql"
	bind := `{"schema":"app","table":"customers","grants":[{"column":"id","usage":"output"}]}`
	require.Equal(t, http.StatusUnauthorized, request(http.MethodPost, path, bind, "", etag).Code)
	require.Equal(t, http.StatusPreconditionRequired, request(http.MethodPost, path, bind, fixture.adminToken, "").Code)
	require.Equal(t, http.StatusBadRequest, request(http.MethodPost, path, bind, fixture.adminToken, "*").Code)
	require.Equal(t, http.StatusBadRequest, request(http.MethodPost, path, "{", fixture.adminToken, etag).Code)
	require.Equal(t, http.StatusConflict, request(http.MethodPost, path, bind, fixture.adminToken, etag).Code,
		"the feature-off runtime must reject MySQL enrollment")
}
