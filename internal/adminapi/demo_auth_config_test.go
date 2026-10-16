package adminapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDemoCredentialsOnlyAppearOnDemoLoginConfig(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		makeFixture func(*testing.T) *adminFixture
		wantDemo    bool
	}{
		{name: "production", makeFixture: newAdminFixture},
		{name: "demo", makeFixture: func(t *testing.T) *adminFixture {
			fixture, _ := newDemoAdminFixture(t)
			return fixture
		}, wantDemo: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := scenario.makeFixture(t)
			response := httptest.NewRecorder()
			fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil))
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			if scenario.wantDemo {
				require.Contains(t, response.Body.String(), `"demo_admin_username":"admin"`)
				require.Contains(t, response.Body.String(), `"demo_admin_password":"password"`)
			} else {
				require.NotContains(t, response.Body.String(), "demo_admin_")
				require.NotContains(t, response.Body.String(), "password")
			}
		})
	}
}
