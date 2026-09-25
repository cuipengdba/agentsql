package b5session

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
)

func TestLeaseDispositionTableAndQuarantineCharge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		disposition b5.ConnectionDisposition
		mutate      func(*DispositionProof)
		want        LeaseState
		charged     bool
		wantErr     bool
	}{
		{name: "released-needs-transfer", disposition: b5.DispositionReleased, mutate: func(p *DispositionProof) { p.PoolTransfer = false }, wantErr: true},
		{name: "released", disposition: b5.DispositionReleased, want: LeaseFree, charged: true},
		{name: "discarded-needs-absence", disposition: b5.DispositionDiscarded, mutate: func(p *DispositionProof) { p.BackendAbsence = false; p.InventoryEpoch = 0 }, wantErr: true},
		{name: "discarded", disposition: b5.DispositionDiscarded, want: LeaseBackendAbsent},
		{name: "unconfirmed", disposition: b5.DispositionDiscardUnconfirmed, want: LeaseQuarantined, charged: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, lease := liveLease(t)
			proof := terminalProof(t, test.disposition, test.name)
			if test.mutate != nil {
				test.mutate(&proof)
				proof.Digest = dispositionProofDigest(proof)
			}
			got, err := ledger.ApplyDisposition(context.Background(), lease.ID, lease.Generation, proof)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.State != test.want || got.Charged != test.charged {
				t.Fatalf("lease = %+v", got)
			}
			budget, _ := ledger.Budget(context.Background(), "ds")
			if budget.ConnectionUnitsUsed != 1 {
				t.Fatalf("disposition prematurely released claim: %+v", budget)
			}
		})
	}
}

func TestSharedAdmissionDoesNotFanOutAcrossFourInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger := NewMemoryLedger()
	_, _ = ledger.ConfigureBudget(ctx, Budget{DatasourceID: "ds", HardLimit: 10, PlanBytesLimit: 8 << 20})
	var group sync.WaitGroup
	var mu sync.Mutex
	connectionAccepted, planAccepted := 0, 0
	for instance := 0; instance < 4; instance++ {
		for attempt := 0; attempt < 10; attempt++ {
			instance, attempt := instance, attempt
			group.Add(1)
			go func() {
				defer group.Done()
				id := string(rune('a'+instance)) + string(rune('A'+attempt))
				if _, err := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: "claim-" + id, DatasourceID: "ds", Kind: ClaimDirectPinned, OwnerInstanceID: "instance-" + id, OwnerIncarnation: "inc-" + id, Capacity: 1, MaxOpenConns: 1}); err == nil {
					mu.Lock()
					connectionAccepted++
					mu.Unlock()
				}
			}()
		}
	}
	for instance := 0; instance < 4; instance++ {
		for attempt := 0; attempt < 4; attempt++ {
			instance, attempt := instance, attempt
			group.Add(1)
			go func() {
				defer group.Done()
				id := string(rune('a'+instance)) + string(rune('0'+attempt))
				if err := ledger.ReservePlan(ctx, PlanLease{ID: "plan-" + id, DatasourceID: "ds", OwnerInstanceID: "instance", OwnerIncarnation: id, Bytes: 1 << 20}); err == nil {
					mu.Lock()
					planAccepted++
					mu.Unlock()
				}
			}()
		}
	}
	group.Wait()
	if connectionAccepted != 10 || planAccepted != 8 {
		t.Fatalf("accepted connections=%d plans=%d", connectionAccepted, planAccepted)
	}
	budget, _ := ledger.Budget(ctx, "ds")
	if budget.ConnectionUnitsUsed != 10 || budget.PlanBytesUsed != 8<<20 {
		t.Fatalf("budget = %+v", budget)
	}
}

func TestDialIdentityAuthenticatedAndBounded(t *testing.T) {
	t.Parallel()
	codec, _ := NewDialIdentityCodec(bytes(32, 9))
	identity, err := codec.New("owner-incarnation")
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.ApplicationName) > 63 {
		t.Fatalf("application_name bytes=%d", len(identity.ApplicationName))
	}
	verified, err := codec.Verify(identity.ApplicationName, "owner-incarnation")
	if err != nil || verified.PermitID != identity.PermitID {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	if _, err := codec.Verify(identity.ApplicationName, "replacement-incarnation"); !errors.Is(err, ErrInvalidDialIdentity) {
		t.Fatalf("wrong owner error=%v", err)
	}
}

func TestPoolMaxOpenConnsAndAuxiliaryClaimsShareBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger := NewMemoryLedger()
	_, _ = ledger.ConfigureBudget(ctx, Budget{DatasourceID: "ds", HardLimit: 6, PlanBytesLimit: 1024})
	pool, err := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: "pool", DatasourceID: "ds", Kind: ClaimPoolEnvelope, OwnerInstanceID: "pool-owner", OwnerIncarnation: "pool-inc", Capacity: 3, MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	codec, _ := NewDialIdentityCodec(bytes(32, 4))
	issuer := DialPermitIssuer{Ledger: ledger, Codec: codec}
	for index := 0; index < 2; index++ {
		if _, _, err := issuer.Issue(ctx, pool.ID, pool.Generation); err != nil {
			t.Fatalf("permit %d: %v", index, err)
		}
	}
	if _, _, err := issuer.Issue(ctx, pool.ID, pool.Generation); !errors.Is(err, ErrAdmissionDenied) {
		t.Fatalf("MaxOpenConns permit error=%v", err)
	}
	for index, kind := range []ClaimKind{ClaimMigration, ClaimHealth, ClaimAdmin} {
		_, err := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: string(kind), DatasourceID: "ds", Kind: kind, OwnerInstanceID: "aux", OwnerIncarnation: string(kind), Capacity: 1, MaxOpenConns: 1})
		if index < 3 && err != nil {
			t.Fatalf("%s claim: %v", kind, err)
		}
	}
	if _, err := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: "overflow", DatasourceID: "ds", Kind: ClaimDirectPinned, OwnerInstanceID: "direct", OwnerIncarnation: "direct", Capacity: 1, MaxOpenConns: 1}); !errors.Is(err, ErrAdmissionDenied) {
		t.Fatalf("shared budget overflow=%v", err)
	}
}

func TestReaperCrashWindowsAndConservativeInventoryFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger := NewMemoryLedger()
	_, _ = ledger.ConfigureBudget(ctx, Budget{DatasourceID: "ds", HardLimit: 4, PlanBytesLimit: 1024})
	claim, _ := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: "envelope", DatasourceID: "ds", Kind: ClaimPoolEnvelope, OwnerInstanceID: "owner", OwnerIncarnation: "inc", Capacity: 4, MaxOpenConns: 4})
	codec, _ := NewDialIdentityCodec(bytes(32, 3))
	// Before TCP connect: a durable permit exists but DialStarted never ran.
	preTCPID, _ := codec.New("inc")
	_, _ = ledger.IssueDialPermit(ctx, claim.ID, claim.Generation, preTCPID.PermitID, preTCPID.ApplicationName)
	// TCP/auth/startup before child CAS: token is visible in inventory.
	preCASID, _ := codec.New("inc")
	preCAS, _ := ledger.IssueDialPermit(ctx, claim.ID, claim.Generation, preCASID.PermitID, preCASID.ApplicationName)
	preCAS, _ = ledger.MarkDialStarted(ctx, preCAS.ID, preCAS.Generation)
	backend := BackendIdentity{ServerID: "server", Database: "db", PID: 42, BackendStarted: time.Now().UTC(), DialToken: preCAS.DialToken}
	// After child CAS: durable exact backend identity exists.
	postCASID, _ := codec.New("inc")
	postCAS, _ := ledger.IssueDialPermit(ctx, claim.ID, claim.Generation, postCASID.PermitID, postCASID.ApplicationName)
	postCAS, _ = ledger.MarkDialStarted(ctx, postCAS.ID, postCAS.Generation)
	postBackend := BackendIdentity{ServerID: "server", Database: "db", PID: 43, BackendStarted: time.Now().UTC().Add(time.Second), DialToken: postCAS.DialToken}
	postCAS, _ = ledger.BindBackend(ctx, postCAS.ID, postCAS.Generation, postBackend)
	quarantine := terminalProof(t, b5.DispositionDiscardUnconfirmed, "q")
	postCAS, _ = ledger.ApplyDisposition(ctx, postCAS.ID, postCAS.Generation, quarantine)
	inventory := newFakeInventory()
	inventory.backends[preCAS.DialToken] = []BackendIdentity{backend}
	inventory.backends[postCAS.DialToken] = []BackendIdentity{postBackend}
	report, err := (Reaper{Ledger: ledger, Inventory: inventory}).ReapOwner(ctx, "owner", "inc")
	if err != nil {
		t.Fatal(err)
	}
	if report.ReconciledChildren != 3 || report.TerminatedBackends != 2 || report.CapacityReleased != 4 || report.ClaimsReleased != 1 {
		t.Fatalf("report=%+v", report)
	}
	budget, _ := ledger.Budget(ctx, "ds")
	if budget.ConnectionUnitsUsed != 0 {
		t.Fatalf("budget=%+v", budget)
	}

	// Inventory failure with a possibly live backend never frees its slot.
	claim2, _ := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: "envelope-2", DatasourceID: "ds", Kind: ClaimPoolEnvelope, OwnerInstanceID: "owner-2", OwnerIncarnation: "inc-2", Capacity: 1, MaxOpenConns: 1})
	id2, _ := codec.New("inc-2")
	lease2, _ := ledger.IssueDialPermit(ctx, claim2.ID, claim2.Generation, id2.PermitID, id2.ApplicationName)
	lease2, _ = ledger.MarkDialStarted(ctx, lease2.ID, lease2.Generation)
	inventory.fail = true
	report, err = (Reaper{Ledger: ledger, Inventory: inventory}).ReapOwner(ctx, "owner-2", "inc-2")
	if err == nil || !report.InventoryBlocked {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	budget, _ = ledger.Budget(ctx, "ds")
	if budget.ConnectionUnitsUsed != 1 {
		t.Fatalf("unknown backend claim released: %+v", budget)
	}
}

func TestFenceRejectsLateOwnerAndPIDReuseDoesNotRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, lease := liveLease(t)
	claims, err := ledger.FenceOwner(ctx, "owner", "inc")
	if err != nil || len(claims) != 1 {
		t.Fatalf("fence claims=%+v err=%v", claims, err)
	}
	if _, err := ledger.BeginTermination(ctx, lease.ID, lease.Generation); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("late owner transition error=%v", err)
	}
	// Inventory reports the same PID and token but a different backend start.
	// It is a replacement process and must neither be terminated nor release
	// the old charged slot.
	replacement := lease.Backend
	replacement.BackendStarted = replacement.BackendStarted.Add(time.Second)
	inventory := newFakeInventory()
	inventory.backends[lease.DialToken] = []BackendIdentity{replacement}
	report, err := (Reaper{Ledger: ledger, Inventory: inventory}).ReapOwner(ctx, "owner", "inc")
	if err == nil || !errors.Is(err, ErrBackendStillPresent) {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if report.TerminatedBackends != 0 || report.CapacityReleased != 0 {
		t.Fatalf("PID reuse affected backend/claim: %+v", report)
	}
	budget, _ := ledger.Budget(ctx, "ds")
	if budget.ConnectionUnitsUsed != 1 {
		t.Fatalf("PID reuse released claim: %+v", budget)
	}
}

func liveLease(t *testing.T) (*MemoryLedger, Lease) {
	t.Helper()
	ctx := context.Background()
	ledger := NewMemoryLedger()
	_, _ = ledger.ConfigureBudget(ctx, Budget{DatasourceID: "ds", HardLimit: 2, PlanBytesLimit: 1024})
	claim, _ := ledger.AcquireClaim(ctx, ClaimRequest{ClaimID: "claim", DatasourceID: "ds", Kind: ClaimPoolEnvelope, OwnerInstanceID: "owner", OwnerIncarnation: "inc", Capacity: 1, MaxOpenConns: 1})
	lease, _ := ledger.IssueDialPermit(ctx, claim.ID, claim.Generation, "permit", "token")
	lease, _ = ledger.MarkDialStarted(ctx, lease.ID, lease.Generation)
	lease, err := ledger.BindBackend(ctx, lease.ID, lease.Generation, BackendIdentity{ServerID: "server", Database: "db", PID: 1, BackendStarted: time.Now().UTC(), DialToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	return ledger, lease
}

func terminalProof(t *testing.T, disposition b5.ConnectionDisposition, material string) DispositionProof {
	t.Helper()
	source := sha256.Sum256([]byte(material))
	terminal := b5terminal.TerminalResolution{
		Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion,
		Disposition: disposition,
	}
	proof, err := DispositionProofFromTerminal(TerminalDisposition{Schema: terminal.Schema, SchemaVersion: terminal.SchemaVersion, Disposition: terminal.Disposition}, source, disposition == b5.DispositionReleased, disposition == b5.DispositionDiscarded, map[bool]uint64{true: 5}[disposition == b5.DispositionDiscarded])
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

type fakeInventory struct {
	mu       sync.Mutex
	epoch    uint64
	backends map[string][]BackendIdentity
	fail     bool
}

func newFakeInventory() *fakeInventory {
	return &fakeInventory{epoch: 10, backends: make(map[string][]BackendIdentity)}
}
func (i *fakeInventory) ObservePermit(_ context.Context, token string) (InventoryObservation, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.fail {
		return InventoryObservation{}, ErrInventoryUnavailable
	}
	i.epoch++
	return InventoryObservation{Epoch: i.epoch, ObservedAt: time.Now().UTC(), Backends: append([]BackendIdentity(nil), i.backends[token]...)}, nil
}
func (i *fakeInventory) Terminate(_ context.Context, backend BackendIdentity) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	values := i.backends[backend.DialToken]
	kept := values[:0]
	for _, v := range values {
		if !v.Equal(backend) {
			kept = append(kept, v)
		}
	}
	i.backends[backend.DialToken] = kept
	return nil
}
