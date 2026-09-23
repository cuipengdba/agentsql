package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

const testCommitment2 = "2222222222222222222222222222222222222222222222222222222222222222"

func TestRegisterStandbysWithAuditRollsBackTogether(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	badAudit := model.AuditLog{Decision: "invalid"}
	err := opened.RedactionKeys().RegisterStandbysWithAudit(ctx, []RedactionKeyRegistration{{
		ID: "1", Commitment: strings.Repeat("a", 64), ConfigRevision: "r1",
	}}, ManagementAuditWrite{Audit: &badAudit})
	require.Error(t, err)
	_, getErr := opened.RedactionKeys().Get(ctx, "1")
	require.ErrorIs(t, getErr, ErrNotFound)
}

func TestRedactionKeyRepositoryStateMachineAndNumericOrder(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.RedactionKeys()

	require.NoError(t, repository.RegisterStandby(ctx, "10", strings.Repeat("a", 64), "ten", "r1", nil))
	require.NoError(t, repository.RegisterStandby(ctx, "2", testCommitment2, "two", "r1", nil))
	listed, err := repository.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"2", "10"}, []string{listed[0].ID, listed[1].ID})

	before, after, outcome, err := repository.MarkActiveCAS(ctx, "2")
	require.NoError(t, err)
	require.Equal(t, model.RedactionKeyStateStandby, before.State)
	require.Equal(t, model.RedactionKeyStateActive, after.State)
	require.Equal(t, RedactionActivationFirst, outcome)
	require.NotNil(t, after.ActivatedAt)

	_, _, outcome, err = repository.MarkActiveCAS(ctx, "10")
	require.NoError(t, err)
	require.Equal(t, RedactionActivationSwitched, outcome)
	old, err := repository.Get(ctx, "2")
	require.NoError(t, err)
	require.Equal(t, model.RedactionKeyStateLegacy, old.State)

	_, _, outcome, err = repository.MarkActiveCAS(ctx, "2")
	require.NoError(t, err)
	require.Equal(t, RedactionActivationSwitched, outcome)
	require.ErrorIs(t, repository.MarkRetired(ctx, "2"), ErrRedactionKeyTransition)
	require.NoError(t, repository.MarkRetired(ctx, "10"))
	require.ErrorIs(t, repository.MarkRetired(ctx, "10"), ErrRedactionKeyTransition)
	require.ErrorIs(t, repository.RegisterStandby(ctx, "10", strings.Repeat("a", 64), "changed", "r2", nil), ErrRedactionKeyTransition)
}

func TestRedactionKeyRepositoryValidationConflictAndRollback(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.RedactionKeys()
	for _, id := range []string{"", "02", "2.0", "+2", "0x2", "0", "10000"} {
		require.ErrorIs(t, repository.RegisterStandby(ctx, id, testCommitment2, "", "", nil), ErrInvalidRedactionKeyID, id)
	}
	for _, commitment := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		require.ErrorIs(t, repository.RegisterStandby(ctx, "2", commitment, "", "", nil), ErrInvalidKeyCommitment, commitment)
	}
	require.NoError(t, repository.RegisterStandby(ctx, "2", testCommitment2, "first", "r1", nil))
	require.ErrorIs(t, repository.RegisterStandby(ctx, "2", strings.Repeat("3", 64), "second", "r2", nil), ErrRedactionKeyCommitmentConflict)
	stored, err := repository.Get(ctx, "2")
	require.NoError(t, err)
	require.Equal(t, testCommitment2, stored.Commitment)
	require.Equal(t, "first", stored.Label)

	require.NoError(t, repository.RegisterStandby(ctx, "3", strings.Repeat("3", 64), "", "", nil))
	require.NoError(t, repository.RegisterStandby(ctx, "4", strings.Repeat("4", 64), "", "", nil))
	require.NoError(t, repository.MarkRetired(ctx, "4"))
	_, _, outcome, err := repository.MarkActiveCAS(ctx, "4")
	require.ErrorIs(t, err, ErrRedactionKeyTransition)
	require.Equal(t, RedactionActivationRolledBack, outcome)
	version3, err := repository.Get(ctx, "3")
	require.NoError(t, err)
	require.Equal(t, model.RedactionKeyStateStandby, version3.State)
}

func TestRedactionKeyRepositoryConcurrentFirstActivation(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.RedactionKeys()
	require.NoError(t, repository.RegisterStandby(ctx, "2", testCommitment2, "", "", nil))

	var successful atomic.Int32
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, _, err := repository.MarkActiveCAS(ctx, "2")
			if err == nil {
				successful.Add(1)
			}
		}()
	}
	wait.Wait()
	require.EqualValues(t, 1, successful.Load())
	var active int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM redaction_key_versions WHERE state='active'`).Scan(&active))
	require.Equal(t, 1, active)
}

func TestRedactionKeyRepositoryRejectsMultipleActiveRows(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, execRedactionStatement(ctx, opened.metaDB, `DROP INDEX ux_redaction_key_versions_active`))
	require.NoError(t, execRedactionStatement(ctx, opened.metaDB, `INSERT INTO redaction_key_versions(id,state,commitment,created_at,updated_at) VALUES('2','active',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, testCommitment2))
	require.NoError(t, execRedactionStatement(ctx, opened.metaDB, `INSERT INTO redaction_key_versions(id,state,commitment,created_at,updated_at) VALUES('3','active',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, strings.Repeat("3", 64)))
	require.NoError(t, execRedactionStatement(ctx, opened.metaDB, `INSERT INTO redaction_key_versions(id,state,commitment,created_at,updated_at) VALUES('4','standby',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, strings.Repeat("4", 64)))
	_, _, outcome, err := opened.RedactionKeys().MarkActiveCAS(ctx, "4")
	require.ErrorIs(t, err, ErrRedactionKeyIntegrity)
	require.Equal(t, RedactionActivationRolledBack, outcome)
}

func TestRedactionKeyRepositorySwitchFailureRollsBackDemotion(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.RedactionKeys()
	require.NoError(t, repository.RegisterStandby(ctx, "2", testCommitment2, "", "", nil))
	require.NoError(t, repository.RegisterStandby(ctx, "3", strings.Repeat("3", 64), "", "", nil))
	_, _, _, err := repository.MarkActiveCAS(ctx, "2")
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `CREATE TRIGGER reject_redaction_three_active BEFORE UPDATE OF state ON redaction_key_versions WHEN NEW.id='3' AND NEW.state='active' BEGIN SELECT RAISE(ABORT,'injected activation failure'); END`)
	require.NoError(t, err)
	_, _, outcome, err := repository.MarkActiveCAS(ctx, "3")
	require.Error(t, err)
	require.Equal(t, RedactionActivationRolledBack, outcome)
	active, err := repository.Get(ctx, "2")
	require.NoError(t, err)
	require.Equal(t, model.RedactionKeyStateActive, active.State)
	target, err := repository.Get(ctx, "3")
	require.NoError(t, err)
	require.Equal(t, model.RedactionKeyStateStandby, target.State)
}

func TestManagementAuditOutboxLifecycleAndSafety(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.Outbox()
	event := model.ManagementAuditOutbox{EventUUID: "event-1", Action: "redaction.register", ActorType: "admin", ActorID: "operator", DetailsJSON: `{"id":"2"}`}
	tx, err := opened.metaDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repository.Append(ctx, tx, event))
	require.NoError(t, tx.Commit())

	claimed, err := repository.ClaimBatch(ctx, "worker-1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "worker-1", claimed[0].ClaimedBy)
	require.NotNil(t, claimed[0].ClaimedAt)
	require.True(t, claimed[0].NextAttemptAt.After(*claimed[0].ClaimedAt))

	secret := "FAKE_KEY_MATERIAL_7ef441"
	require.NoError(t, repository.MarkFailed(ctx, event.EventUUID, "postgres://user:"+secret+"@db/app "+strings.Repeat("ordinary failure ", 60)+"password="+secret, 0))
	var attempts int
	var lastError string
	var claimedBy sql.NullString
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT attempts,last_error,claimed_by FROM management_audit_outbox WHERE event_uuid=?`, event.EventUUID).Scan(&attempts, &lastError, &claimedBy))
	require.Equal(t, 1, attempts)
	require.NotContains(t, lastError, secret)
	require.Len(t, lastError, maxOutboxErrorText)
	require.False(t, claimedBy.Valid)

	claimed, err = repository.ClaimBatch(ctx, "worker-2", 1, 0)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, repository.MarkDelivered(ctx, event.EventUUID))
	count, age, err := repository.PendingStats(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Zero(t, age)

	tx, err = opened.metaDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = repository.Append(ctx, tx, event)
	require.ErrorIs(t, err, ErrOutboxEventAlreadyExists)
	require.NoError(t, tx.Rollback())

	unsafeEvent := event
	unsafeEvent.EventUUID = "event-secret"
	unsafeEvent.DetailsJSON = `{"key_b64":"` + secret + `"}`
	tx, err = opened.metaDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = repository.Append(ctx, tx, unsafeEvent)
	require.ErrorIs(t, err, ErrUnsafeManagementAuditDetails)
	require.NotContains(t, err.Error(), secret)
	require.NoError(t, tx.Rollback())

	unsafeEvent.EventUUID = "event-sample"
	unsafeEvent.DetailsJSON = `{"sample_value":"raw customer value"}`
	tx, err = opened.metaDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = repository.Append(ctx, tx, unsafeEvent)
	require.ErrorIs(t, err, ErrUnsafeManagementAuditDetails)
	require.NoError(t, tx.Rollback())

	safePhysicalKey := event
	safePhysicalKey.EventUUID = "event-physical-key"
	safePhysicalKey.DetailsJSON = `{"table":"customers","column":"secret","algo":"block"}`
	tx, err = opened.metaDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repository.Append(ctx, tx, safePhysicalKey))
	require.NoError(t, tx.Commit())
}

func TestAuditEventUUIDIdempotency(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	eventUUID := "audit-event-1"
	_, err := opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow", EventUUID: &eventUUID})
	require.NoError(t, err)
	_, err = opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow", EventUUID: &eventUUID})
	require.ErrorIs(t, err, ErrAuditEventAlreadyDelivered)
}

func TestOpenMetadataOnlyIgnoresUnreachableAudit(t *testing.T) {
	ctx := context.Background()
	view, err := OpenMetadataOnly(ctx, MetadataOptions{
		Driver: DialectSQLite, SQLitePath: filepath.Join(t.TempDir(), "metadata.db"), AutoMigrate: true,
		Audit: AuditOptions{Separate: true, Driver: DialectPostgres, PostgresDSN: "postgres://invalid.invalid:1/unreachable"},
	}, []byte(testSecret))
	require.NoError(t, err)
	current, latest, err := view.MigrationVersions(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, current)
	require.NoError(t, view.RedactionKeys().RegisterStandby(ctx, "2", testCommitment2, "", "", nil))
	require.NoError(t, view.Close())
}

func TestIndependentStoreViewsAreNarrow(t *testing.T) {
	methodNames := func(interfaceType reflect.Type) []string {
		names := make([]string, interfaceType.NumMethod())
		for index := range interfaceType.NumMethod() {
			names[index] = interfaceType.Method(index).Name
		}
		return names
	}
	require.Equal(t, []string{"Close", "MigrationVersions", "Outbox", "RedactionKeys"}, methodNames(reflect.TypeOf((*MetadataRegistryView)(nil)).Elem()))
	require.Equal(t, []string{"AuditLogs", "Close", "MigrationVersions"}, methodNames(reflect.TypeOf((*AuditStoreView)(nil)).Elem()))
}

func TestRedactionStorageDialectMatrix(t *testing.T) {
	forEachStore(t, func(t *testing.T, opened *Store) {
		ctx := context.Background()
		registry := opened.RedactionKeys()
		require.NoError(t, registry.RegisterStandby(ctx, "2", testCommitment2, "matrix", "r1", nil))
		var successful atomic.Int32
		var wait sync.WaitGroup
		for range 16 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, _, _, activateErr := registry.MarkActiveCAS(ctx, "2")
				if activateErr == nil {
					successful.Add(1)
				}
			}()
		}
		wait.Wait()
		require.EqualValues(t, 1, successful.Load())

		outbox := opened.Outbox()
		for _, id := range []string{"matrix-1", "matrix-2"} {
			tx, err := opened.metaDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			require.NoError(t, outbox.Append(ctx, tx, model.ManagementAuditOutbox{
				EventUUID: id, Action: "matrix", ActorType: "test", ActorID: "test", DetailsJSON: `{}`,
			}))
			require.NoError(t, tx.Commit())
		}
		first, err := outbox.ClaimBatch(ctx, "worker-a", 1, time.Minute)
		require.NoError(t, err)
		second, err := outbox.ClaimBatch(ctx, "worker-b", 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, first, 1)
		require.Len(t, second, 1)
		require.NotEqual(t, first[0].EventUUID, second[0].EventUUID)

		eventUUID := "matrix-audit"
		_, err = opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow", EventUUID: &eventUUID})
		require.NoError(t, err)
		_, err = opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow", EventUUID: &eventUUID})
		require.ErrorIs(t, err, ErrAuditEventAlreadyDelivered)
	})
}

func execRedactionStatement(ctx context.Context, database *sql.DB, statement string, arguments ...any) error {
	_, err := database.ExecContext(ctx, statement, arguments...)
	return err
}

var _ MetadataRegistryView = (*metadataOnlyStore)(nil)
var _ AuditStoreView = (*auditOnlyStore)(nil)
