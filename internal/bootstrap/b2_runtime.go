package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/cuipengdba/agentsql/internal/version"
)

const (
	B2StateActive      = "active"
	B2StateDryRun      = "dry-run"
	B2StateFeatureOff  = "feature-off"
	B2StateDegraded    = "degraded"
	B2StateUnsupported = "unsupported"

	B2ReasonFeatureOff              = "B2_FEATURE_OFF"
	B2ReasonDryRunReady             = "B2_DRY_RUN_READY"
	B2ReasonNoPostgresDatasource    = "B2_POSTGRES_DATASOURCE_REQUIRED"
	B2ReasonMetadataUnavailable     = "B2_METADATA_UNAVAILABLE"
	B2ReasonEnrollmentIncomplete    = "B2_ENROLLMENT_INCOMPLETE"
	B2ReasonCatalogBindIncomplete   = "B2_CATALOG_BIND_INCOMPLETE"
	B2ReasonBinderUnsupported       = "B2_BINDER_ABI_UNSUPPORTED"
	B2ReasonBinderProbeFailed       = "B2_BINDER_PROBE_FAILED"
	B2ReasonReservationUnavailable  = "B2_RESERVATION_UNAVAILABLE"
	B2ReasonControlFenceUnavailable = "B2_CONTROL_FENCE_UNAVAILABLE"
	B2ReasonArtifactAttestation     = "B2_ARTIFACT_ATTESTATION_FAILED"
	B2ReasonHeartbeatFailed         = "B2_RUNTIME_HEARTBEAT_FAILED"
	B2ReasonRuntimeLeaseExpired     = "B2_RUNTIME_LEASE_EXPIRED"
	B2ReasonDatasourceUnsupported   = "B2_DATASOURCE_DIALECT_UNSUPPORTED"
)

// B2Status is the stable, non-secret readiness/health projection.
type B2Status struct {
	State                  string            `json:"state"`
	Reason                 string            `json:"reason"`
	Protocol               int               `json:"protocol"`
	InstanceID             string            `json:"instance_id,omitempty"`
	ArtifactDigest         string            `json:"artifact_digest,omitempty"`
	LeaseExpiresAt         *time.Time        `json:"lease_expires_at,omitempty"`
	UnsupportedDatasources map[string]string `json:"unsupported_datasources,omitempty"`
}

type b2Runtime struct {
	mu              sync.RWMutex
	status          B2Status
	fence           *store.FenceRepository
	instance        store.RuntimeInstance
	lease, interval time.Duration
	cancel          context.CancelFunc
	wait            sync.WaitGroup
	stateObserver   func(string)
}

func featureOffB2Status() B2Status {
	return B2Status{State: B2StateFeatureOff, Reason: B2ReasonFeatureOff, Protocol: 2}
}

func activateB2Runtime(ctx context.Context, metadata *store.Store, gateway *executor.Gateway, secret []byte, instanceID string, lease, interval time.Duration) (*b2Runtime, error) {
	return evaluateB2Runtime(ctx, metadata, gateway, secret, instanceID, lease, interval, true)
}

// dryRunB2Runtime executes the same probes and phase-one snapshot as
// activation, but never changes the control fence or registers a runtime.
func dryRunB2Runtime(ctx context.Context, metadata *store.Store, gateway *executor.Gateway, secret []byte, instanceID string, lease, interval time.Duration) (*b2Runtime, error) {
	return evaluateB2Runtime(ctx, metadata, gateway, secret, instanceID, lease, interval, false)
}

func evaluateB2Runtime(ctx context.Context, metadata *store.Store, gateway *executor.Gateway, secret []byte, instanceID string, lease, interval time.Duration, activate bool) (*b2Runtime, error) {
	manager := &b2Runtime{fence: metadata.Fence(), lease: lease, interval: interval}
	if lease <= 0 {
		lease = 15 * time.Second
		manager.lease = lease
	}
	if interval <= 0 || interval >= lease {
		interval = lease / 3
		manager.interval = interval
	}
	readiness, err := manager.fence.PrepareProtocol3Activation(ctx)
	if err != nil {
		manager.status = B2Status{State: B2StateDegraded, Reason: B2ReasonMetadataUnavailable, Protocol: 2}
		return manager, err
	}
	if !readiness.Ready() {
		reason := B2ReasonEnrollmentIncomplete
		if readiness.IncompleteBindings != 0 || readiness.UnhealthyBindings != 0 || readiness.OrphanedPermissions != 0 {
			reason = B2ReasonCatalogBindIncomplete
		}
		manager.status = B2Status{State: B2StateDegraded, Reason: reason, Protocol: 2}
		return manager, store.ErrActivationGate
	}
	datasources, err := metadata.Datasources().List(ctx)
	if err != nil {
		manager.status = B2Status{State: B2StateDegraded, Reason: B2ReasonMetadataUnavailable, Protocol: 2}
		return manager, err
	}
	unsupported := make(map[string]string)
	capabilityDigests := make([]string, 0, len(datasources))
	postgresCount := 0
	for _, datasource := range datasources {
		if datasource.DBType != "postgres" {
			unsupported[datasource.ID] = B2ReasonDatasourceUnsupported
			continue
		}
		postgresCount++
		capability, probeErr := gateway.ProbePostgresB2Capability(ctx, datasource, secret)
		if probeErr != nil {
			reason := B2ReasonBinderProbeFailed
			var authErr *executor.AuthError
			if errors.As(probeErr, &authErr) && (authErr.Reason == executor.ReasonBinderCapability || authErr.Reason == executor.ReasonDatasourceUnsupported) {
				reason = B2ReasonBinderUnsupported
			}
			manager.status = B2Status{State: B2StateUnsupported, Reason: reason, Protocol: 2, UnsupportedDatasources: unsupported}
			return manager, probeErr
		}
		capabilityDigests = append(capabilityDigests, strings.Join([]string{datasource.ID, capability.ABI,
			capability.ExtensionVersion, capability.ExtensionHash, capability.NodeManifestHash,
			capability.AllowlistHash}, "\x00"))
		if err := gateway.ProbeReservation(datasource.ID); err != nil {
			manager.status = B2Status{State: B2StateDegraded, Reason: B2ReasonReservationUnavailable, Protocol: 2, UnsupportedDatasources: unsupported}
			return manager, err
		}
	}
	if postgresCount == 0 {
		manager.status = B2Status{State: B2StateUnsupported, Reason: B2ReasonNoPostgresDatasource, Protocol: 2, UnsupportedDatasources: unsupported}
		return manager, store.ErrActivationGate
	}
	sort.Strings(capabilityDigests)
	artifact, err := b2ArtifactDigest(capabilityDigests)
	if err != nil {
		manager.status = B2Status{State: B2StateDegraded, Reason: B2ReasonArtifactAttestation, Protocol: 2, UnsupportedDatasources: unsupported}
		return manager, err
	}
	if !activate {
		manager.status = B2Status{State: B2StateDryRun, Reason: B2ReasonDryRunReady, Protocol: 2,
			InstanceID: instanceID, ArtifactDigest: artifact, UnsupportedDatasources: unsupported}
		return manager, nil
	}
	now := time.Now().UTC()
	instance, err := manager.fence.ActivateProtocol3(ctx, store.Protocol3Activation{
		InstanceID: instanceID, ArtifactDigest: artifact, ExpectedETag: readiness.ETag, BinderReady: true,
		CatalogReady: true, ReservationReady: true, Now: now, Lease: lease,
	})
	if err != nil {
		manager.status = B2Status{State: B2StateDegraded, Reason: B2ReasonControlFenceUnavailable, Protocol: 2, UnsupportedDatasources: unsupported}
		return manager, err
	}
	manager.instance = instance
	expires := instance.LeaseExpiresAt
	manager.status = B2Status{State: B2StateActive, Reason: "B2_READY", Protocol: 3,
		InstanceID: instance.InstanceID, ArtifactDigest: artifact, LeaseExpiresAt: &expires,
		UnsupportedDatasources: unsupported}
	heartbeatContext, cancel := context.WithCancel(ctx)
	manager.cancel = cancel
	manager.wait.Add(1)
	go manager.heartbeatLoop(heartbeatContext)
	return manager, nil
}

func b2ArtifactDigest(capabilityDigests []string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	binary, err := os.Open(executable)
	if err != nil {
		return "", err
	}
	defer binary.Close()
	hash := sha256.New()
	_, _ = hash.Write([]byte("agentsql-b2-protocol3-artifact-v2\x00" + version.Version + "\x00"))
	if _, err := io.Copy(hash, binary); err != nil {
		return "", err
	}
	for _, digest := range capabilityDigests {
		_, _ = hash.Write([]byte("\x00" + digest))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (runtime *b2Runtime) heartbeatLoop(ctx context.Context) {
	defer runtime.wait.Done()
	ticker := time.NewTicker(runtime.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			instance, err := runtime.fence.Heartbeat(ctx, runtime.instance, now.UTC(), runtime.lease)
			runtime.mu.Lock()
			if err != nil {
				runtime.status.State = B2StateDegraded
				runtime.status.Reason = B2ReasonHeartbeatFailed
				runtime.status.LeaseExpiresAt = nil
			} else {
				runtime.instance = instance
				expires := instance.LeaseExpiresAt
				runtime.status.State = B2StateActive
				runtime.status.Reason = "B2_READY"
				runtime.status.LeaseExpiresAt = &expires
			}
			state, observer := runtime.status.State, runtime.stateObserver
			runtime.mu.Unlock()
			if observer != nil {
				observer(state)
			}
		}
	}
}

func (runtime *b2Runtime) attachStateObserver(observer func(string)) {
	if runtime == nil || observer == nil {
		return
	}
	runtime.mu.Lock()
	runtime.stateObserver = observer
	state := runtime.status.State
	runtime.mu.Unlock()
	observer(state)
}

func (runtime *b2Runtime) snapshot() B2Status {
	if runtime == nil {
		return featureOffB2Status()
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	result := runtime.status
	if runtime.status.LeaseExpiresAt != nil {
		value := *runtime.status.LeaseExpiresAt
		result.LeaseExpiresAt = &value
	}
	if result.Protocol == 3 && result.State == B2StateActive &&
		(result.LeaseExpiresAt == nil || !result.LeaseExpiresAt.After(time.Now())) {
		result.State = B2StateDegraded
		result.Reason = B2ReasonRuntimeLeaseExpired
	}
	result.UnsupportedDatasources = cloneStringMap(runtime.status.UnsupportedDatasources)
	return result
}

func (runtime *b2Runtime) allow(datasource model.Datasource) bool {
	status := runtime.snapshot()
	if status.State != B2StateActive || status.Protocol != 3 || datasource.DBType != "postgres" {
		return false
	}
	_, unsupported := status.UnsupportedDatasources[datasource.ID]
	return !unsupported
}

func (runtime *b2Runtime) route(datasource model.Datasource) bool {
	if runtime == nil || datasource.DBType != "postgres" {
		return false
	}
	status := runtime.snapshot()
	_, unsupported := status.UnsupportedDatasources[datasource.ID]
	return status.Protocol == 3 && !unsupported
}

func (runtime *b2Runtime) close() {
	if runtime == nil {
		return
	}
	if runtime.cancel != nil {
		runtime.cancel()
		runtime.wait.Wait()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = runtime.fence.DrainRuntime(ctx, runtime.instance.InstanceID)
	cancel()
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
