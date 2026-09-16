package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"gopkg.in/yaml.v3"
)

const metadataDSNEnv = "AGENTSQL_STORE_METADATA_DSN"

var (
	ErrConflictingStoreConfig = errors.New("store.sqlite_path and store.metadata are mutually exclusive")
	ErrMissingPostgresDSN     = errors.New("store.metadata.dsn is required for postgres")
	ErrUnsupportedAuditStore  = errors.New("separate audit store is not supported until T28b-1")
)

// StoreConfig supports both the v0.1 SQLite shorthand and the extensible
// metadata/audit form. SQLitePath must remain compatible with programmatic
// StoreConfig{SQLitePath: ...} construction.
type StoreConfig struct {
	SQLitePath string               `yaml:"sqlite_path"`
	Metadata   *MetadataStoreConfig `yaml:"metadata"`
	Audit      *AuditStoreConfig    `yaml:"audit"`

	legacySQLitePathSet bool
}

type MetadataStoreConfig struct {
	Driver          string         `yaml:"driver"`
	SQLitePath      string         `yaml:"sqlite_path"`
	DSN             string         `yaml:"dsn"`
	MaxOpenConns    int            `yaml:"max_open_conns"`
	MaxIdleConns    int            `yaml:"max_idle_conns"`
	ConnMaxLifetime ConfigDuration `yaml:"conn_max_lifetime"`
}

type AuditStoreConfig struct {
	Separate bool `yaml:"separate"`
}

// ConfigDuration is a YAML string containing a Go duration such as 30m or 1h.
type ConfigDuration time.Duration

func (duration *ConfigDuration) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Tag != "!!str" {
		return fmt.Errorf("duration must be a Go duration string with a unit, such as 30m or 1h")
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}
	*duration = ConfigDuration(parsed)
	return nil
}

// ResolvedStore is the normalized, environment-aware store configuration.
type ResolvedStore struct {
	Driver          store.Dialect
	SQLitePath      string
	PostgresDSN     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime ConfigDuration
	AuditSeparate   bool
}

// MetadataOptions maps the resolved configuration to the store package.
func (resolved ResolvedStore) MetadataOptions() store.MetadataOptions {
	return store.MetadataOptions{
		Driver:          resolved.Driver,
		SQLitePath:      resolved.SQLitePath,
		PostgresDSN:     resolved.PostgresDSN,
		MaxOpenConns:    resolved.MaxOpenConns,
		MaxIdleConns:    resolved.MaxIdleConns,
		ConnMaxLifetime: time.Duration(resolved.ConnMaxLifetime),
	}
}

// ResolveStore applies defaults, then the explicit environment override, and
// finally validates all cross-field invariants. It never touches the filesystem.
func ResolveStore(cfg *Config, lookup func(string) (string, bool)) (*ResolvedStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("resolve store: configuration is required")
	}
	if err := cfg.validateNonStore(); err != nil {
		return nil, err
	}
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}

	legacyConfigured := cfg.Store.legacySQLitePathSet || strings.TrimSpace(cfg.Store.SQLitePath) != ""
	if legacyConfigured && cfg.Store.Metadata != nil {
		return nil, fmt.Errorf("validate store: %w", ErrConflictingStoreConfig)
	}

	metadata := MetadataStoreConfig{Driver: string(store.DialectSQLite)}
	if cfg.Store.Metadata == nil {
		metadata.SQLitePath = cfg.Store.SQLitePath
	} else {
		metadata = *cfg.Store.Metadata
		if strings.TrimSpace(metadata.Driver) == "" {
			metadata.Driver = string(store.DialectSQLite)
		}
	}
	if metadata.MaxOpenConns < 0 || metadata.MaxIdleConns < 0 || metadata.ConnMaxLifetime < 0 {
		return nil, fmt.Errorf("validate store connection pool: values must not be negative")
	}
	if metadata.MaxOpenConns == 0 {
		metadata.MaxOpenConns = 10
	}
	if metadata.MaxIdleConns == 0 {
		metadata.MaxIdleConns = 5
	}
	if metadata.ConnMaxLifetime == 0 {
		metadata.ConnMaxLifetime = ConfigDuration(30 * time.Minute)
	}
	if value, ok := lookup(metadataDSNEnv); ok {
		metadata.DSN = value
	}

	dialect, err := store.ParseDialect(metadata.Driver)
	if err != nil {
		return nil, fmt.Errorf("validate store metadata driver: %w", err)
	}
	resolved := &ResolvedStore{
		Driver:          dialect,
		SQLitePath:      metadata.SQLitePath,
		PostgresDSN:     strings.TrimSpace(metadata.DSN),
		MaxOpenConns:    metadata.MaxOpenConns,
		MaxIdleConns:    metadata.MaxIdleConns,
		ConnMaxLifetime: metadata.ConnMaxLifetime,
	}
	if cfg.Store.Audit != nil {
		resolved.AuditSeparate = cfg.Store.Audit.Separate
	}
	if resolved.AuditSeparate {
		return nil, fmt.Errorf("validate store audit: %w", ErrUnsupportedAuditStore)
	}
	if resolved.MaxOpenConns <= 0 || resolved.MaxIdleConns < 0 || resolved.ConnMaxLifetime <= 0 {
		return nil, fmt.Errorf("validate store connection pool: max_open_conns and conn_max_lifetime must be positive and max_idle_conns must not be negative")
	}
	if resolved.MaxIdleConns > resolved.MaxOpenConns {
		return nil, fmt.Errorf("validate store connection pool: max_idle_conns must not exceed max_open_conns")
	}

	switch resolved.Driver {
	case store.DialectSQLite:
		if strings.TrimSpace(resolved.SQLitePath) == "" {
			return nil, fmt.Errorf("validate store: %w", ErrMissingSQLitePath)
		}
		if resolved.PostgresDSN != "" {
			return nil, fmt.Errorf("validate store metadata: dsn must be empty for sqlite")
		}
	case store.DialectPostgres:
		if strings.TrimSpace(resolved.SQLitePath) != "" {
			return nil, fmt.Errorf("validate store metadata: sqlite_path must be empty for postgres")
		}
		if resolved.PostgresDSN == "" {
			return nil, fmt.Errorf("validate store: %w", ErrMissingPostgresDSN)
		}
	}
	return resolved, nil
}

func applyResolvedStore(cfg *Config, resolved *ResolvedStore) {
	if cfg.Store.Metadata == nil {
		cfg.Store.SQLitePath = resolved.SQLitePath
		return
	}
	cfg.Store.Metadata.Driver = string(resolved.Driver)
	cfg.Store.Metadata.SQLitePath = resolved.SQLitePath
	cfg.Store.Metadata.DSN = resolved.PostgresDSN
	cfg.Store.Metadata.MaxOpenConns = resolved.MaxOpenConns
	cfg.Store.Metadata.MaxIdleConns = resolved.MaxIdleConns
	cfg.Store.Metadata.ConnMaxLifetime = resolved.ConnMaxLifetime
}
