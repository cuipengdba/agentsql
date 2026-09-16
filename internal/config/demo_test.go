package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDemoDefaultsOffWithoutConfiguration(t *testing.T) {
	t.Setenv(demoEnvironmentVariable, "")
	loaded, err := Parse([]byte(fmt.Sprintf(validConfig, filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")))))

	require.NoError(t, err)
	require.False(t, loaded.DemoEnabled())
	require.Equal(t, DemoConfig{}, loaded.Demo)
}

func TestDemoEnvironmentStrictlyEnables(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		unset   bool
		enabled bool
		wantErr string
	}{
		{name: "unset", unset: true},
		{name: "empty"},
		{name: "exact one", value: "1", enabled: true},
		{name: "true rejected", value: "true", wantErr: "AGENTSQL_DEMO"},
		{name: "yes rejected", value: "yes", wantErr: "AGENTSQL_DEMO"},
		{name: "on rejected", value: "on", wantErr: "AGENTSQL_DEMO"},
		{name: "zero rejected", value: "0", wantErr: "AGENTSQL_DEMO"},
		{name: "whitespace rejected", value: " ", wantErr: "AGENTSQL_DEMO"},
		{name: "arbitrary rejected", value: "demo", wantErr: "AGENTSQL_DEMO"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(demoEnvironmentVariable, test.value)
			if test.unset {
				require.NoError(t, os.Unsetenv(demoEnvironmentVariable))
			}
			loaded, err := Parse([]byte(demoTestConfig(t, "  enabled: false\n")))
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.ErrorContains(t, err, `unset, empty, or exactly "1"`)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.enabled, loaded.DemoEnabled())
		})
	}

	t.Setenv(demoEnvironmentVariable, "")
	loaded, err := Parse([]byte(demoTestConfig(t, "  enabled: true\n")))
	require.NoError(t, err)
	require.True(t, loaded.DemoEnabled(), "an empty environment value must not disable YAML")
}

func TestDemoEnabledDefaultsAndDatasourceAllowlist(t *testing.T) {
	t.Setenv(demoEnvironmentVariable, "")
	loaded, err := Parse([]byte(demoTestConfig(t, "  enabled: true\n")))
	require.NoError(t, err)
	require.Equal(t, []string{DemoDatasourcePG, DemoDatasourceMySQL}, loaded.Demo.AllowedDatasourceIDs)
	require.Equal(t, DemoDefaultQPSPerAgent, loaded.Demo.QPSPerAgent)
	require.Equal(t, DemoDefaultBanner, loaded.Demo.Banner)

	reversed, err := Parse([]byte(demoTestConfig(t, fmt.Sprintf(
		"  enabled: true\n  allowed_datasource_ids: [%s, %s]\n",
		DemoDatasourceMySQL,
		DemoDatasourcePG,
	))))
	require.NoError(t, err)
	require.Equal(t, []string{DemoDatasourceMySQL, DemoDatasourcePG}, reversed.Demo.AllowedDatasourceIDs)

	tests := []struct {
		name string
		ids  string
	}{
		{name: "missing member", ids: DemoDatasourcePG},
		{name: "extra member", ids: DemoDatasourcePG + ", " + DemoDatasourceMySQL + ", ds-extra"},
		{name: "duplicate", ids: DemoDatasourcePG + ", " + DemoDatasourcePG},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(demoTestConfig(t, "  enabled: true\n  allowed_datasource_ids: ["+test.ids+"]\n")))
			require.ErrorContains(t, err, "demo.allowed_datasource_ids")
		})
	}
}

func TestDemoQPSValidation(t *testing.T) {
	t.Setenv(demoEnvironmentVariable, "")
	for _, test := range []struct {
		value int
		want  int
		valid bool
	}{
		{value: 0, want: DemoDefaultQPSPerAgent, valid: true},
		{value: 1, want: 1, valid: true},
		{value: 5, want: 5, valid: true},
		{value: 6},
		{value: -1},
	} {
		t.Run(fmt.Sprintf("qps_%d", test.value), func(t *testing.T) {
			contents := demoTestConfig(t, fmt.Sprintf("  enabled: true\n  qps_per_agent: %d\n", test.value))
			loaded, err := Parse([]byte(contents))
			if !test.valid {
				require.ErrorContains(t, err, "demo.qps_per_agent")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, loaded.Demo.QPSPerAgent)
		})
	}
}

func TestDemoAnchorDateValidation(t *testing.T) {
	t.Setenv(demoEnvironmentVariable, "")
	for _, test := range []struct {
		name    string
		anchor  string
		wantErr bool
	}{
		{name: "empty"},
		{name: "valid leap day", anchor: "2028-02-29"},
		{name: "invalid month", anchor: "2026-13-40", wantErr: true},
		{name: "invalid leap day", anchor: "2026-02-29", wantErr: true},
		{name: "not strict format", anchor: "2026-1-02", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			contents := demoTestConfig(t, "  enabled: true\n  anchor_date: \""+test.anchor+"\"\n")
			loaded, err := Parse([]byte(contents))
			if test.wantErr {
				require.ErrorContains(t, err, "demo.anchor_date")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.anchor, loaded.Demo.AnchorDate)
		})
	}
}

func TestDisabledDemoDoesNotValidateChildFields(t *testing.T) {
	t.Setenv(demoEnvironmentVariable, "")
	loaded, err := Parse([]byte(demoTestConfig(t, `  enabled: false
  allowed_datasource_ids: [unrestricted-production-datasource]
  qps_per_agent: -100
  anchor_date: "not-a-date"
`)))

	require.NoError(t, err)
	require.False(t, loaded.DemoEnabled())
	require.Equal(t, -100, loaded.Demo.QPSPerAgent)
}

func TestDemoDecisionHelpers(t *testing.T) {
	demo := DemoConfig{
		Enabled:              true,
		AllowedDatasourceIDs: []string{DemoDatasourcePG, DemoDatasourceMySQL},
	}
	require.True(t, (Config{Demo: demo}).DemoEnabled())
	require.False(t, (Config{}).DemoEnabled())
	require.True(t, demo.AllowsDatasource(DemoDatasourcePG))
	require.True(t, demo.AllowsDatasource(DemoDatasourceMySQL))
	require.False(t, demo.AllowsDatasource("ds-production"))
	demo.Enabled = false
	require.False(t, demo.AllowsDatasource(DemoDatasourcePG))
	require.Equal(t, DemoDefaultQPSPerAgent, (DemoConfig{}).EffectiveQPS())
	require.Equal(t, 4, (DemoConfig{QPSPerAgent: 4}).EffectiveQPS())
}

func demoTestConfig(t *testing.T, demoBody string) string {
	t.Helper()
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	return fmt.Sprintf(validConfig, databasePath) + "demo:\n" + demoBody
}
