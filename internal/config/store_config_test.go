package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestResolveStoreConfigurationMatrix(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "metadata", "agentsql.db"))
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		}
	}
	tests := []struct {
		name        string
		storeYAML   string
		environment map[string]string
		wantDriver  store.Dialect
		wantPath    string
		wantDSN     string
		wantError   error
		contains    string
	}{
		{name: "legacy shorthand", storeYAML: fmt.Sprintf("  sqlite_path: %q\n", databasePath), wantDriver: store.DialectSQLite, wantPath: databasePath},
		{name: "metadata defaults to sqlite", storeYAML: fmt.Sprintf("  metadata:\n    sqlite_path: %q\n", databasePath), wantDriver: store.DialectSQLite, wantPath: databasePath},
		{name: "legacy and metadata conflict", storeYAML: fmt.Sprintf("  sqlite_path: %q\n  metadata:\n    sqlite_path: %q\n", databasePath, databasePath), wantError: ErrConflictingStoreConfig},
		{name: "no store target", storeYAML: "  {}\n", wantError: ErrMissingSQLitePath},
		{name: "postgres missing dsn", storeYAML: "  metadata:\n    driver: postgres\n", wantError: ErrMissingPostgresDSN},
		{
			name:        "postgres dsn only from environment",
			storeYAML:   "  metadata:\n    driver: postgres\n",
			environment: map[string]string{metadataDSNEnv: "  postgres://env:secret@example/env  "},
			wantDriver:  store.DialectPostgres,
			wantDSN:     "postgres://env:secret@example/env",
		},
		{
			name:        "postgres environment dsn overrides yaml",
			storeYAML:   "  metadata:\n    driver: postgres\n    dsn: postgres://yaml:yaml@example/yaml\n",
			environment: map[string]string{metadataDSNEnv: "  postgres://env:secret@example/env  "},
			wantDriver:  store.DialectPostgres,
			wantDSN:     "postgres://env:secret@example/env",
		},
		{
			name:        "explicit empty environment dsn wins",
			storeYAML:   "  metadata:\n    driver: postgres\n    dsn: postgres://yaml:yaml@example/yaml\n",
			environment: map[string]string{metadataDSNEnv: ""},
			wantError:   ErrMissingPostgresDSN,
		},
		{name: "invalid driver", storeYAML: "  metadata:\n    driver: mysql\n", contains: "unsupported dialect"},
		{
			name:        "driver whitespace and case normalized",
			storeYAML:   "  metadata:\n    driver: '  PoStGrEs  '\n",
			environment: map[string]string{metadataDSNEnv: "postgres://user:secret@example/db"},
			wantDriver:  store.DialectPostgres,
			wantDSN:     "postgres://user:secret@example/db",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := Parse([]byte(configWithStore(test.storeYAML)))
			require.NoError(t, err)
			resolved, err := ResolveStore(&parsed, lookup(test.environment))
			if test.wantError != nil || test.contains != "" {
				require.Error(t, err)
				if test.wantError != nil {
					require.ErrorIs(t, err, test.wantError)
				}
				if test.contains != "" {
					require.ErrorContains(t, err, test.contains)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantDriver, resolved.Metadata.Driver)
			require.Equal(t, test.wantPath, resolved.Metadata.SQLitePath)
			require.Equal(t, test.wantDSN, resolved.Metadata.PostgresDSN)
			require.Equal(t, 10, resolved.Metadata.MaxOpenConns)
			require.Equal(t, 5, resolved.Metadata.MaxIdleConns)
			require.Equal(t, ConfigDuration(30*time.Minute), resolved.Metadata.ConnMaxLifetime)
			require.True(t, resolved.AutoMigrate)
		})
	}
}

func TestResolveStoreLegacyAndMetadataSQLiteAreEquivalent(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	legacy, err := Parse([]byte(configWithStore(fmt.Sprintf("  sqlite_path: %q\n", databasePath))))
	require.NoError(t, err)
	metadata, err := Parse([]byte(configWithStore(fmt.Sprintf("  metadata:\n    sqlite_path: %q\n", databasePath))))
	require.NoError(t, err)

	legacyResolved, err := ResolveStore(&legacy, nil)
	require.NoError(t, err)
	metadataResolved, err := ResolveStore(&metadata, nil)
	require.NoError(t, err)
	require.Equal(t, legacyResolved, metadataResolved)
}

func TestParseStoreKnownFieldsRemainStrict(t *testing.T) {
	tests := map[string]string{
		"unknown root":     configWithStore("  sqlite_path: /tmp/agentsql.db\n") + "unexpected: true\n",
		"unknown metadata": configWithStore("  metadata:\n    sqlite_path: /tmp/agentsql.db\n    unexpected: true\n"),
		"unknown audit":    configWithStore("  sqlite_path: /tmp/agentsql.db\n  audit:\n    unexpected: true\n"),
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(contents))
			require.Error(t, err)
			require.ErrorContains(t, err, "field unexpected not found")
		})
	}
}

func TestResolveStorePoolValidation(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	tests := []struct {
		name       string
		poolYAML   string
		wantOpen   int
		wantIdle   int
		wantMaxAge ConfigDuration
		wantError  string
	}{
		{name: "zero values receive defaults", wantOpen: 10, wantIdle: 5, wantMaxAge: ConfigDuration(30 * time.Minute)},
		{name: "explicit values", poolYAML: "    max_open_conns: 20\n    max_idle_conns: 7\n    conn_max_lifetime: 1h\n", wantOpen: 20, wantIdle: 7, wantMaxAge: ConfigDuration(time.Hour)},
		{name: "negative open", poolYAML: "    max_open_conns: -1\n", wantError: "must not be negative"},
		{name: "negative idle", poolYAML: "    max_idle_conns: -1\n", wantError: "must not be negative"},
		{name: "negative duration", poolYAML: "    conn_max_lifetime: -1s\n", wantError: "must not be negative"},
		{name: "idle exceeds open", poolYAML: "    max_open_conns: 3\n    max_idle_conns: 4\n", wantError: "must not exceed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storeYAML := fmt.Sprintf("  metadata:\n    driver: sqlite\n    sqlite_path: %q\n%s", databasePath, test.poolYAML)
			parsed, err := Parse([]byte(configWithStore(storeYAML)))
			require.NoError(t, err)
			resolved, err := ResolveStore(&parsed, nil)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantOpen, resolved.Metadata.MaxOpenConns)
			require.Equal(t, test.wantIdle, resolved.Metadata.MaxIdleConns)
			require.Equal(t, test.wantMaxAge, resolved.Metadata.ConnMaxLifetime)
		})
	}

	for name, value := range map[string]string{"invalid": "not-a-duration", "without unit": "30"} {
		t.Run(name+" duration", func(t *testing.T) {
			contents := configWithStore(fmt.Sprintf("  metadata:\n    driver: sqlite\n    sqlite_path: %q\n    conn_max_lifetime: %s\n", databasePath, value))
			_, err := Parse([]byte(contents))
			require.Error(t, err)
		})
	}
}

func TestResolveStoreAuditConfiguration(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		}
	}
	tests := []struct {
		name        string
		auditYAML   string
		environment map[string]string
		wantReuse   bool
		wantDSN     string
		wantOpen    int
		wantIdle    int
		wantError   error
		contains    string
	}{
		{name: "audit omitted reuses metadata", wantReuse: true},
		{name: "separate false reuses metadata", auditYAML: "  audit:\n    separate: false\n", wantReuse: true},
		{name: "separate postgres", auditYAML: "  audit:\n    separate: true\n    driver: postgres\n    dsn: postgres://audit:secret@example/audit\n", wantDSN: "postgres://audit:secret@example/audit", wantOpen: 10, wantIdle: 5},
		{name: "separate defaults driver", auditYAML: "  audit:\n    separate: true\n    dsn: postgres://audit:secret@example/audit\n", wantDSN: "postgres://audit:secret@example/audit", wantOpen: 10, wantIdle: 5},
		{name: "separate missing dsn", auditYAML: "  audit:\n    separate: true\n", wantError: ErrMissingAuditDSN},
		{name: "separate sqlite rejected", auditYAML: "  audit:\n    separate: true\n    driver: sqlite\n    dsn: ignored\n", contains: "only postgres is supported"},
		{name: "false with dsn rejected", auditYAML: "  audit:\n    separate: false\n    dsn: postgres://audit:secret@example/audit\n", contains: "require separate=true"},
		{name: "false with explicit empty dsn rejected", auditYAML: "  audit:\n    separate: false\n    dsn: \"\"\n", contains: "require separate=true"},
		{name: "environment overrides yaml", auditYAML: "  audit:\n    separate: true\n    dsn: postgres://yaml:secret@example/audit\n", environment: map[string]string{AuditDSNEnv: "  postgres://env:secret@example/audit  "}, wantDSN: "postgres://env:secret@example/audit", wantOpen: 10, wantIdle: 5},
		{name: "empty environment overrides yaml", auditYAML: "  audit:\n    separate: true\n    dsn: postgres://yaml:secret@example/audit\n", environment: map[string]string{AuditDSNEnv: ""}, wantError: ErrMissingAuditDSN},
		{name: "explicit pool", auditYAML: "  audit:\n    separate: true\n    dsn: postgres://audit:secret@example/audit\n    max_open_conns: 8\n    max_idle_conns: 3\n", wantDSN: "postgres://audit:secret@example/audit", wantOpen: 8, wantIdle: 3},
		{name: "negative open", auditYAML: "  audit:\n    separate: true\n    dsn: postgres://audit:secret@example/audit\n    max_open_conns: -1\n", contains: "must not be negative"},
		{name: "negative idle", auditYAML: "  audit:\n    separate: true\n    dsn: postgres://audit:secret@example/audit\n    max_idle_conns: -1\n", contains: "must not be negative"},
		{name: "idle exceeds open", auditYAML: "  audit:\n    separate: true\n    dsn: postgres://audit:secret@example/audit\n    max_open_conns: 2\n    max_idle_conns: 3\n", contains: "must not exceed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := Parse([]byte(configWithStore(fmt.Sprintf("  sqlite_path: %q\n%s", databasePath, test.auditYAML))))
			require.NoError(t, err)
			resolved, err := ResolveStore(&parsed, lookup(test.environment))
			if test.wantError != nil || test.contains != "" {
				require.Error(t, err)
				if test.wantError != nil {
					require.ErrorIs(t, err, test.wantError)
				}
				if test.contains != "" {
					require.ErrorContains(t, err, test.contains)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantReuse, resolved.Audit.ReuseMetadata)
			if test.wantReuse {
				require.Equal(t, store.DialectSQLite, resolved.Audit.Driver)
				require.Empty(t, resolved.Audit.PostgresDSN)
				return
			}
			require.Equal(t, store.DialectPostgres, resolved.Audit.Driver)
			require.Equal(t, test.wantDSN, resolved.Audit.PostgresDSN)
			require.Equal(t, test.wantOpen, resolved.Audit.MaxOpenConns)
			require.Equal(t, test.wantIdle, resolved.Audit.MaxIdleConns)
		})
	}
}

func TestResolveStoreAutoMigrateDefaultAndExplicitFalse(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	for _, test := range []struct {
		name      string
		field     string
		wantValue bool
	}{
		{name: "default true", wantValue: true},
		{name: "explicit true", field: "  auto_migrate: true\n", wantValue: true},
		{name: "explicit false", field: "  auto_migrate: false\n", wantValue: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := Parse([]byte(configWithStore(fmt.Sprintf("  sqlite_path: %q\n%s", databasePath, test.field))))
			require.NoError(t, err)
			resolved, err := ResolveStore(&parsed, nil)
			require.NoError(t, err)
			require.Equal(t, test.wantValue, resolved.AutoMigrate)
			require.Equal(t, test.wantValue, resolved.MetadataOptions().AutoMigrate)
			require.Equal(t, test.wantValue, resolved.MetadataOptions().Audit.AutoMigrate)
		})
	}
}

func TestLoadStoreFilesystemEffectsDependOnDriver(t *testing.T) {
	t.Run("sqlite creates parent", func(t *testing.T) {
		t.Setenv(metadataDSNEnv, "")
		databasePath := filepath.Join(t.TempDir(), "nested", "metadata", "agentsql.db")
		path := writeConfig(t, configWithStore(fmt.Sprintf("  metadata:\n    driver: sqlite\n    sqlite_path: %q\n", filepath.ToSlash(databasePath))))
		loaded, err := Load(path)
		require.NoError(t, err)
		require.Equal(t, filepath.Clean(databasePath), loaded.Store.Metadata.SQLitePath)
		require.DirExists(t, filepath.Dir(databasePath))
	})

	t.Run("postgres persists only the B2 instance identity beside config", func(t *testing.T) {
		t.Setenv(metadataDSNEnv, "postgres://env:secret@example/agentsql")
		root := t.TempDir()
		configPath := filepath.Join(root, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(configWithStore("  metadata:\n    driver: postgres\n    dsn: postgres://yaml:secret@example/agentsql\n")), 0o600))
		loaded, err := Load(configPath)
		require.NoError(t, err)
		require.Equal(t, store.DialectPostgres, store.Dialect(loaded.Store.Metadata.Driver))
		after, err := os.ReadDir(root)
		require.NoError(t, err)
		require.Equal(t, []string{"config.yaml", "config.yaml.instance-id"}, entryNames(after))
	})
}

func configWithStore(storeYAML string) string {
	return `server:
  http_listen: "127.0.0.1:7780"
  console_enabled: true
store:
` + storeYAML + `defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
