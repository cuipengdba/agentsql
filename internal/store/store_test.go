package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func openTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv(secretEnvironmentVariable, testSecret)
	opened, err := Open(context.Background(), filepath.Join(t.TempDir(), "agentsql.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, opened.Close())
	})
	return opened
}

func TestStorePing(t *testing.T) {
	opened := openTestStore(t)
	require.NoError(t, opened.Ping(context.Background()))

	var nilStore *Store
	require.Error(t, nilStore.Ping(context.Background()))
	require.Error(t, opened.Ping(nil))
}

func TestOpenMetadataSQLiteAndValidation(t *testing.T) {
	ctx := context.Background()
	opened, err := OpenMetadata(ctx, MetadataOptions{
		Driver:      DialectSQLite,
		SQLitePath:  filepath.Join(t.TempDir(), "metadata.db"),
		AutoMigrate: true,
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	require.Equal(t, 1, opened.metaDB.Stats().MaxOpenConnections)

	_, err = OpenMetadata(ctx, MetadataOptions{Driver: Dialect("mysql")}, []byte(testSecret))
	require.ErrorIs(t, err, ErrInvalidMetadataDriver)

	_, err = OpenMetadata(ctx, MetadataOptions{Driver: DialectSQLite}, []byte(testSecret))
	require.ErrorIs(t, err, ErrInvalidStorePath)

	_, err = OpenMetadata(ctx, MetadataOptions{Driver: DialectPostgres}, []byte(testSecret))
	require.ErrorIs(t, err, ErrInvalidPostgresDSN)

	_, err = OpenMetadata(nil, MetadataOptions{
		Driver:     DialectSQLite,
		SQLitePath: "unused.db",
	}, []byte(testSecret))
	require.True(t, errors.Is(err, ErrNilContext))
}

func TestOpenMetadataAutoMigrateFalseVerifiesWithoutDDL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	options := MetadataOptions{
		Driver:      DialectSQLite,
		SQLitePath:  path,
		AutoMigrate: false,
	}
	_, err := OpenMetadata(ctx, options, []byte(testSecret))
	require.ErrorContains(t, err, "schema_migrations")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var schemaTableCount int
	require.NoError(t, raw.QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&schemaTableCount))
	require.Zero(t, schemaTableCount, "verification-only startup must not create schema tables")
	require.NoError(t, raw.Close())

	options.AutoMigrate = true
	opened, err := OpenMetadata(ctx, options, []byte(testSecret))
	require.NoError(t, err)
	require.NoError(t, opened.Close())

	options.AutoMigrate = false
	verified, err := OpenMetadata(ctx, options, []byte(testSecret))
	require.NoError(t, err)
	require.NoError(t, verified.Ping(ctx))
	require.NoError(t, verified.Close())

	raw, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, "UPDATE schema_migrations SET version=3 WHERE version=2")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	_, err = OpenMetadata(ctx, options, []byte(testSecret))
	require.ErrorContains(t, err, "current=3 latest=2")
}

func TestStorePingAndCloseUseConnectionIdentity(t *testing.T) {
	t.Run("shared connection is used once", func(t *testing.T) {
		counters := &countingDriverCounters{}
		database := openCountingDatabase(t, counters)
		opened := &Store{
			metaDB:      database,
			auditDB:     database,
			metaDriver:  DialectSQLite,
			auditDriver: DialectSQLite,
		}
		require.NoError(t, opened.Ping(context.Background()))
		require.EqualValues(t, 1, counters.pings.Load())
		require.NoError(t, opened.Close())
		require.EqualValues(t, 1, counters.closes.Load())
	})

	t.Run("separate connections are both used", func(t *testing.T) {
		counters := &countingDriverCounters{}
		metadataDB := openCountingDatabase(t, counters)
		auditDB := openCountingDatabase(t, counters)
		opened := &Store{
			metaDB:        metadataDB,
			auditDB:       auditDB,
			metaDriver:    DialectPostgres,
			auditDriver:   DialectPostgres,
			auditSeparate: true,
		}
		require.Same(t, metadataDB, opened.Agents().db)
		require.Same(t, metadataDB, opened.Datasources().db)
		require.Same(t, metadataDB, opened.Policies().db)
		require.Same(t, metadataDB, opened.Rules().db)
		require.Same(t, metadataDB, opened.MaskRules().db)
		require.Same(t, metadataDB, opened.Notifications().db)
		require.Same(t, auditDB, opened.AuditLogs().db)
		require.Same(t, metadataDB, opened.Approvals().db)
		require.Same(t, metadataDB, opened.Dashboard().meta.db)
		require.Same(t, auditDB, opened.Dashboard().audit.db)
		require.NoError(t, opened.Ping(context.Background()))
		require.EqualValues(t, 2, counters.pings.Load())
		require.NoError(t, opened.Close())
		require.EqualValues(t, 2, counters.closes.Load())
	})
}

var countingDriverSequence atomic.Int64

type countingDriverCounters struct {
	pings  atomic.Int32
	closes atomic.Int32
}

type countingDriver struct {
	counters *countingDriverCounters
}

func (driverImpl countingDriver) Open(string) (driver.Conn, error) {
	return &countingConnection{counters: driverImpl.counters}, nil
}

type countingConnection struct {
	counters *countingDriverCounters
}

func (connection *countingConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (connection *countingConnection) Close() error {
	connection.counters.closes.Add(1)
	return nil
}

func (*countingConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("not implemented")
}

func (connection *countingConnection) Ping(context.Context) error {
	connection.counters.pings.Add(1)
	return nil
}

func openCountingDatabase(t *testing.T, counters *countingDriverCounters) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("agentsql-counting-%d", countingDriverSequence.Add(1))
	sql.Register(name, countingDriver{counters: counters})
	database, err := sql.Open(name, "")
	require.NoError(t, err)
	return database
}

func createPolicyDependencies(t *testing.T, opened *Store) (model.Agent, model.Datasource) {
	t.Helper()
	_, apiKeyHash, err := GenerateAPIKey()
	require.NoError(t, err)
	agent, err := opened.Agents().Create(context.Background(), model.Agent{
		ID:         "ag_policy",
		Name:       "Policy Agent",
		Status:     "active",
		APIKeyHash: apiKeyHash,
		Level:      "readonly",
	})
	require.NoError(t, err)
	datasource, err := opened.Datasources().Create(context.Background(), model.Datasource{
		ID:            "ds_policy",
		Name:          "Policy Database",
		DBType:        "postgres",
		Host:          "127.0.0.1",
		Port:          5432,
		Database:      "app",
		Username:      "agentsql",
		ConnLimit:     5,
		StmtTimeoutMS: 5000,
		RowLimit:      1000,
	}, "database-password")
	require.NoError(t, err)
	return agent, datasource
}

func pointer[T any](value T) *T {
	return &value
}
