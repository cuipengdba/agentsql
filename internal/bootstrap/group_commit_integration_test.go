package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestCombinedSQLiteAssemblyUsesGroupCommitForManagementRecorder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "combined-group.db")
	runtime, err := Assemble(ctx, bootstrapTestConfig(path), bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NotNil(t, runtime.groupSink)

	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	exerciseCombinedGroupCommit(t, ctx, runtime, database, store.DialectSQLite, 96)

	sink := runtime.groupSink
	require.NoError(t, runtime.Close())
	require.Zero(t, sink.Stats().ActiveFlusherGoroutines)
	require.Error(t, runtime.Store.Ping(ctx), "store must close only after the sink flusher exits")
}

func exerciseCombinedGroupCommit(
	t *testing.T,
	ctx context.Context,
	runtime *Runtime,
	database *sql.DB,
	dialect store.Dialect,
	count int,
) {
	t.Helper()
	// The production monitor is orthogonal to writer assembly; stop its startup
	// verification so provisioning through a second SQLite handle is deterministic.
	runtime.ChainMonitor.close()
	manifest := store.KeylessChainManifest{}
	provisioner := store.NewChainProvisioner(database, dialect, "management", manifest, store.BackfillConfig{})
	require.NoError(t, provisioner.Provision(ctx, "bootstrap-group-integration"))

	start := make(chan struct{})
	errorsByIndex := make([]error, count)
	recorded := make([]model.AuditLog, count)
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			action := fmt.Sprintf("management.concurrent.%03d", index)
			recorded[index], errorsByIndex[index] = runtime.ManagementAudit.Record(ctx, model.AuditLog{
				Decision: "allow", Action: &action,
			})
		}()
	}
	close(start)
	waitGroup.Wait()
	for index, recordErr := range errorsByIndex {
		require.NoErrorf(t, recordErr, "record management audit %d", index)
		require.Positive(t, recorded[index].ID)
		require.NotNil(t, recorded[index].EventUUID)
	}
	require.Eventually(t, func() bool {
		stats := runtime.groupSink.Stats()
		return stats.QueueDepth == 0 && stats.Waiters == 0 && stats.AdmissionTokens == 0
	}, 2*time.Second, 10*time.Millisecond)
	stats := runtime.groupSink.Stats()
	require.Equal(t, uint64(count), stats.Acknowledgements)
	require.Less(t, stats.Batches, uint64(count), "concurrent recorder traffic should be combined")

	outcome, err := store.NewChainVerifier(database, dialect, "management", manifest).Verify(ctx)
	require.NoError(t, err)
	require.True(t, outcome.Valid)
	require.Contains(t, []string{"VALID", "VALID_AT_OBSERVED_HEAD"}, outcome.Result)
}

func TestAuditBatchBackendForwardsAndRejectsNil(t *testing.T) {
	var nilBackend *auditBatchBackend
	_, err := nilBackend.InsertBatch(context.Background(), nil)
	require.ErrorContains(t, err, "backend is unavailable")

	appender := &recordingBatchAppender{}
	backend := newAuditBatchBackend(appender)
	input := []model.AuditLog{{Decision: "allow"}, {Decision: "deny"}}
	rows, err := backend.InsertBatch(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, input, rows)
	require.Equal(t, 1, appender.calls)
}

type recordingBatchAppender struct {
	calls int
}

func (appender *recordingBatchAppender) AppendBatch(
	_ context.Context,
	batch []model.AuditLog,
) ([]model.AuditLog, error) {
	appender.calls++
	return batch, nil
}
