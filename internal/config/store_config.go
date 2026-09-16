package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"gopkg.in/yaml.v3"
)

const (
	MetadataDSNEnv = "AGENTSQL_STORE_METADATA_DSN"
	AuditDSNEnv    = "AGENTSQL_STORE_AUDIT_DSN"

	// Keep the package-local names used by the existing tests.
	metadataDSNEnv = MetadataDSNEnv
	auditDSNEnv    = AuditDSNEnv
)

var (
	ErrConflictingStoreConfig = errors.New("store.sqlite_path and store.metadata are mutually exclusive")
	ErrMissingPostgresDSN     = errors.New("store.metadata.dsn is required for postgres")
	ErrMissingAuditDSN        = errors.New("store.audit.dsn is required for a separate audit store")
)

// StoreConfig supports both the v0.1 SQLite shorthand and the extensible
// metadata/audit form. SQLitePath must remain compatible with programmatic
// StoreConfig{SQLitePath: ...} construction.
type StoreConfig struct {
	SQLitePath  string               `yaml:"sqlite_path"`
	Metadata    *MetadataStoreConfig `yaml:"metadata"`
	Audit       *AuditStoreConfig    `yaml:"audit"`
	AutoMigrate bool                 `yaml:"auto_migrate"`

	legacySQLitePathSet bool
	autoMigrateSet      bool
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
	Separate     bool   `yaml:"separate"`
	Driver       string `yaml:"driver"`
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`

	configuredFields map[string]bool
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

// ResolvedStoreTarget is one normalized database target. ReuseMetadata is only
// true for the audit target when it shares the metadata connection; in that
// case PostgresDSN and SQLitePath deliberately remain empty.
type ResolvedStoreTarget struct {
	Driver          store.Dialect
	SQLitePath      string
	PostgresDSN     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime ConfigDuration
	ReuseMetadata   bool
}

// ResolvedStore is the normalized, environment-aware two-target configuration.
type ResolvedStore struct {
	Metadata    ResolvedStoreTarget
	Audit       ResolvedStoreTarget
	AutoMigrate bool
}

// MetadataOptions maps both resolved targets to the store package.
func (resolved ResolvedStore) MetadataOptions() store.MetadataOptions {
	return store.MetadataOptions{
		Driver:          resolved.Metadata.Driver,
		SQLitePath:      resolved.Metadata.SQLitePath,
		PostgresDSN:     resolved.Metadata.PostgresDSN,
		MaxOpenConns:    resolved.Metadata.MaxOpenConns,
		MaxIdleConns:    resolved.Metadata.MaxIdleConns,
		ConnMaxLifetime: time.Duration(resolved.Metadata.ConnMaxLifetime),
		AutoMigrate:     resolved.AutoMigrate,
		Audit:           resolved.AuditOptions(),
	}
}

// AuditOptions maps the resolved audit target without copying a shared DSN.
func (resolved ResolvedStore) AuditOptions() store.AuditOptions {
	if resolved.Audit.ReuseMetadata {
		return store.AuditOptions{Separate: false, AutoMigrate: resolved.AutoMigrate}
	}
	return store.AuditOptions{
		Separate:     true,
		Driver:       resolved.Audit.Driver,
		PostgresDSN:  resolved.Audit.PostgresDSN,
		MaxOpenConns: resolved.Audit.MaxOpenConns,
		MaxIdleConns: resolved.Audit.MaxIdleConns,
		AutoMigrate:  resolved.AutoMigrate,
	}
}

// ResolveStore applies defaults, then explicit environment overrides, and
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
	if err := defaultAndValidateMetadataPool(&metadata); err != nil {
		return nil, err
	}
	if value, ok := lookup(MetadataDSNEnv); ok {
		metadata.DSN = value
	}
	metadataDialect, err := store.ParseDialect(metadata.Driver)
	if err != nil {
		return nil, fmt.Errorf("validate store metadata driver: %w", err)
	}
	resolvedMetadata := ResolvedStoreTarget{
		Driver:          metadataDialect,
		SQLitePath:      strings.TrimSpace(metadata.SQLitePath),
		PostgresDSN:     strings.TrimSpace(metadata.DSN),
		MaxOpenConns:    metadata.MaxOpenConns,
		MaxIdleConns:    metadata.MaxIdleConns,
		ConnMaxLifetime: metadata.ConnMaxLifetime,
	}
	if err := validateResolvedMetadata(resolvedMetadata); err != nil {
		return nil, err
	}

	audit := AuditStoreConfig{}
	if cfg.Store.Audit != nil {
		audit = *cfg.Store.Audit
	}
	if value, ok := lookup(AuditDSNEnv); ok {
		audit.DSN = value
	}
	resolvedAudit, err := resolveAudit(audit, resolvedMetadata)
	if err != nil {
		return nil, err
	}
	autoMigrate := cfg.Store.AutoMigrate
	if !cfg.Store.autoMigrateSet {
		autoMigrate = true
	}
	return &ResolvedStore{
		Metadata:    resolvedMetadata,
		Audit:       resolvedAudit,
		AutoMigrate: autoMigrate,
	}, nil
}

func defaultAndValidateMetadataPool(metadata *MetadataStoreConfig) error {
	if metadata.MaxOpenConns < 0 || metadata.MaxIdleConns < 0 || metadata.ConnMaxLifetime < 0 {
		return fmt.Errorf("validate store metadata connection pool: values must not be negative")
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
	if metadata.MaxIdleConns > metadata.MaxOpenConns {
		return fmt.Errorf("validate store metadata connection pool: max_idle_conns must not exceed max_open_conns")
	}
	return nil
}

func validateResolvedMetadata(resolved ResolvedStoreTarget) error {
	switch resolved.Driver {
	case store.DialectSQLite:
		if resolved.SQLitePath == "" {
			return fmt.Errorf("validate store: %w", ErrMissingSQLitePath)
		}
		if resolved.PostgresDSN != "" {
			return fmt.Errorf("validate store metadata: dsn must be empty for sqlite")
		}
	case store.DialectPostgres:
		if resolved.SQLitePath != "" {
			return fmt.Errorf("validate store metadata: sqlite_path must be empty for postgres")
		}
		if resolved.PostgresDSN == "" {
			return fmt.Errorf("validate store: %w", ErrMissingPostgresDSN)
		}
	}
	return nil
}

func resolveAudit(audit AuditStoreConfig, metadata ResolvedStoreTarget) (ResolvedStoreTarget, error) {
	if !audit.Separate {
		if auditFieldConfigured(audit, "driver", strings.TrimSpace(audit.Driver) != "") ||
			auditFieldConfigured(audit, "dsn", strings.TrimSpace(audit.DSN) != "") ||
			auditFieldConfigured(audit, "max_open_conns", audit.MaxOpenConns != 0) ||
			auditFieldConfigured(audit, "max_idle_conns", audit.MaxIdleConns != 0) {
			return ResolvedStoreTarget{}, fmt.Errorf("validate store audit: driver, dsn, and pool settings require separate=true")
		}
		return ResolvedStoreTarget{Driver: metadata.Driver, ReuseMetadata: true}, nil
	}

	driver := strings.ToLower(strings.TrimSpace(audit.Driver))
	if driver == "" {
		driver = string(store.DialectPostgres)
	}
	if driver != string(store.DialectPostgres) {
		return ResolvedStoreTarget{}, fmt.Errorf("validate store audit driver %q: only postgres is supported", driver)
	}
	if audit.MaxOpenConns < 0 || audit.MaxIdleConns < 0 {
		return ResolvedStoreTarget{}, fmt.Errorf("validate store audit connection pool: values must not be negative")
	}
	if audit.MaxOpenConns == 0 {
		audit.MaxOpenConns = 10
	}
	if audit.MaxIdleConns == 0 {
		audit.MaxIdleConns = 5
	}
	if audit.MaxIdleConns > audit.MaxOpenConns {
		return ResolvedStoreTarget{}, fmt.Errorf("validate store audit connection pool: max_idle_conns must not exceed max_open_conns")
	}
	dsn := strings.TrimSpace(audit.DSN)
	if dsn == "" {
		return ResolvedStoreTarget{}, fmt.Errorf("validate store audit: %w", ErrMissingAuditDSN)
	}
	return ResolvedStoreTarget{
		Driver:       store.DialectPostgres,
		PostgresDSN:  dsn,
		MaxOpenConns: audit.MaxOpenConns,
		MaxIdleConns: audit.MaxIdleConns,
	}, nil
}

func auditFieldConfigured(audit AuditStoreConfig, name string, nonzero bool) bool {
	return nonzero || audit.configuredFields[name]
}

func applyResolvedStore(cfg *Config, resolved *ResolvedStore) {
	cfg.Store.AutoMigrate = resolved.AutoMigrate
	if cfg.Store.Metadata == nil {
		cfg.Store.SQLitePath = resolved.Metadata.SQLitePath
	} else {
		cfg.Store.Metadata.Driver = string(resolved.Metadata.Driver)
		cfg.Store.Metadata.SQLitePath = resolved.Metadata.SQLitePath
		cfg.Store.Metadata.DSN = resolved.Metadata.PostgresDSN
		cfg.Store.Metadata.MaxOpenConns = resolved.Metadata.MaxOpenConns
		cfg.Store.Metadata.MaxIdleConns = resolved.Metadata.MaxIdleConns
		cfg.Store.Metadata.ConnMaxLifetime = resolved.Metadata.ConnMaxLifetime
	}
	if cfg.Store.Audit != nil && !resolved.Audit.ReuseMetadata {
		cfg.Store.Audit.Driver = string(resolved.Audit.Driver)
		cfg.Store.Audit.DSN = resolved.Audit.PostgresDSN
		cfg.Store.Audit.MaxOpenConns = resolved.Audit.MaxOpenConns
		cfg.Store.Audit.MaxIdleConns = resolved.Audit.MaxIdleConns
	}
}
