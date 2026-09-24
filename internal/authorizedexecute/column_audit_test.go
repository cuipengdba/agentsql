package authorizedexecute

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/columnauth"
	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestColumnAuditV2IsBoundedHashedAndDenyFirst(t *testing.T) {
	relation := columnauth.RelationIdentity{DatabaseID: "1", StableObjectID: "pg:1:2", Schema: "secret_schema", Name: "secret_table", CatalogFingerprint: "catalog"}
	input := columnauth.Input{
		Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Statement: model.StmtType("SELECT"),
		PreliminaryAllowed: true, DatasourceSupported: true, CatalogConsistent: true,
		BinderDigest: "binder", CatalogDigest: "catalog", ControlRevisionDigest: "control", Relations: []columnauth.RelationIdentity{relation},
		Nonce: []byte("0123456789abcdef0123456789abcdef"),
	}
	for index := 0; index < 400; index++ {
		input.Uses = append(input.Uses, columnauth.BoundColumnUse{Column: columnauth.ColumnIdentity{Relation: relation, Ordinal: index + 1, Name: "plaintext_secret_column", TypeDigest: strings.Repeat("x", 256)}, Usage: columnauth.UsageReference, Site: strings.Repeat("predicate", 128)})
	}
	plan := columnauth.Authorize(input)
	audit := buildColumnAuthorizationAudit(input, plan)
	encoded, err := json.Marshal(audit)
	require.NoError(t, err)
	require.Equal(t, 2, audit.Version)
	require.True(t, audit.Truncated)
	require.NotEmpty(t, audit.CompleteDigest)
	require.LessOrEqual(t, len(audit.Details), 256)
	require.LessOrEqual(t, len(encoded), 32<<10)
	require.NotContains(t, string(encoded), "plaintext_secret_column")
	require.NotContains(t, string(encoded), "secret_table")
}

func TestP0ENoBusinessToControlReverseEdge(t *testing.T) {
	ctx := lockrank.WithTracker(context.Background())
	business, err := lockrank.Acquire(ctx, lockrank.Business)
	require.NoError(t, err)
	require.ErrorIs(t, proveBusinessEnded(ctx), lockrank.ErrReverseOrder)
	business.Release()
	require.NoError(t, proveBusinessEnded(ctx))
}
