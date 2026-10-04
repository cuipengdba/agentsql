package authorizedexecute

import (
	"strconv"
	"testing"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/columnauth"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgresColumnAuthorizationInputConsumesModeNeutralFacts(t *testing.T) {
	facts := businessdb.SemanticFacts{
		Schema: businessdb.SemanticFactsSchemaID, SchemaVersion: businessdb.SemanticFactsVersion,
		StatementClass: businessdb.BinderStatementSelect,
		Identity: businessdb.SemanticIdentity{DatasourceIdentity: "pg-1", DatabaseOID: 10,
			CatalogDigest: "catalog-digest", PlanGeneration: 1},
		Relations: []businessdb.SemanticRelation{{DatasourceID: "pg-1", DatabaseOID: 10, RelationOID: 20,
			Schema: "app", Name: "customers", Kind: 'r', Persistence: 'p', CatalogFingerprint: "catalog-digest",
			BindingAlias: "customers"}},
		ColumnUses: []businessdb.SemanticColumnUse{{RelationOID: 20, Attnum: 2, Name: "phone", TypeOID: 25,
			TypeModifier: -1, Usage: businessdb.SemanticUsageOutput, Site: "target.1", OutputIndex: 0,
			BindingAlias: "customers"}},
	}
	request := authorizedSelectRequest{ColumnAuthorizationRequest: ColumnAuthorizationRequest{
		Agent: model.Agent{ID: "agent-1", Status: "active"}, PreliminaryAllowed: true,
		ControlRevisionDigest: "control-digest",
	}, Datasource: model.Datasource{ID: "pg-1", DBType: "postgres"}}

	input, masks, err := postgresColumnAuthorizationInputFromFacts(request, facts, []byte("nonce"))

	require.NoError(t, err)
	require.Equal(t, "catalog-digest", input.CatalogDigest)
	require.NotEmpty(t, input.BinderDigest)
	require.Len(t, input.Relations, 1)
	require.Equal(t, "pg:10:20", input.Relations[0].StableObjectID)
	require.Len(t, input.Uses, 1)
	require.Equal(t, columnauth.UsageOutput, input.Uses[0].Usage)
	require.Equal(t, 0, input.Uses[0].OutputIndex)
	require.Len(t, masks, 1)
	require.Equal(t, "app", masks[0].Sources[0].Schema)
	require.Equal(t, "customers", masks[0].Sources[0].Table)
	require.Equal(t, "phone", masks[0].Sources[0].Column)
}

func TestPostgresColumnAuthorizationInputRejectsUnprovableFacts(t *testing.T) {
	facts := businessdb.SemanticFacts{Schema: businessdb.SemanticFactsSchemaID,
		SchemaVersion: businessdb.SemanticFactsVersion, StatementClass: businessdb.BinderStatementSelect,
		Identity:   businessdb.SemanticIdentity{CatalogDigest: "catalog-digest"},
		Relations:  []businessdb.SemanticRelation{{DatabaseOID: 1, RelationOID: 2, Schema: "app", Name: "customers"}},
		ColumnUses: []businessdb.SemanticColumnUse{{RelationOID: 2, Attnum: 0, WholeRow: true}},
	}

	_, _, err := postgresColumnAuthorizationInputFromFacts(authorizedSelectRequest{}, facts, nil)
	require.Error(t, err)
	require.Equal(t, ReasonColumnShape, StableError(err).Reason)
}

func TestClosedHandshakeDoesNotRequireNativeMajor(t *testing.T) {
	t.Parallel()
	for _, serverVersion := range []int{90204, 100000, 120007, 150019, 180003} {
		serverVersion := serverVersion
		t.Run(strconv.Itoa(serverVersion), func(t *testing.T) {
			t.Parallel()
			handshake := businessdb.BinderCapabilityHandshake{
				SelectedMode: businessdb.BinderModeCatalogClosedV1,
				Closed: businessdb.CapabilityAttestation{
					Mode:             businessdb.BinderModeCatalogClosedV1,
					Available:        true,
					Digest:           "closed-digest",
					ServerVersionNum: serverVersion,
					ServerMajor:      serverVersion / 10000,
					DatabaseOID:      42,
				},
			}
			require.NoError(t, validatePostgresClosedHandshake(handshake))
		})
	}
}

func TestClosedHandshakeRejectsUnboundVersionIdentity(t *testing.T) {
	t.Parallel()
	handshake := businessdb.BinderCapabilityHandshake{
		SelectedMode: businessdb.BinderModeCatalogClosedV1,
		Closed: businessdb.CapabilityAttestation{
			Mode: businessdb.BinderModeCatalogClosedV1, Available: true, Digest: "closed-digest",
			ServerVersionNum: 120007, ServerMajor: 14, DatabaseOID: 42,
		},
	}
	err := validatePostgresClosedHandshake(handshake)
	require.Error(t, err)
	require.Equal(t, ReasonBinderCapability, StableError(err).Reason)
}

func TestClosedHandshakeRejectsInconsistentNativeSelection(t *testing.T) {
	t.Parallel()
	handshake := businessdb.BinderCapabilityHandshake{
		SelectedMode: businessdb.BinderModeNativeCV1,
		Closed: businessdb.CapabilityAttestation{
			Mode: businessdb.BinderModeCatalogClosedV1, Available: true, Digest: "closed-digest",
			ServerVersionNum: 160005, ServerMajor: 16, DatabaseOID: 42,
		},
		Native: businessdb.CapabilityAttestation{
			Mode: businessdb.BinderModeNativeCV1, Available: true, Digest: "native-digest",
			ServerVersionNum: 160005, ServerMajor: 16, DatabaseOID: 43,
		},
		NativeHealth: "healthy",
	}
	err := validatePostgresClosedHandshake(handshake)
	require.Error(t, err)
	require.Equal(t, ReasonBinderCapability, StableError(err).Reason)
}
