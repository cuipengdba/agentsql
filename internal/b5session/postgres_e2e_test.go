package b5session

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestS3Postgres14And18SharedAdmissionAndReaper(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:14/18 S3 integration test")
	}
	for _, image := range []string{"postgres:14", "postgres:18"} {
		image := image
		t.Run(image, func(t *testing.T) {
			ctx := context.Background()
			dsn := startS3Postgres(t, ctx, image)
			db, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			db.SetMaxOpenConns(32)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, InstallPostgresSchema(ctx, db))
			ledger, err := NewPostgresLedger(db)
			require.NoError(t, err)
			require.NoError(t, testSQLAdmissionNoFanout(ctx, ledger))
			require.NoError(t, testSQLTombstoneBounds(ctx, ledger))
			testPGDialCrashWindows(t, ctx, db, dsn, ledger)
		})
	}
}

func testSQLAdmissionNoFanout(ctx context.Context, ledger *SQLLedger) error {
	if _, err := ledger.ConfigureBudget(ctx, Budget{DatasourceID: "admission", HardLimit: 10, PlanBytesLimit: 8 << 20}); err != nil {
		return err
	}
	var group sync.WaitGroup
	var mu sync.Mutex
	connections, plans := 0, 0
	instances := make([]*SQLLedger, 4)
	for index := range instances {
		instances[index], _ = NewPostgresLedger(ledger.db)
	}
	for instance := 0; instance < 4; instance++ {
		for attempt := 0; attempt < 10; attempt++ {
			instance, attempt := instance, attempt
			group.Add(1)
			go func() {
				defer group.Done()
				id := fmt.Sprintf("pg-claim-%d-%d", instance, attempt)
				if _, claimErr := instances[instance].AcquireClaim(ctx, ClaimRequest{ClaimID: id, DatasourceID: "admission", Kind: ClaimDirectPinned, OwnerInstanceID: fmt.Sprintf("instance-%d", instance), OwnerIncarnation: fmt.Sprintf("inc-%d", instance), Capacity: 1, MaxOpenConns: 1}); claimErr == nil {
					mu.Lock()
					connections++
					mu.Unlock()
				}
			}()
		}
		for attempt := 0; attempt < 4; attempt++ {
			instance, attempt := instance, attempt
			group.Add(1)
			go func() {
				defer group.Done()
				id := fmt.Sprintf("pg-plan-%d-%d", instance, attempt)
				if planErr := instances[instance].ReservePlan(ctx, PlanLease{ID: id, DatasourceID: "admission", OwnerInstanceID: fmt.Sprintf("instance-%d", instance), OwnerIncarnation: fmt.Sprintf("inc-%d", instance), Bytes: 1 << 20}); planErr == nil {
					mu.Lock()
					plans++
					mu.Unlock()
				}
			}()
		}
	}
	group.Wait()
	if connections != 10 || plans != 8 {
		return fmt.Errorf("fanout: connections=%d plans=%d", connections, plans)
	}
	budget, err := ledger.Budget(ctx, "admission")
	if err != nil {
		return err
	}
	if budget.ConnectionUnitsUsed != 10 || budget.PlanBytesUsed != 8<<20 {
		return fmt.Errorf("budget drift: %+v", budget)
	}
	return nil
}

func testSQLTombstoneBounds(ctx context.Context, ledger *SQLLedger) error {
	now := time.Now().UTC()
	if err := ledger.ConfigureTombstones(ctx, TombstoneBudget{MaxEntries: 2, MaxBytes: 512, MaxChurnPerSecond: 2}, now); err != nil {
		return err
	}
	for index := 0; index < 2; index++ {
		value := Tombstone{ID: fmt.Sprintf("tomb-%d", index), SessionIDDigest: DigestSessionID(fmt.Sprintf("session-%d", index)), TerminalCode: "TERMINAL_NOT_COMMITTED", FinalSeq: uint64(index), OwnerEpoch: 1, EventDigest: DigestSessionID(fmt.Sprintf("event-%d", index)), ExpiresAt: now.Add(time.Minute)}
		if err := ledger.PutTombstone(ctx, value, now); err != nil {
			return err
		}
	}
	overflow := Tombstone{ID: "tomb-overflow", SessionIDDigest: DigestSessionID("s"), TerminalCode: "TERMINAL_UNKNOWN", OwnerEpoch: 1, EventDigest: DigestSessionID("e"), ExpiresAt: now.Add(time.Minute)}
	if err := ledger.PutTombstone(ctx, overflow, now); err == nil {
		return fmt.Errorf("tombstone entry/churn cap not enforced")
	}
	entries, charged, err := ledger.TombstoneUsage(ctx)
	if err != nil {
		return err
	}
	if entries != 2 || charged != 512 {
		return fmt.Errorf("tombstone usage entries=%d bytes=%d", entries, charged)
	}
	return nil
}

func testPGDialCrashWindows(t *testing.T, ctx context.Context, db *sql.DB, dsn string, ledger *SQLLedger) {
	t.Helper()
	_, err := ledger.ConfigureBudget(ctx, Budget{DatasourceID: "reaper", HardLimit: 2, PlanBytesLimit: 1024})
	require.NoError(t, err)
	codec, err := NewDialIdentityCodec(bytes(32, 6))
	require.NoError(t, err)
	issuer := DialPermitIssuer{Ledger: ledger, Codec: codec}
	inventory, err := NewPGInventory(ctx, db)
	require.NoError(t, err)
	windows := []struct {
		name                       string
		dialStarted, connect, bind bool
	}{
		{name: "before-tcp-connect"},
		{name: "tcp-connect", dialStarted: true},
		{name: "authentication-complete", dialStarted: true, connect: true},
		{name: "startup-complete-before-child-cas", dialStarted: true, connect: true},
		{name: "after-child-cas", dialStarted: true, connect: true, bind: true},
	}
	for index, window := range windows {
		window := window
		t.Run(window.name, func(t *testing.T) {
			owner := fmt.Sprintf("owner-%d", index)
			incarnation := fmt.Sprintf("inc-%d", index)
			claim, claimErr := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: "reaper-claim-" + window.name, DatasourceID: "reaper", Kind: ClaimPoolEnvelope, OwnerInstanceID: owner, OwnerIncarnation: incarnation, Capacity: 1, MaxOpenConns: 1})
			require.NoError(t, claimErr)
			lease, identity, leaseErr := issuer.Issue(ctx, claim.ID, claim.Generation)
			require.NoError(t, leaseErr)
			if window.dialStarted {
				lease, leaseErr = ledger.MarkDialStarted(ctx, lease.ID, lease.Generation)
				require.NoError(t, leaseErr)
			}
			var client *sql.DB
			if window.connect {
				client, leaseErr = sql.Open("pgx", dsnWithApplicationName(dsn, identity.ApplicationName))
				require.NoError(t, leaseErr)
				client.SetMaxOpenConns(1)
				client.SetMaxIdleConns(1)
				require.NoError(t, client.PingContext(ctx))
				if window.name == "authentication-complete" {
					require.NoError(t, client.Close())
					client = nil
				}
			}
			if window.bind {
				observed, observeErr := inventory.ObservePermit(ctx, identity.ApplicationName)
				require.NoError(t, observeErr)
				require.Len(t, observed.Backends, 1)
				lease, leaseErr = ledger.BindBackend(ctx, lease.ID, lease.Generation, observed.Backends[0])
				require.NoError(t, leaseErr)
			}
			report, reapErr := (Reaper{Ledger: ledger, Inventory: inventory}).ReapOwner(ctx, owner, incarnation)
			require.NoError(t, reapErr)
			require.Equal(t, 1, report.ClaimsReleased)
			if client != nil {
				require.NoError(t, client.Close())
			}
			budget, budgetErr := ledger.Budget(ctx, "reaper")
			require.NoError(t, budgetErr)
			require.Zero(t, budget.ConnectionUnitsUsed)
		})
	}
}

func startS3Postgres(t *testing.T, ctx context.Context, image string) string {
	t.Helper()
	container, err := postgrescontainer.Run(ctx, image, postgrescontainer.WithDatabase("agentsql_s3"), postgrescontainer.WithUsername("agentsql"), postgrescontainer.WithPassword("s3-password"), postgrescontainer.BasicWaitStrategies())
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("postgres://agentsql:s3-password@%s:%s/agentsql_s3?sslmode=disable", host, port.Port())
}

func dsnWithApplicationName(dsn, applicationName string) string {
	parsed, _ := url.Parse(dsn)
	query := parsed.Query()
	query.Set("application_name", applicationName)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
