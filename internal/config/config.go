// Package config loads and validates AgentSQL configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/store"
	"gopkg.in/yaml.v3"
)

var (
	// ErrMissingSQLitePath indicates that store.sqlite_path is empty.
	ErrMissingSQLitePath = errors.New("store.sqlite_path is required")
	// ErrInvalidHTTPListen indicates that server.http_listen is not a valid host and TCP port.
	ErrInvalidHTTPListen = errors.New("server.http_listen must contain a valid host and TCP port")
	// ErrNegativeStatementTimeout indicates that defaults.statement_timeout_ms is negative.
	ErrNegativeStatementTimeout = errors.New("defaults.statement_timeout_ms must not be negative")
	// ErrInvalidDefault indicates that a safety-related default is not positive.
	ErrInvalidDefault = errors.New("row, connection, and QPS defaults must be positive")
	// ErrInvalidTheme indicates that theme.default is neither light nor dark.
	ErrInvalidTheme = errors.New("theme.default must be light or dark")
	// ErrMultipleYAMLDocuments indicates that a configuration file contains trailing YAML documents.
	ErrMultipleYAMLDocuments = errors.New("configuration must contain exactly one YAML document")
	// ErrInvalidEventStreamMaxConnections indicates an SSE connection limit outside 1-1000.
	ErrInvalidEventStreamMaxConnections = errors.New("server.event_stream_max_connections must be between 1 and 1000")
)

// Config is the root AgentSQL configuration.
type Config struct {
	Server              ServerConfig              `yaml:"server"`
	Store               StoreConfig               `yaml:"store"`
	Defaults            DefaultsConfig            `yaml:"defaults"`
	Theme               ThemeConfig               `yaml:"theme"`
	Demo                DemoConfig                `yaml:"demo"`
	Redaction           RedactionConfig           `yaml:"redaction"`
	ColumnAuthorization ColumnAuthorizationConfig `yaml:"column_authorization"`
}

// ColumnAuthorizationConfig is the explicit S5 activation request. Enabled is
// off by default; startup establishes protocol-3 only after every readiness
// probe succeeds. Failed activation keeps the table-level pipeline available.
type ColumnAuthorizationConfig struct {
	Enabled             bool   `yaml:"enabled"`
	InstanceID          string `yaml:"instance_id"`
	LeaseMS             int    `yaml:"lease_ms"`
	HeartbeatIntervalMS int    `yaml:"heartbeat_interval_ms"`
}

// ServerConfig controls the shared HTTP listener and console availability.
type ServerConfig struct {
	HTTPListen                string `yaml:"http_listen"`
	ConsoleEnabled            bool   `yaml:"console_enabled"`
	EventStream               bool   `yaml:"event_stream"`
	EventStreamMaxConnections int    `yaml:"event_stream_max_connections"`
}

// DefaultsConfig contains global execution and capacity limits.
type DefaultsConfig struct {
	StatementTimeoutMS    int `yaml:"statement_timeout_ms"`
	RowLimit              int `yaml:"row_limit"`
	MaxConnsPerDatasource int `yaml:"max_conns_per_datasource"`
	QPSPerAgent           int `yaml:"qps_per_agent"`
}

// ThemeConfig controls the initial console theme.
type ThemeConfig struct {
	Default string `yaml:"default"`
}

// Parse decodes exactly one YAML configuration document, applies strict
// environment switches, and validates it without changing the filesystem.
func Parse(contents []byte) (Config, error) {
	var loaded Config
	loaded.Store.AutoMigrate = true
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&loaded); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	var trailing any
	err := decoder.Decode(&trailing)
	if err == nil {
		return Config{}, fmt.Errorf("decode config: %w", ErrMultipleYAMLDocuments)
	}
	if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode trailing config data: %w", err)
	}
	redactionFields := configuredRedactionFields(contents)
	loaded.Redaction.hashKeySet = redactionFields["hash_key"]
	loaded.Redaction.hashKeysSet = redactionFields["hash_keys"]
	if loaded.Redaction.hashKeysSet && loaded.Redaction.HashKeys == nil {
		return Config{}, fmt.Errorf("validate redaction.hash_keys: value must be an object: %w", ErrInvalidHashKeysManifest)
	}
	if err := validateInlineHashKeys(contents, loaded.Redaction.HashKeys); err != nil {
		return Config{}, fmt.Errorf("validate redaction.hash_keys: %w", err)
	}

	configured := configuredServerFields(contents)
	if !configured["console_enabled"] {
		loaded.Server.ConsoleEnabled = true
	}
	if !configured["event_stream"] {
		loaded.Server.EventStream = true
	}
	if !configured["event_stream_max_connections"] {
		loaded.Server.EventStreamMaxConnections = 100
	}
	storeFields := configuredStoreFields(contents)
	loaded.Store.legacySQLitePathSet = storeFields["sqlite_path"]
	loaded.Store.autoMigrateSet = storeFields["auto_migrate"]
	if loaded.Store.Audit != nil {
		loaded.Store.Audit.configuredFields = configuredAuditFields(contents)
	}
	if err := applyDemoEnvironment(&loaded, os.LookupEnv); err != nil {
		return Config{}, fmt.Errorf("resolve demo config: %w", err)
	}
	if err := defaultAndValidateDemo(&loaded.Demo); err != nil {
		return Config{}, fmt.Errorf("validate demo config: %w", err)
	}
	if err := loaded.validateNonStore(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}
	return loaded, nil
}

func configuredRedactionFields(contents []byte) map[string]bool {
	var document struct {
		Redaction map[string]yaml.Node `yaml:"redaction"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return map[string]bool{}
	}
	configured := make(map[string]bool, len(document.Redaction))
	for field := range document.Redaction {
		configured[field] = true
	}
	return configured
}

// Load reads one YAML document, validates all startup settings, and creates the
// parent directory for the SQLite database.
func Load(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	loaded, err := Parse(contents)
	if err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	resolved, err := ResolveStore(&loaded, os.LookupEnv)
	if err != nil {
		return Config{}, fmt.Errorf("resolve config %q: %w", path, err)
	}
	if resolved.Metadata.Driver == store.DialectSQLite {
		resolved.Metadata.SQLitePath = filepath.Clean(resolved.Metadata.SQLitePath)
		if err := os.MkdirAll(filepath.Dir(resolved.Metadata.SQLitePath), 0o750); err != nil {
			return Config{}, fmt.Errorf("create SQLite directory for %q: %w", resolved.Metadata.SQLitePath, err)
		}
	}
	applyResolvedStore(&loaded, resolved)

	return loaded, nil
}

func configuredAuditFields(contents []byte) map[string]bool {
	var document struct {
		Store struct {
			Audit map[string]yaml.Node `yaml:"audit"`
		} `yaml:"store"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return map[string]bool{}
	}
	configured := make(map[string]bool, len(document.Store.Audit))
	for field := range document.Store.Audit {
		configured[field] = true
	}
	return configured
}

func configuredStoreFields(contents []byte) map[string]bool {
	var document struct {
		Store map[string]yaml.Node `yaml:"store"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return map[string]bool{}
	}
	configured := make(map[string]bool, len(document.Store))
	for field := range document.Store {
		configured[field] = true
	}
	return configured
}

func configuredServerFields(contents []byte) map[string]bool {
	var document struct {
		Server map[string]yaml.Node `yaml:"server"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return map[string]bool{}
	}
	configured := make(map[string]bool, len(document.Server))
	for field := range document.Server {
		configured[field] = true
	}
	return configured
}

// Validate checks every T01 startup invariant and fails closed on invalid input.
func (config Config) Validate() error {
	if err := config.validateNonStore(); err != nil {
		return err
	}
	if _, err := ResolveStore(&config, func(string) (string, bool) { return "", false }); err != nil {
		return err
	}
	return nil
}

func (config Config) validateNonStore() error {
	demo := config.Demo
	if err := defaultAndValidateDemo(&demo); err != nil {
		return fmt.Errorf("validate demo config: %w", err)
	}
	if config.ColumnAuthorization.Enabled && strings.TrimSpace(config.ColumnAuthorization.InstanceID) == "" {
		return fmt.Errorf("validate column_authorization: instance_id is required when enabled")
	}
	if config.ColumnAuthorization.LeaseMS < 0 || config.ColumnAuthorization.HeartbeatIntervalMS < 0 {
		return fmt.Errorf("validate column_authorization: lease and heartbeat interval must not be negative")
	}
	if config.ColumnAuthorization.Enabled && config.ColumnAuthorization.LeaseMS > 0 &&
		config.ColumnAuthorization.HeartbeatIntervalMS >= config.ColumnAuthorization.LeaseMS {
		return fmt.Errorf("validate column_authorization: heartbeat interval must be less than lease")
	}

	listen := strings.TrimSpace(config.Server.HTTPListen)
	_, portText, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("validate server.http_listen %q: %w", listen, errors.Join(ErrInvalidHTTPListen, err))
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("parse server.http_listen port %q: %w", portText, errors.Join(ErrInvalidHTTPListen, err))
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("validate server.http_listen port %d: %w", port, ErrInvalidHTTPListen)
	}
	if config.Server.EventStreamMaxConnections < 1 || config.Server.EventStreamMaxConnections > 1000 {
		return fmt.Errorf("validate server event stream connections: %w", ErrInvalidEventStreamMaxConnections)
	}

	if config.Defaults.StatementTimeoutMS < 0 {
		return fmt.Errorf("validate defaults: %w", ErrNegativeStatementTimeout)
	}
	if config.Defaults.RowLimit <= 0 || config.Defaults.MaxConnsPerDatasource <= 0 || config.Defaults.QPSPerAgent <= 0 {
		return fmt.Errorf("validate defaults: %w", ErrInvalidDefault)
	}

	theme := strings.ToLower(strings.TrimSpace(config.Theme.Default))
	if theme != "light" && theme != "dark" {
		return fmt.Errorf("validate theme.default %q: %w", config.Theme.Default, ErrInvalidTheme)
	}

	return nil
}
