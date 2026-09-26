package businessdb

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEasyDeploySelectorRulesAndNativeFallback(t *testing.T) {
	t.Parallel()
	handshake := healthySelectorHandshake(t, 16, 42)
	health := NewNativeHealthRegistry()
	selector := NewBinderModeSelector(health)
	base := BinderSelectionRequest{DatasourceIdentity: "ds", RequestDigest: "sha256:req", StatementClass: BinderStatementSelect,
		ClosedDisposition: ClosedRequestProven, ClosedShapeID: "direct", Provider: postgresProviderSelfManaged, Handshake: handshake}

	decision, err := selector.Select(base)
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
	require.Equal(t, ModeSelectionClosedDefault, decision.Reason)

	native := base
	native.ClosedDisposition = ClosedRequestNativeRequired
	decision, err = selector.Select(native)
	require.NoError(t, err)
	require.Equal(t, BinderModeNativeCV1, decision.Mode)
	require.Equal(t, ModeSelectionNativeRequired, decision.Reason)

	managed := native
	managed.Provider = postgresProviderAWSManaged
	decision, err = selector.Select(managed)
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
	require.Equal(t, ModeSelectionManagedService, decision.Reason)

	absent := native
	absent.DatasourceIdentity = "without-extension"
	absent.Handshake.Native = CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion, Mode: BinderModeNativeCV1}
	absent.Handshake.NativeFilesAvailable = false
	absent.Handshake.NativeInstalled = false
	absent.Handshake.NativeHealth = BinderCodeModeRequired
	decision, err = selector.Select(absent)
	require.NoError(t, err, "native absence must not make selection fail")
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
	require.Equal(t, ModeSelectionNativeAbsent, decision.Reason)

	mismatch := native
	mismatch.DatasourceIdentity = "hash-mismatch"
	mismatch.Handshake.Native.BuildHash = "tampered"
	mismatch.Handshake.Native.Digest, _ = mismatch.Handshake.Native.CanonicalDigest()
	mismatch.Handshake.NativeHealth = "AUTH_BINDER_CAPABILITY_MISMATCH"
	decision, err = selector.Select(mismatch)
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
	require.Equal(t, ModeSelectionNativeMismatch, decision.Reason)

	rejected := base
	rejected.ClosedDisposition = ClosedRequestMustReject
	decision, err = selector.Select(rejected)
	require.Error(t, err)
	require.True(t, decision.Rejected)
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
}

func TestEasyDeploySelectorMatviewRequiresHealthyNative(t *testing.T) {
	t.Parallel()
	handshake := healthySelectorHandshake(t, 16, 42)
	selector := NewBinderModeSelector(NewNativeHealthRegistry())
	request := BinderSelectionRequest{DatasourceIdentity: "matview-ds", RequestDigest: "sha256:matview",
		StatementClass: BinderStatementSelect, ClosedDisposition: ClosedRequestProven, RequiresMatview: true,
		Provider: postgresProviderSelfManaged, Handshake: handshake}
	decision, err := selector.Select(request)
	require.NoError(t, err)
	require.Equal(t, BinderModeNativeCV1, decision.Mode)
	require.Equal(t, ClosedRequestNativeRequired, decision.ClosedDisposition)

	request.DatasourceIdentity = "matview-without-native"
	request.Handshake.NativeFilesAvailable = false
	request.Handshake.NativeInstalled = false
	request.Handshake.NativeHealth = BinderCodeModeRequired
	request.Handshake.Native = CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion, Mode: BinderModeNativeCV1}
	decision, err = selector.Select(request)
	requireAuthorizationReason(t, err, "AUTH_RELATION_SHAPE_UNSUPPORTED")
	require.True(t, decision.Rejected)
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
}

func TestShadowDivergenceMarksNativeUnhealthyAndSelectorFallsBack(t *testing.T) {
	t.Parallel()
	handshake := healthySelectorHandshake(t, 16, 42)
	health := NewNativeHealthRegistry()
	selector := NewBinderModeSelector(health)
	nativeRequest := BinderSelectionRequest{DatasourceIdentity: "ds", RequestDigest: "sha256:req-2", StatementClass: BinderStatementSelect,
		ClosedDisposition: ClosedRequestNativeRequired, Provider: postgresProviderSelfManaged, Handshake: handshake}
	decision, err := selector.Select(nativeRequest)
	require.NoError(t, err)
	require.Equal(t, BinderModeNativeCV1, decision.Mode)

	var mu sync.Mutex
	var events []ShadowDifferentialEvent
	metrics := NewInMemoryShadowMetrics()
	runner := NewShadowDifferentialRunner(health, ShadowDifferentialSinkFunc(func(_ context.Context, event ShadowDifferentialEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}), metrics)
	closedFacts := selectorTestFacts("id")
	nativeFacts := selectorTestFacts("renamed")
	result, err := runner.Run(context.Background(), ShadowDifferentialInput{DatasourceIdentity: "ds", RequestDigest: "sha256:req-2",
		StatementClass: BinderStatementSelect, ClosedDisposition: ClosedRequestProven, NativeCapabilityDigest: handshake.Native.Digest},
		BindRequest{RawSQL: "redacted-at-event-boundary"}, fixedShadowBind(BinderModeCatalogClosedV1, closedFacts, nil),
		fixedShadowBind(BinderModeNativeCV1, nativeFacts, nil))
	require.Error(t, err)
	var binderErr *BinderError
	require.True(t, errors.As(err, &binderErr))
	require.Equal(t, BinderFailureDivergence, binderErr.Class)
	require.Equal(t, ShadowDivergent, result.Status)
	require.True(t, result.NativeMarkedUnhealthy)
	require.Contains(t, result.DifferingFields, "ColumnUses[0].Name")
	require.Len(t, events, 1)
	require.True(t, events[0].HealthTransition)
	require.NotContains(t, string(mustJSON(t, events[0])), "redacted-at-event-boundary")
	require.Equal(t, uint64(1), metrics.Snapshot().Divergence)

	decision, err = selector.Select(nativeRequest)
	require.NoError(t, err)
	require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
	require.Equal(t, ModeSelectionNativeUnhealthy, decision.Reason)

	_, err = runner.Run(context.Background(), ShadowDifferentialInput{DatasourceIdentity: "ds", RequestDigest: "sha256:req-3",
		StatementClass: BinderStatementSelect, ClosedDisposition: ClosedRequestProven, NativeCapabilityDigest: handshake.Native.Digest},
		BindRequest{}, fixedShadowBind(BinderModeCatalogClosedV1, closedFacts, nil), fixedShadowBind(BinderModeNativeCV1, nativeFacts, nil))
	require.Error(t, err)
	mu.Lock()
	require.False(t, events[1].HealthTransition, "repeated divergence must be idempotent")
	mu.Unlock()
}

func TestShadowConsistentNativeOnlyRejectedAndFieldOrdering(t *testing.T) {
	t.Parallel()
	health := NewNativeHealthRegistry()
	metrics := NewInMemoryShadowMetrics()
	runner := NewShadowDifferentialRunner(health, ShadowDifferentialSinkFunc(func(context.Context, ShadowDifferentialEvent) error { return nil }), metrics)
	facts := selectorTestFacts("id")
	reordered := facts
	reordered.ColumnUses = []SemanticColumnUse{facts.ColumnUses[1], facts.ColumnUses[0]}
	result, err := runner.Run(context.Background(), ShadowDifferentialInput{DatasourceIdentity: "ds", RequestDigest: "sha256:a",
		ClosedDisposition: ClosedRequestProven}, BindRequest{}, fixedShadowBind(BinderModeCatalogClosedV1, facts, nil), fixedShadowBind(BinderModeNativeCV1, reordered, nil))
	require.NoError(t, err)
	require.Equal(t, ShadowConsistent, result.Status)

	modeRequired := NewPrecisionFailure(BinderCodeModeRequired)
	result, err = runner.Run(context.Background(), ShadowDifferentialInput{DatasourceIdentity: "ds", RequestDigest: "sha256:b",
		ClosedDisposition: ClosedRequestNativeRequired}, BindRequest{}, fixedShadowBind(BinderModeCatalogClosedV1, SemanticFacts{}, modeRequired), fixedShadowBind(BinderModeNativeCV1, facts, nil))
	require.NoError(t, err)
	require.Equal(t, ShadowNativeOnly, result.Status)

	result, err = runner.Run(context.Background(), ShadowDifferentialInput{DatasourceIdentity: "ds", RequestDigest: "sha256:c",
		ClosedDisposition: ClosedRequestMustReject}, BindRequest{}, fixedShadowBind(BinderModeCatalogClosedV1, SemanticFacts{}, modeRequired), fixedShadowBind(BinderModeNativeCV1, SemanticFacts{}, modeRequired))
	require.NoError(t, err)
	require.Equal(t, ShadowRejected, result.Status)

	shapeUnsupported := NewPrecisionFailure("AUTH_RELATION_SHAPE_UNSUPPORTED")
	result, err = runner.Run(context.Background(), ShadowDifferentialInput{DatasourceIdentity: "matview-ds", RequestDigest: "sha256:matview",
		StatementClass: BinderStatementSelect, ClosedDisposition: ClosedRequestNativeRequired}, BindRequest{},
		fixedShadowBind(BinderModeCatalogClosedV1, SemanticFacts{}, shapeUnsupported), fixedShadowBind(BinderModeNativeCV1, facts, nil))
	require.NoError(t, err)
	require.Equal(t, ShadowNativeOnly, result.Status, "closed matview rejection and native proof are an expected capability layer")
	require.Equal(t, "AUTH_RELATION_SHAPE_UNSUPPORTED", result.ClosedReason)
}

func TestClassifyEasyDeployClosedSyntaxEscapeCorpus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sql         string
		class       BinderStatementClass
		disposition ClosedRequestDisposition
	}{
		{`SELECT a.id FROM app.accounts a WHERE a.id=1`, BinderStatementSelect, ClosedRequestProven},
		{`SELECT pg_catalog.count(a.id) FROM app.accounts a`, BinderStatementSelect, ClosedRequestNativeRequired},
		{`UPDATE app.accounts SET name='x' WHERE id=1`, BinderStatementUpdate, ClosedRequestProven},
		{`DELETE FROM app.accounts WHERE id=1; DROP TABLE app.accounts`, "", ClosedRequestMustReject},
		{`COPY app.accounts TO PROGRAM 'id'`, "", ClosedRequestMustReject},
		{`PREPARE x AS SELECT 1`, "", ClosedRequestMustReject},
		{`SELECT a.id FROM app.accounts a /* ; */; SELECT 1`, "", ClosedRequestMustReject},
	}
	for _, test := range tests {
		class, disposition, _ := ClassifyEasyDeployClosedSyntax(test.sql)
		require.Equal(t, test.class, class, test.sql)
		require.Equal(t, test.disposition, disposition, test.sql)
	}
}

func healthySelectorHandshake(t testing.TB, major int, databaseOID uint32) BinderCapabilityHandshake {
	t.Helper()
	expected, ok := PostgresBinderNativeExpectation(major)
	require.True(t, ok)
	capability := PostgresBinderCapability{ABI: expected.ABI, ServerMajor: major, ExtensionVersion: expected.ExtensionVersion,
		BuildHash: expected.BuildHash, ExtensionHash: expected.ExtensionHash, NodeManifestHash: expected.NodeManifestHash,
		AllowlistHash: expected.AllowlistHash, Matview: true}
	native, err := nativeCapabilityAttestation(capability, databaseOID, major*10000+1)
	require.NoError(t, err)
	return BinderCapabilityHandshake{SelectedMode: BinderModeNativeCV1, Closed: closedCapability(major*10000+1, databaseOID),
		Native: native, NativeHealth: BinderNativeHealthHealthy, NativeFilesAvailable: true, NativeInstalled: true}
}

func selectorTestFacts(columnName string) SemanticFacts {
	facts := SemanticFacts{Schema: SemanticFactsSchemaID, SchemaVersion: SemanticFactsVersion, StatementClass: BinderStatementSelect,
		Relations: []SemanticRelation{{DatasourceID: "ds", DatabaseOID: 42, RelationOID: 100, NamespaceOID: 99, Schema: "app", Name: "accounts", Kind: 'r', Persistence: 'p', CatalogFingerprint: "catalog"}},
		Identity:  SemanticIdentity{DatasourceIdentity: "ds", DatabaseOID: 42, CurrentUser: "agent", RoleOID: 10, FixedSearchPath: "pg_catalog", CatalogDigest: "catalog"}}
	facts.ColumnUses = []SemanticColumnUse{
		{RelationOID: 100, Attnum: 1, Name: columnName, TypeOID: 23, Usage: SemanticUsageOutput, Site: "target.1", OutputIndex: 0},
		{RelationOID: 100, Attnum: 1, Name: columnName, TypeOID: 23, Usage: SemanticUsageReference, Site: "join_where", OutputIndex: -1},
	}
	return facts
}

func fixedShadowBind(mode BinderMode, facts SemanticFacts, err error) ShadowBindFunc {
	return func(context.Context, BindRequest) (BoundProgram, error) {
		if err != nil {
			return BoundProgram{}, err
		}
		digest, digestErr := facts.Digest()
		if digestErr != nil {
			return BoundProgram{}, digestErr
		}
		return BoundProgram{Mode: mode, Facts: facts, SemanticFactsDigest: digest}, nil
	}
}

func mustJSON(t testing.TB, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
