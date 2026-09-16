package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteToPostgresTableOrderIsFrozen(t *testing.T) {
	names := make([]string, 0, len(sqliteToPostgresTables))
	for _, table := range sqliteToPostgresTables {
		names = append(names, table.name)
	}
	require.Equal(t, []string{
		"agents", "datasources", "rules", "mask_rules", "policies", "audit_logs", "approvals",
	}, names)
}

func TestNormalizeMigrationCell(t *testing.T) {
	t.Run("boolean", func(t *testing.T) {
		zero, err := normalizeMigrationCell(migrationBool, int64(0))
		require.NoError(t, err)
		require.Equal(t, false, zero.value)
		one, err := normalizeMigrationCell(migrationBool, int64(1))
		require.NoError(t, err)
		require.Equal(t, true, one.value)
		_, err = normalizeMigrationCell(migrationBool, int64(2))
		require.ErrorContains(t, err, "0 or 1")
	})

	t.Run("timestamp UTC microseconds", func(t *testing.T) {
		withoutZone, err := normalizeMigrationCell(migrationTime, "2026-09-17 12:34:56.123456789")
		require.NoError(t, err)
		require.Equal(t, time.Date(2026, 9, 17, 12, 34, 56, 123456000, time.UTC), withoutZone.value)
		withZone, err := normalizeMigrationCell(migrationTime, "2026-09-17T20:34:56.987654321+08:00")
		require.NoError(t, err)
		require.Equal(t, time.Date(2026, 9, 17, 12, 34, 56, 987654000, time.UTC), withZone.value)
	})

	t.Run("integer ranges", func(t *testing.T) {
		_, err := normalizeMigrationCell(migrationInt32, int64(1<<31))
		require.ErrorContains(t, err, "int32")
		value, err := normalizeMigrationCell(migrationInt64, int64(1<<62))
		require.NoError(t, err)
		require.Equal(t, int64(1<<62), value.value)
	})

	t.Run("NULL and empty string differ", func(t *testing.T) {
		nullValue, err := normalizeMigrationCell(migrationText, nil)
		require.NoError(t, err)
		require.False(t, nullValue.valid)
		emptyValue, err := normalizeMigrationCell(migrationText, "")
		require.NoError(t, err)
		require.True(t, emptyValue.valid)
		require.Empty(t, emptyValue.value)
	})

	t.Run("unsafe text", func(t *testing.T) {
		_, err := normalizeMigrationCell(migrationText, []byte{0xff})
		require.ErrorContains(t, err, "UTF-8")
		_, err = normalizeMigrationCell(migrationText, "safe\x00unsafe")
		require.ErrorContains(t, err, "NUL")
	})
}

func TestCanonicalMigrationEncodingIsDeterministicAndUnambiguous(t *testing.T) {
	cells := []migrationCell{
		{kind: migrationText, valid: true, value: "a|b"},
		{kind: migrationText},
		{kind: migrationText, valid: true, value: ""},
		{kind: migrationBool, valid: true, value: true},
		{kind: migrationInt64, valid: true, value: int64(42)},
		{kind: migrationTime, valid: true, value: time.Date(2026, 9, 17, 1, 2, 3, 456789999, time.FixedZone("x", 8*3600))},
	}
	first, second := sha256.New(), sha256.New()
	writeCanonicalRow(first, cells)
	writeCanonicalRow(second, cells)
	require.Equal(t, first.Sum(nil), second.Sum(nil))

	different := sha256.New()
	changed := append([]migrationCell(nil), cells...)
	changed[1], changed[2] = changed[2], changed[1]
	writeCanonicalRow(different, changed)
	require.NotEqual(t, first.Sum(nil), different.Sum(nil))

	large := sha256.New()
	writeCanonicalCell(large, migrationCell{kind: migrationText, valid: true, value: strings.Repeat("x", 2<<20)})
	require.Len(t, large.Sum(nil), sha256.Size)
}

func TestClassifyMigrationGroup(t *testing.T) {
	tables := sqliteToPostgresTables[:2]
	source := map[string]migrationDigest{
		"agents":      {rows: 1, hash: [32]byte{1}, primaryKey: [32]byte{2}},
		"datasources": {rows: 1, hash: [32]byte{3}, primaryKey: [32]byte{4}},
	}
	empty := map[string]migrationDigest{"agents": {}, "datasources": {}}
	require.Equal(t, migrationEmpty, classifyMigrationGroup(tables, source, empty))
	exact := map[string]migrationDigest{"agents": source["agents"], "datasources": source["datasources"]}
	require.Equal(t, migrationExact, classifyMigrationGroup(tables, source, exact))
	conflict := map[string]migrationDigest{"agents": source["agents"], "datasources": {rows: 1, hash: [32]byte{9}}}
	require.Equal(t, migrationConflict, classifyMigrationGroup(tables, source, conflict))
}

func TestExpectedSequenceNext(t *testing.T) {
	require.Equal(t, int64(1), expectedSequenceNext(1, false))
	require.Equal(t, int64(43), expectedSequenceNext(42, true))
}

func TestVerifyMigrationSourceRejectsNonLatestAndOrphan(t *testing.T) {
	ctx := context.Background()
	t.Run("non-latest", func(t *testing.T) {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		require.NoError(t, database.PingContext(ctx))
		tx, err := database.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		err = verifyMigrationSource(ctx, tx)
		require.ErrorContains(t, err, "schema_migrations")
	})

	t.Run("orphan", func(t *testing.T) {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "orphan.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		require.NoError(t, Migrate(ctx, database, DialectSQLite))
		_, err = database.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
		require.NoError(t, err)
		_, err = database.ExecContext(ctx, `INSERT INTO approvals(id,audit_id) VALUES('approval-locator',999)`)
		require.NoError(t, err)
		tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer tx.Rollback()
		err = verifyMigrationSource(ctx, tx)
		require.ErrorContains(t, err, "table=\"approvals\"")
		require.NotContains(t, err.Error(), "999")
	})

	t.Run("separated SQLite schema is not a combined source", func(t *testing.T) {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "split.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		require.NoError(t, MigrateMetadata(ctx, database, DialectSQLite, true))
		tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer tx.Rollback()
		err = verifyMigrationSource(ctx, tx)
		require.ErrorContains(t, err, "audit_logs")
	})
}
