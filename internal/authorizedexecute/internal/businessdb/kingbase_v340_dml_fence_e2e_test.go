package businessdb

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// TestKingbaseV9DMLFenceRealE2E verifies the vendor boundary on the pinned
// V9R1C10 PG-mode instance. B2 SELECT is tested by the closed-only matrix;
// this test keeps the incompatible B5/DML paths closed at their entry points.
func TestKingbaseV9DMLFenceRealE2E(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_KINGBASE_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_KINGBASE_HOST for the isolated V9R1C10 instance")
	}
	password := os.Getenv("AGENTSQL_TEST_KINGBASE_PASSWORD")
	require.NotEmpty(t, password)
	port := 54321
	if raw := os.Getenv("AGENTSQL_TEST_KINGBASE_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executor, err := NewPostgresExecutor(ctx, model.Datasource{
		ID: "kingbase-v340-dml-fence", DBType: "postgres", Host: host, Port: port,
		Database: "kingbase", Username: "agentsql_v340_b2", ConnLimit: 2,
		StmtTimeoutMS: 5_000, RowLimit: 10,
	}, password, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	require.True(t, executor.restrictedVendorDML)
	session, err := executor.OpenSession(ctx, "v340-write")
	require.Nil(t, session)
	require.ErrorIs(t, err, ErrReadOnlyViolated)
	tx, err := executor.BeginWriteTx(ctx)
	require.Nil(t, tx)
	require.ErrorIs(t, err, ErrReadOnlyViolated)
	_, err = executor.Execute(ctx, "UPDATE public.agentsql_batch72_verify SET name='forbidden' WHERE id=1")
	require.ErrorIs(t, err, ErrReadOnlyViolated)
	_, err = executor.BindClosedDML(ctx, BindRequest{RawSQL: "UPDATE public.agentsql_batch72_verify SET name='forbidden' WHERE id=1",
		Identity: SemanticIdentity{DatasourceIdentity: "kingbase-v340-dml-fence"}}, &unlimitedPostgresBudget{})
	require.ErrorIs(t, err, ErrReadOnlyViolated)
	_, err = executor.EnrollPostgresDML(ctx, "UPDATE public.agentsql_batch72_verify SET name='forbidden' WHERE id=1",
		"kingbase-v340-dml-fence", &unlimitedPostgresBudget{})
	require.ErrorIs(t, err, ErrReadOnlyViolated)
}
