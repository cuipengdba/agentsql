package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/auditchain"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

const chainAppendTestInstanceID = "01234567-89ab-cdef-8123-456789abcdef"

type fakeChainKeyProvider struct {
	keys map[int][]byte
	err  error
}

func (provider fakeChainKeyProvider) ChainKeyForVersion(_ context.Context, version int) ([]byte, error) {
	if provider.err != nil {
		return nil, provider.err
	}
	key, ok := provider.keys[version]
	if !ok {
		return nil, fmt.Errorf("test key version %d is missing", version)
	}
	return key, nil
}

type storedAuditChainRow struct {
	ID            int64
	Sequence      int64
	PreviousHash  string
	SelfHash      string
	KeyVersion    int
	FormatVersion int
}

func TestSQLiteChainAwareAppend(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled preserves null chain columns", func(t *testing.T) {
		opened := openTestStore(t)
		repository := opened.AuditLogs()
		inserted, err := repository.Insert(ctx, model.AuditLog{Decision: "allow"})
		require.NoError(t, err)
		batch, err := repository.AppendBatch(ctx, []model.AuditLog{{Decision: "deny"}, {Decision: "warn"}})
		require.NoError(t, err)
		require.Len(t, batch, 2)
		assertNullChainColumns(t, ctx, opened.metaDB, append([]int64{inserted.ID}, batch[0].ID, batch[1].ID))
	})

	t.Run("building keyless links inserts and advances head", func(t *testing.T) {
		opened := openTestStore(t)
		repository := opened.AuditLogs()
		setBuildingChain(t, ctx, opened.metaDB, "management", "keyless")

		first, err := repository.Insert(ctx, fullAuditLogForChainTest("first", nil))
		require.NoError(t, err)
		second, err := repository.Insert(ctx, fullAuditLogForChainTest("second", nil))
		require.NoError(t, err)

		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Equal(t, []int64{1, 2}, []int64{rows[0].Sequence, rows[1].Sequence})
		require.Equal(t, auditchain.GenesisPrevHex, rows[0].PreviousHash)
		require.Equal(t, rows[0].SelfHash, rows[1].PreviousHash)
		assertStoredChainHash(t, first, rows[0], "management", chainAppendTestInstanceID, nil)
		assertStoredChainHash(t, second, rows[1], "management", chainAppendTestInstanceID, nil)
		assertChainHead(t, ctx, opened.metaDB, "management", 2, second.ID, rows[1].SelfHash)
	})

	t.Run("building hmac uses key and fails closed", func(t *testing.T) {
		opened := openTestStore(t)
		setBuildingChain(t, ctx, opened.metaDB, "management", "hmac")
		key := []byte("0123456789abcdef0123456789abcdef")
		repository := opened.AuditLogs()
		repository.keys = fakeChainKeyProvider{keys: map[int][]byte{1: key}}

		inserted, err := repository.Insert(ctx, fullAuditLogForChainTest("hmac", nil))
		require.NoError(t, err)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 1)
		require.Equal(t, 1, rows[0].KeyVersion)
		assertStoredChainHash(t, inserted, rows[0], "management", chainAppendTestInstanceID, key)

		beforeCount := countAuditLogs(t, ctx, opened.metaDB)
		beforeState := loadChainState(t, ctx, opened.metaDB, "management")
		for name, provider := range map[string]ChainKeyProvider{
			"provider error": fakeChainKeyProvider{err: errors.New("key service unavailable")},
			"missing key":    fakeChainKeyProvider{keys: map[int][]byte{}},
			"nil provider":   nil,
		} {
			t.Run(name, func(t *testing.T) {
				repository.keys = provider
				_, err := repository.Insert(ctx, model.AuditLog{Decision: "allow"})
				require.Error(t, err)
				require.Equal(t, beforeCount, countAuditLogs(t, ctx, opened.metaDB))
				afterState := loadChainState(t, ctx, opened.metaDB, "management")
				require.Equal(t, beforeState.HeadSeq, afterState.HeadSeq)
				require.Equal(t, beforeState.HeadHash, afterState.HeadHash)
			})
		}
	})

	t.Run("building batch is contiguous and atomic", func(t *testing.T) {
		opened := openTestStore(t)
		repository := opened.AuditLogs()
		setBuildingChain(t, ctx, opened.metaDB, "management", "keyless")
		logs := make([]model.AuditLog, 5)
		for index := range logs {
			eventUUID := fmt.Sprintf("batch-%d", index)
			logs[index] = fullAuditLogForChainTest(eventUUID, &eventUUID)
		}
		inserted, err := repository.AppendBatch(ctx, logs)
		require.NoError(t, err)
		require.Len(t, inserted, 5)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 5)
		for index, row := range rows {
			require.Equal(t, int64(index+1), row.Sequence)
			if index == 0 {
				require.Equal(t, auditchain.GenesisPrevHex, row.PreviousHash)
			} else {
				require.Equal(t, rows[index-1].SelfHash, row.PreviousHash)
			}
			assertStoredChainHash(t, inserted[index], row, "management", chainAppendTestInstanceID, nil)
		}
		assertChainHead(t, ctx, opened.metaDB, "management", 5, inserted[4].ID, rows[4].SelfHash)

		beforeCount := countAuditLogs(t, ctx, opened.metaDB)
		beforeState := loadChainState(t, ctx, opened.metaDB, "management")
		duplicate := "batch-2"
		_, err = repository.AppendBatch(ctx, []model.AuditLog{
			fullAuditLogForChainTest("would-roll-back", nil),
			fullAuditLogForChainTest("duplicate", &duplicate),
			fullAuditLogForChainTest("never-inserted", nil),
		})
		require.ErrorIs(t, err, ErrAuditEventAlreadyDelivered)
		require.Equal(t, beforeCount, countAuditLogs(t, ctx, opened.metaDB))
		afterState := loadChainState(t, ctx, opened.metaDB, "management")
		require.Equal(t, beforeState.HeadSeq, afterState.HeadSeq)
		require.Equal(t, beforeState.HeadID, afterState.HeadID)
		require.Equal(t, beforeState.HeadHash, afterState.HeadHash)
	})
}

func TestPostgres18ChainAwareAppendE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 chain-aware append E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	opened := openPostgres18TestStore(t)
	repository := opened.AuditLogs()

	t.Run("new writes link without backfilling history", func(t *testing.T) {
		resetChainAppendTestData(t, ctx, opened.metaDB, "management")
		historical := make([]model.AuditLog, 3)
		for index := range historical {
			historical[index] = model.AuditLog{Decision: "allow"}
		}
		oldRows, err := repository.AppendBatch(ctx, historical)
		require.NoError(t, err)
		setBuildingChain(t, ctx, opened.metaDB, "management", "keyless")

		one, err := repository.Insert(ctx, fullAuditLogForChainTest("pg-one", nil))
		require.NoError(t, err)
		batch, err := repository.AppendBatch(ctx, []model.AuditLog{
			fullAuditLogForChainTest("pg-two", nil),
			fullAuditLogForChainTest("pg-three", nil),
		})
		require.NoError(t, err)
		ids := []int64{oldRows[0].ID, oldRows[1].ID, oldRows[2].ID}
		assertNullChainColumns(t, ctx, opened.metaDB, ids)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 3)
		models := []model.AuditLog{one, batch[0], batch[1]}
		for index, row := range rows {
			require.Equal(t, int64(index+1), row.Sequence)
			assertStoredChainHash(t, models[index], row, "management", chainAppendTestInstanceID, nil)
		}
		assertChainHead(t, ctx, opened.metaDB, "management", 3, batch[1].ID, rows[2].SelfHash)
	})

	t.Run("concurrent inserts serialize to a contiguous chain", func(t *testing.T) {
		resetChainAppendTestData(t, ctx, opened.metaDB, "management")
		setBuildingChain(t, ctx, opened.metaDB, "management", "keyless")
		const writers = 40
		inserted := make([]model.AuditLog, writers)
		errorsByWriter := make([]error, writers)
		var waitGroup sync.WaitGroup
		for index := 0; index < writers; index++ {
			index := index
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				inserted[index], errorsByWriter[index] = repository.Insert(ctx, fullAuditLogForChainTest(fmt.Sprintf("concurrent-%02d", index), nil))
			}()
		}
		waitGroup.Wait()
		for index, err := range errorsByWriter {
			require.NoError(t, err, "writer %d", index)
		}

		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, writers)
		modelsByID := make(map[int64]model.AuditLog, writers)
		for _, auditLog := range inserted {
			modelsByID[auditLog.ID] = auditLog
		}
		for index, row := range rows {
			require.Equal(t, int64(index+1), row.Sequence)
			if index == 0 {
				require.Equal(t, auditchain.GenesisPrevHex, row.PreviousHash)
			} else {
				require.Equal(t, rows[index-1].SelfHash, row.PreviousHash)
			}
			assertStoredChainHash(t, modelsByID[row.ID], row, "management", chainAppendTestInstanceID, nil)
		}
		assertChainHead(t, ctx, opened.metaDB, "management", writers, rows[writers-1].ID, rows[writers-1].SelfHash)
	})

	t.Run("batch links once and rolls back on failure", func(t *testing.T) {
		resetChainAppendTestData(t, ctx, opened.metaDB, "management")
		setBuildingChain(t, ctx, opened.metaDB, "management", "keyless")
		logs := make([]model.AuditLog, 5)
		for index := range logs {
			eventUUID := fmt.Sprintf("pg-batch-%d", index)
			logs[index] = fullAuditLogForChainTest(eventUUID, &eventUUID)
		}
		inserted, err := repository.AppendBatch(ctx, logs)
		require.NoError(t, err)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 5)
		for index := range rows {
			require.Equal(t, int64(index+1), rows[index].Sequence)
			assertStoredChainHash(t, inserted[index], rows[index], "management", chainAppendTestInstanceID, nil)
		}

		beforeState := loadChainState(t, ctx, opened.metaDB, "management")
		duplicate := "pg-batch-3"
		_, err = repository.AppendBatch(ctx, []model.AuditLog{
			fullAuditLogForChainTest("pg-would-roll-back", nil),
			fullAuditLogForChainTest("pg-duplicate", &duplicate),
		})
		require.ErrorIs(t, err, ErrAuditEventAlreadyDelivered)
		require.Equal(t, int64(5), countAuditLogs(t, ctx, opened.metaDB))
		afterState := loadChainState(t, ctx, opened.metaDB, "management")
		require.Equal(t, beforeState.HeadSeq, afterState.HeadSeq)
		require.Equal(t, beforeState.HeadID, afterState.HeadID)
		require.Equal(t, beforeState.HeadHash, afterState.HeadHash)
	})
}

func TestAuditLogRepositoryChainDomainInjection(t *testing.T) {
	shared := &Store{auditDB: &sql.DB{}, auditDriver: DialectSQLite}
	require.Equal(t, "management", shared.AuditLogs().chainID)
	separate := &Store{auditDB: &sql.DB{}, auditDriver: DialectPostgres, auditSeparate: true}
	require.Equal(t, "traffic", separate.AuditLogs().chainID)
	auditOnly := &auditOnlyStore{db: &sql.DB{}, dialect: DialectPostgres}
	require.Equal(t, "traffic", auditOnly.AuditLogs().chainID)
}

func setBuildingChain(t *testing.T, ctx context.Context, database *sql.DB, chainID, mode string) {
	t.Helper()
	result, err := database.ExecContext(ctx, `
UPDATE chain_state
SET status = 'BUILDING', mode = $1, chain_instance_id = $2, head_seq = 0,
    head_id = NULL, head_hash = NULL, updated_at = CURRENT_TIMESTAMP
WHERE chain_id = $3`, mode, chainAppendTestInstanceID, chainID)
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)
}

func resetChainAppendTestData(t *testing.T, ctx context.Context, database *sql.DB, chainID string) {
	t.Helper()
	_, err := database.ExecContext(ctx, "DELETE FROM audit_logs")
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `
UPDATE chain_state
SET status = 'DISABLED', mode = NULL, chain_instance_id = NULL, head_seq = 0,
    head_id = NULL, head_hash = NULL, updated_at = CURRENT_TIMESTAMP
WHERE chain_id = $1`, chainID)
	require.NoError(t, err)
}

func assertNullChainColumns(t *testing.T, ctx context.Context, database *sql.DB, ids []int64) {
	t.Helper()
	for _, id := range ids {
		var sequence, previousHash, selfHash, keyVersion, formatVersion sql.NullString
		err := database.QueryRowContext(ctx, `
SELECT chain_seq, prev_hash, self_hash, chain_key_version, chain_format_version
FROM audit_logs WHERE id = $1`, id).Scan(&sequence, &previousHash, &selfHash, &keyVersion, &formatVersion)
		require.NoError(t, err)
		for name, value := range map[string]sql.NullString{
			"chain_seq": sequence, "prev_hash": previousHash, "self_hash": selfHash,
			"chain_key_version": keyVersion, "chain_format_version": formatVersion,
		} {
			require.False(t, value.Valid, "%s for audit id %d", name, id)
		}
	}
}

func loadStoredAuditChainRows(t *testing.T, ctx context.Context, database *sql.DB) []storedAuditChainRow {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT id, chain_seq, prev_hash, self_hash, chain_key_version, chain_format_version
FROM audit_logs
WHERE chain_seq IS NOT NULL
ORDER BY chain_seq`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	result := make([]storedAuditChainRow, 0)
	for rows.Next() {
		var row storedAuditChainRow
		require.NoError(t, rows.Scan(&row.ID, &row.Sequence, &row.PreviousHash, &row.SelfHash, &row.KeyVersion, &row.FormatVersion))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}

func assertStoredChainHash(
	t *testing.T,
	auditLog model.AuditLog,
	stored storedAuditChainRow,
	chainID, instanceID string,
	key []byte,
) {
	t.Helper()
	require.Equal(t, auditLog.ID, stored.ID)
	require.Equal(t, 1, stored.FormatVersion)
	previousBytes, err := hex.DecodeString(stored.PreviousHash)
	require.NoError(t, err)
	require.Len(t, previousBytes, 32)
	var previous [32]byte
	copy(previous[:], previousBytes)
	canonical, err := auditchain.EncodeCanonical(auditChainRowForTest(auditLog))
	require.NoError(t, err)
	algorithm := auditchain.AlgorithmSHA256
	if key != nil {
		algorithm = auditchain.AlgorithmHMACSHA256
	}
	envelope := auditchain.NewEnvelope(
		instanceID, chainID, algorithm, int64(stored.KeyVersion), stored.Sequence, previous, canonical,
	)
	encoded, err := auditchain.EncodeEnvelope(envelope)
	require.NoError(t, err)
	want, err := auditchain.HashEnvelope(envelope, encoded, key)
	require.NoError(t, err)
	require.Equal(t, want, stored.SelfHash)
}

func auditChainRowForTest(auditLog model.AuditLog) auditchain.Row {
	return auditchain.Row{
		ID: auditLog.ID, TS: auditLog.TS.UTC().Format("2006-01-02T15:04:05.000000Z"),
		AgentID: auditLog.AgentID, DatasourceID: auditLog.DatasourceID,
		SessionID: auditLog.SessionID, ConversationID: auditLog.ConversationID,
		MCPTool: auditLog.MCPTool, DBType: auditLog.DBType, SQLRaw: auditLog.SQLRaw,
		SQLNorm: auditLog.SQLNorm, StmtType: auditLog.StmtType, Objects: auditLog.Objects,
		Decision: auditLog.Decision, RuleHits: auditLog.RuleHits,
		RiskLevel: testInt64Pointer(auditLog.RiskLevel), EstRows: auditLog.EstRows,
		RowsReturned: testInt64Pointer(auditLog.RowsReturned), LatencyMS: auditLog.LatencyMS,
		ClientIP: auditLog.ClientIP, ModelName: auditLog.ModelName, ErrorMsg: auditLog.ErrorMsg,
		ErrorCode: auditLog.ErrorCode, Action: auditLog.Action, ActorType: auditLog.ActorType,
		ActorID: auditLog.ActorID, DetailsJSON: auditLog.DetailsJSON, EventUUID: auditLog.EventUUID,
	}
}

func testInt64Pointer(value *int) *int64 {
	if value == nil {
		return nil
	}
	converted := int64(*value)
	return &converted
}

func assertChainHead(t *testing.T, ctx context.Context, database *sql.DB, chainID string, sequence, id int64, hash string) {
	t.Helper()
	state := loadChainState(t, ctx, database, chainID)
	require.Equal(t, sequence, state.HeadSeq)
	require.Equal(t, id, *state.HeadID)
	require.Equal(t, hash, *state.HeadHash)
}

func loadChainState(t *testing.T, ctx context.Context, database *sql.DB, chainID string) ChainState {
	t.Helper()
	repository := &ChainStateRepository{repositoryBase: repositoryBase{db: database, dialect: dialectForTestDatabase(database)}}
	state, err := repository.Get(ctx, chainID)
	require.NoError(t, err)
	return state
}

func dialectForTestDatabase(database *sql.DB) Dialect {
	// The pgx stdlib driver reports as *stdlib.Driver (no "pgx" substring),
	// while the SQLite driver is *sqlite.SQLiteDriver. Detect SQLite explicitly
	// and treat every other registered driver as PostgreSQL (the only two
	// drivers used by the store test suite).
	if strings.Contains(fmt.Sprintf("%T", database.Driver()), "sqlite") {
		return DialectSQLite
	}
	return DialectPostgres
}

func countAuditLogs(t *testing.T, ctx context.Context, database *sql.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&count))
	return count
}

func fullAuditLogForChainTest(label string, eventUUID *string) model.AuditLog {
	risk, rowsReturned := 7, 9
	estRows, latency := int64(123), int64(45)
	values := []string{
		"agent-" + label, "datasource-" + label, "session-" + label, "conversation-" + label,
		"tool-" + label, "postgres", "SELECT * FROM t WHERE label='" + label + "'", "select * from t",
		"SELECT", `["public.t"]`, `["R001"]`, "127.0.0.1", "model-" + label,
		"", "TEST_ERROR", "query.execute", "agent", "actor-" + label, `{"label":"` + label + `"}`,
	}
	// error_msg/error_code remain NULL because decision is allow. The empty
	// string slot deliberately exercises a present empty value through action.
	return model.AuditLog{
		AgentID: &values[0], DatasourceID: &values[1], SessionID: &values[2], ConversationID: &values[3],
		MCPTool: &values[4], DBType: &values[5], SQLRaw: &values[6], SQLNorm: &values[7],
		StmtType: &values[8], Objects: &values[9], Decision: "allow", RuleHits: &values[10],
		RiskLevel: &risk, EstRows: &estRows, RowsReturned: &rowsReturned, LatencyMS: &latency,
		ClientIP: &values[11], ModelName: &values[12], Action: &values[15], ActorType: &values[16],
		ActorID: &values[17], DetailsJSON: &values[18], EventUUID: eventUUID,
	}
}
