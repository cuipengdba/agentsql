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
)

// Config is the root AgentSQL configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Store    StoreConfig    `yaml:"store"`
	Defaults DefaultsConfig `yaml:"defaults"`
	Theme    ThemeConfig    `yaml:"theme"`
}

// ServerConfig controls the shared HTTP listener and console availability.
type ServerConfig struct {
	HTTPListen     string `yaml:"http_listen"`
	ConsoleEnabled bool   `yaml:"console_enabled"`
}

// StoreConfig controls the local metadata store path.
type StoreConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
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

// Load reads one YAML document, validates all startup settings, and creates the
// parent directory for the SQLite database.
func Load(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var loaded Config
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&loaded); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}

	var trailing any
	err = decoder.Decode(&trailing)
	if err == nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, ErrMultipleYAMLDocuments)
	}
	if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode trailing config data %q: %w", path, err)
	}

	if err := loaded.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}

	loaded.Store.SQLitePath = filepath.Clean(loaded.Store.SQLitePath)
	if err := os.MkdirAll(filepath.Dir(loaded.Store.SQLitePath), 0o750); err != nil {
		return Config{}, fmt.Errorf("create SQLite directory for %q: %w", loaded.Store.SQLitePath, err)
	}

	return loaded, nil
}

// Validate checks every T01 startup invariant and fails closed on invalid input.
func (config Config) Validate() error {
	if strings.TrimSpace(config.Store.SQLitePath) == "" {
		return fmt.Errorf("validate store: %w", ErrMissingSQLitePath)
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
