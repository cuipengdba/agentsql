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
	"github.com/google/uuid"
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
	// ErrInvalidColumnAuthorizationInstanceID indicates an explicitly empty or corrupt persisted instance identity.
	ErrInvalidColumnAuthorizationInstanceID = errors.New("column_authorization.instance_id must be non-empty when explicitly configured")
	// ErrB5MySQLUnsupported prevents a configuration switch from advertising a
	// transaction dialect whose ownership and binder contracts are not closed.
	ErrB5MySQLUnsupported = errors.New("mcp.transactions.mysql 不受支持；请关闭该开关并使用 PostgreSQL")
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
	MCP                 MCPConfig                 `yaml:"mcp"`
}

// MCPConfig is the production configuration root for the MCP HTTP transport,
// explicit logical sessions, and planned multi-request transactions.
type MCPConfig struct {
	HTTP         MCPHTTPConfig         `yaml:"http"`
	Sessions     MCPSessionsConfig     `yaml:"sessions"`
	Transactions MCPTransactionsConfig `yaml:"transactions"`

	httpStatefulSet      bool
	httpEventStoreSet    bool
	sessionsEnabledSet   bool
	transactionsPGSet    bool
	transactionsMySQLSet bool
}

// MCPHTTPConfig controls protocol-level Streamable HTTP sessions. These
// sessions are independent from the application-level B5 sessions below.
type MCPHTTPConfig struct {
	Stateful           bool `yaml:"stateful"`
	SessionTimeoutMS   int  `yaml:"session_timeout_ms"`
	EventStoreEnabled  bool `yaml:"event_store_enabled"`
	EventStoreMaxBytes int  `yaml:"event_store_max_bytes"`
	EventStoreTTLMS    int  `yaml:"event_store_ttl_ms"`
}

type MCPSessionsConfig struct {
	Enabled       bool   `yaml:"enabled"`
	InstanceID    string `yaml:"instance_id"`
	StickyRoute   string `yaml:"sticky_route"`
	IdleTTLMS     int    `yaml:"idle_ttl_ms"`
	AbsoluteTTLMS int    `yaml:"absolute_ttl_ms"`
}

type MCPTransactionsConfig struct {
	Postgres           bool   `yaml:"postgres"`
	MySQL              bool   `yaml:"mysql"`
	IdleTimeoutMS      int    `yaml:"idle_timeout_ms"`
	WallTimeoutMS      int    `yaml:"wall_timeout_ms"`
	StatementTimeoutMS int    `yaml:"statement_timeout_ms"`
	ShutdownDrainMS    int    `yaml:"shutdown_drain_ms"`
	WALDirectory       string `yaml:"wal_directory"`
}

// EffectiveMCP returns the factory defaults for programmatically constructed
// Config values as well as parsed YAML. Parsed explicit false values remain
// authoritative because Parse fills the non-boolean defaults before decode.
func (config Config) EffectiveMCP() MCPConfig {
	value := config.MCP
	if !value.httpStatefulSet {
		value.HTTP.Stateful = true
	}
	if value.HTTP.SessionTimeoutMS == 0 {
		value.HTTP.SessionTimeoutMS = 10 * 60 * 1000
	}
	if !value.httpEventStoreSet {
		value.HTTP.EventStoreEnabled = true
	}
	if value.HTTP.EventStoreMaxBytes <= 0 {
		value.HTTP.EventStoreMaxBytes = 64 << 20
	}
	if value.HTTP.EventStoreTTLMS <= 0 {
		value.HTTP.EventStoreTTLMS = 30 * 60 * 1000
	}
	if value.Sessions.IdleTTLMS == 0 && value.Sessions.AbsoluteTTLMS == 0 &&
		value.Transactions.IdleTimeoutMS == 0 && value.Transactions.WallTimeoutMS == 0 &&
		value.Transactions.StatementTimeoutMS == 0 && value.Transactions.ShutdownDrainMS == 0 {
		value.Sessions.Enabled = true
		value.Transactions.Postgres = true
	}
	if value.Sessions.IdleTTLMS == 0 {
		value.Sessions.IdleTTLMS = 10 * 60 * 1000
	}
	if value.Sessions.AbsoluteTTLMS == 0 {
		value.Sessions.AbsoluteTTLMS = 60 * 60 * 1000
	}
	if value.Transactions.IdleTimeoutMS == 0 {
		value.Transactions.IdleTimeoutMS = 15 * 1000
	}
	if value.Transactions.WallTimeoutMS == 0 {
		value.Transactions.WallTimeoutMS = 60 * 1000
	}
	if value.Transactions.StatementTimeoutMS == 0 {
		value.Transactions.StatementTimeoutMS = 5 * 1000
	}
	if value.Transactions.ShutdownDrainMS == 0 {
		value.Transactions.ShutdownDrainMS = 5 * 1000
	}
	return value
}

// ColumnAuthorizationConfig controls the PostgreSQL protocol-3 path. Enabled
// defaults on for parsed YAML, while an explicitly configured false remains an
// authoritative rollback switch. Load resolves an omitted InstanceID from a
// persistent per-deployment random identity before production assembly.
type ColumnAuthorizationConfig struct {
	Enabled             bool   `yaml:"enabled"`
	DryRun              bool   `yaml:"dry_run"`
	InstanceID          string `yaml:"instance_id"`
	InstanceIDFile      string `yaml:"instance_id_file"`
	LeaseMS             int    `yaml:"lease_ms"`
	HeartbeatIntervalMS int    `yaml:"heartbeat_interval_ms"`

	enabledSet               bool
	instanceIDSet            bool
	instanceIDFileSet        bool
	instanceIDDefaultPending bool
	instanceIDResolvedFile   bool
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
	loaded.MCP.HTTP = MCPHTTPConfig{
		Stateful: true, SessionTimeoutMS: 10 * 60 * 1000,
		EventStoreEnabled: true, EventStoreMaxBytes: 64 << 20, EventStoreTTLMS: 30 * 60 * 1000,
	}
	loaded.MCP.Sessions = MCPSessionsConfig{Enabled: true, IdleTTLMS: 10 * 60 * 1000, AbsoluteTTLMS: 60 * 60 * 1000}
	loaded.MCP.Transactions = MCPTransactionsConfig{
		Postgres: true, MySQL: false, IdleTimeoutMS: 15 * 1000,
		WallTimeoutMS: 60 * 1000, StatementTimeoutMS: 5 * 1000, ShutdownDrainMS: 5 * 1000,
	}
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
	columnFields := configuredColumnAuthorizationFields(contents)
	loaded.ColumnAuthorization.enabledSet = columnFields["enabled"]
	loaded.ColumnAuthorization.instanceIDSet = columnFields["instance_id"]
	loaded.ColumnAuthorization.instanceIDFileSet = columnFields["instance_id_file"]
	if !loaded.ColumnAuthorization.enabledSet {
		loaded.ColumnAuthorization.Enabled = true
	}
	httpFields, sessionFields, transactionFields := configuredMCPFields(contents)
	loaded.MCP.httpStatefulSet = httpFields["stateful"]
	loaded.MCP.httpEventStoreSet = httpFields["event_store_enabled"]
	loaded.MCP.sessionsEnabledSet = sessionFields["enabled"]
	loaded.MCP.transactionsPGSet = transactionFields["postgres"]
	loaded.MCP.transactionsMySQLSet = transactionFields["mysql"]
	// A single explicit session rollback switch disables the default dependent
	// PostgreSQL transaction switch unless the operator explicitly contradicted
	// it, in which case validation fails below.
	if loaded.MCP.sessionsEnabledSet && !loaded.MCP.Sessions.Enabled && !loaded.MCP.transactionsPGSet {
		loaded.MCP.Transactions.Postgres = false
	}
	if (loaded.ColumnAuthorization.Enabled || loaded.ColumnAuthorization.DryRun) &&
		!loaded.ColumnAuthorization.instanceIDSet && strings.TrimSpace(loaded.ColumnAuthorization.InstanceID) == "" {
		loaded.ColumnAuthorization.instanceIDDefaultPending = true
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
	if err := resolveColumnAuthorizationInstanceID(path, resolved, &loaded.ColumnAuthorization); err != nil {
		return Config{}, fmt.Errorf("resolve config %q: %w", path, err)
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

func configuredColumnAuthorizationFields(contents []byte) map[string]bool {
	var document struct {
		ColumnAuthorization map[string]yaml.Node `yaml:"column_authorization"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return map[string]bool{}
	}
	configured := make(map[string]bool, len(document.ColumnAuthorization))
	for field := range document.ColumnAuthorization {
		configured[field] = true
	}
	return configured
}

func configuredMCPFields(contents []byte) (map[string]bool, map[string]bool, map[string]bool) {
	var document struct {
		MCP struct {
			HTTP         map[string]yaml.Node `yaml:"http"`
			Sessions     map[string]yaml.Node `yaml:"sessions"`
			Transactions map[string]yaml.Node `yaml:"transactions"`
		} `yaml:"mcp"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	httpFields := make(map[string]bool, len(document.MCP.HTTP))
	for field := range document.MCP.HTTP {
		httpFields[field] = true
	}
	sessions := make(map[string]bool, len(document.MCP.Sessions))
	for field := range document.MCP.Sessions {
		sessions[field] = true
	}
	transactions := make(map[string]bool, len(document.MCP.Transactions))
	for field := range document.MCP.Transactions {
		transactions[field] = true
	}
	return httpFields, sessions, transactions
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
	if config.ColumnAuthorization.Enabled && config.ColumnAuthorization.DryRun {
		return fmt.Errorf("validate column_authorization: enabled and dry_run are mutually exclusive")
	}
	if strings.TrimSpace(config.ColumnAuthorization.InstanceID) != "" && strings.TrimSpace(config.ColumnAuthorization.InstanceIDFile) != "" &&
		!config.ColumnAuthorization.instanceIDResolvedFile {
		return fmt.Errorf("validate column_authorization: instance_id and instance_id_file are mutually exclusive")
	}
	if (config.ColumnAuthorization.Enabled || config.ColumnAuthorization.DryRun) && strings.TrimSpace(config.ColumnAuthorization.InstanceID) == "" &&
		!config.ColumnAuthorization.instanceIDDefaultPending {
		return fmt.Errorf("validate column_authorization: %w", ErrInvalidColumnAuthorizationInstanceID)
	}
	if config.ColumnAuthorization.LeaseMS < 0 || config.ColumnAuthorization.HeartbeatIntervalMS < 0 {
		return fmt.Errorf("validate column_authorization: lease and heartbeat interval must not be negative")
	}
	if (config.ColumnAuthorization.Enabled || config.ColumnAuthorization.DryRun) && config.ColumnAuthorization.LeaseMS > 0 &&
		config.ColumnAuthorization.HeartbeatIntervalMS >= config.ColumnAuthorization.LeaseMS {
		return fmt.Errorf("validate column_authorization: heartbeat interval must be less than lease")
	}
	mcp := config.EffectiveMCP()
	if mcp.HTTP.SessionTimeoutMS <= 0 || mcp.HTTP.SessionTimeoutMS > 30*60*1000 {
		return fmt.Errorf("validate mcp.http: session_timeout_ms must be positive and <= 30m")
	}
	if mcp.HTTP.EventStoreMaxBytes > 1<<30 {
		return fmt.Errorf("validate mcp.http: event_store_max_bytes must be <= 1GiB")
	}
	if mcp.HTTP.EventStoreTTLMS > 24*60*60*1000 {
		return fmt.Errorf("validate mcp.http: event_store_ttl_ms must be <= 24h")
	}
	if mcp.Transactions.MySQL {
		return fmt.Errorf("validate mcp.transactions.mysql: %w", ErrB5MySQLUnsupported)
	}
	if !mcp.Sessions.Enabled && mcp.Transactions.Postgres {
		return fmt.Errorf("validate mcp: transactions.postgres=true requires sessions.enabled=true")
	}
	if mcp.Sessions.IdleTTLMS <= 0 || mcp.Sessions.IdleTTLMS > 30*60*1000 ||
		mcp.Sessions.AbsoluteTTLMS <= 0 || mcp.Sessions.AbsoluteTTLMS > 60*60*1000 ||
		mcp.Sessions.IdleTTLMS > mcp.Sessions.AbsoluteTTLMS {
		return fmt.Errorf("validate mcp.sessions: idle_ttl_ms must be positive and <= 30m; absolute_ttl_ms must be positive and <= 60m")
	}
	if len(strings.TrimSpace(mcp.Sessions.InstanceID)) > 128 || len(strings.TrimSpace(mcp.Sessions.StickyRoute)) > 256 {
		return fmt.Errorf("validate mcp.sessions: instance_id or sticky_route is too long")
	}
	transactions := mcp.Transactions
	if transactions.IdleTimeoutMS <= 0 || transactions.IdleTimeoutMS > 30*1000 ||
		transactions.WallTimeoutMS <= 0 || transactions.WallTimeoutMS > 60*1000 ||
		transactions.StatementTimeoutMS <= 0 || transactions.StatementTimeoutMS > 5*1000 ||
		transactions.ShutdownDrainMS <= 0 || transactions.ShutdownDrainMS > 30*1000 {
		return fmt.Errorf("validate mcp.transactions: timeout values must be positive and must not exceed the B5 hard limits")
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

func resolveColumnAuthorizationInstanceID(configPath string, resolved *ResolvedStore, column *ColumnAuthorizationConfig) error {
	if column == nil || (!column.Enabled && !column.DryRun) {
		return nil
	}
	if explicit := strings.TrimSpace(column.InstanceID); explicit != "" {
		column.InstanceID = explicit
		column.instanceIDDefaultPending = false
		return nil
	}
	if column.instanceIDSet {
		return ErrInvalidColumnAuthorizationInstanceID
	}
	identityPath := strings.TrimSpace(column.InstanceIDFile)
	if column.instanceIDFileSet && identityPath == "" {
		return fmt.Errorf("column_authorization.instance_id_file must be non-empty when explicitly configured")
	}
	var err error
	if identityPath == "" {
		identityPath, err = columnAuthorizationInstanceIDPath(configPath, resolved)
	}
	if err != nil {
		return err
	}
	identity, err := readOrCreateColumnAuthorizationInstanceID(identityPath)
	if err != nil {
		return err
	}
	column.InstanceID = identity
	column.instanceIDDefaultPending = false
	column.instanceIDResolvedFile = true
	return nil
}

func columnAuthorizationInstanceIDPath(configPath string, resolved *ResolvedStore) (string, error) {
	if resolved == nil {
		return "", fmt.Errorf("column authorization instance identity: resolved store is required")
	}
	if resolved.Metadata.Driver == store.DialectSQLite && strings.TrimSpace(resolved.Metadata.SQLitePath) != "" {
		return filepath.Clean(resolved.Metadata.SQLitePath) + ".instance-id", nil
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolve column authorization instance identity path: %w", err)
	}
	return filepath.Clean(absolute) + ".instance-id", nil
}

func readOrCreateColumnAuthorizationInstanceID(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err == nil {
		return validatePersistedColumnAuthorizationInstanceID(contents)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read column authorization instance identity %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create column authorization instance identity directory %q: %w", filepath.Dir(path), err)
	}
	generated, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate column authorization instance identity: %w", err)
	}
	identity := "agentsql-" + generated.String()
	file, err := os.CreateTemp(filepath.Dir(path), ".agentsql-instance-id-*")
	if err != nil {
		return "", fmt.Errorf("create temporary column authorization instance identity for %q: %w", path, err)
	}
	temporaryPath := file.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("secure temporary column authorization instance identity %q: %w", temporaryPath, err)
	}
	if _, err := file.WriteString(identity + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("persist column authorization instance identity %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync column authorization instance identity %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temporary column authorization instance identity %q: %w", temporaryPath, err)
	}
	err = os.Link(temporaryPath, path)
	if errors.Is(err, os.ErrExist) {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read concurrently created column authorization instance identity %q: %w", path, readErr)
		}
		return validatePersistedColumnAuthorizationInstanceID(contents)
	}
	if err != nil {
		return "", fmt.Errorf("create column authorization instance identity %q: %w", path, err)
	}
	return identity, nil
}

func validatePersistedColumnAuthorizationInstanceID(contents []byte) (string, error) {
	identity := strings.TrimSpace(string(contents))
	const prefix = "agentsql-"
	if !strings.HasPrefix(identity, prefix) {
		return "", fmt.Errorf("validate persisted column authorization instance identity: %w", ErrInvalidColumnAuthorizationInstanceID)
	}
	if _, err := uuid.Parse(strings.TrimPrefix(identity, prefix)); err != nil {
		return "", fmt.Errorf("validate persisted column authorization instance identity: %w", ErrInvalidColumnAuthorizationInstanceID)
	}
	return identity, nil
}
