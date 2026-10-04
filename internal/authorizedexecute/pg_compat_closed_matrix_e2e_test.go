package authorizedexecute_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const pgCompatClosedMatrixEnv = "AGENTSQL_PG_COMPAT_CLOSED_MATRIX"
const pgCompatClosedMatrixBase64Env = "AGENTSQL_PG_COMPAT_CLOSED_MATRIX_B64"

type pgCompatClosedTarget struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Database     string `json:"database"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	ServerMajor  int    `json:"server_major"`
	ExpectClosed bool   `json:"expect_closed"`
	ExpectReason string `json:"expect_reason,omitempty"`
}

// TestPGCompatibleClosedOnlyMatrix exercises real vendor kernels. It is
// environment-gated because the images and credentials are not redistributable
// CI fixtures. The JSON environment value is an array of pgCompatClosedTarget.
func TestPGCompatibleClosedOnlyMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("PG-compatible closed-only matrix requires real vendor databases")
	}
	raw := os.Getenv(pgCompatClosedMatrixEnv)
	if encoded := os.Getenv(pgCompatClosedMatrixBase64Env); raw == "" && encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err)
		raw = string(decoded)
	}
	if raw == "" {
		t.Skip(pgCompatClosedMatrixEnv + " or " + pgCompatClosedMatrixBase64Env + " is not set")
	}
	var targets []pgCompatClosedTarget
	require.NoError(t, json.Unmarshal([]byte(raw), &targets))
	require.NotEmpty(t, targets)

	for index, target := range targets {
		index := index
		target := target
		name := target.Name
		if name == "" {
			name = "target-" + strconv.Itoa(index)
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			t.Cleanup(cancel)
			require.NotEmpty(t, target.Host)
			require.Positive(t, target.Port)
			require.NotEmpty(t, target.Database)
			require.NotEmpty(t, target.Username)
			require.Positive(t, target.ServerMajor)

			secret := []byte("pg-compat-matrix-secret-32-bytes")
			cipher, err := store.NewPasswordCipher(secret)
			require.NoError(t, err)
			passwordEnc, err := cipher.Encrypt(target.Password)
			require.NoError(t, err)
			datasource := model.Datasource{
				ID: "pg-compat-" + strconv.Itoa(index), Name: name, DBType: "postgres",
				Host: target.Host, Port: target.Port, Database: target.Database, Username: target.Username,
				PasswordEnc: passwordEnc, ConnLimit: 2, StmtTimeoutMS: 10_000, RowLimit: 100,
			}
			gateway := executor.NewGateway(false)
			t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
			capability, probeErr := gateway.ProbePostgresB2Modes(ctx, datasource, secret)
			if !target.ExpectClosed {
				require.Error(t, probeErr)
				if target.ExpectReason != "" {
					require.Equal(t, target.ExpectReason, string(executor.StableError(probeErr).Reason))
				}
				return
			}

			require.NoError(t, probeErr)
			require.Equal(t, target.ServerMajor, capability.ServerMajor)
			require.Equal(t, string(businessdb.BinderModeCatalogClosedV1), capability.Mode)
			require.NotEmpty(t, capability.ClosedDigest)
			require.False(t, capability.NativeAvailable)
			require.Empty(t, capability.NativeDigest)
			require.Empty(t, capability.ABI)
			require.Empty(t, capability.ExtensionVersion)
			exercisePGCompatibleClosedAST(t, ctx, datasource, target.Password, index)
		})
	}
}

func exercisePGCompatibleClosedAST(t *testing.T, ctx context.Context, datasource model.Datasource, password string, index int) {
	t.Helper()
	connectionURL := url.URL{Scheme: "postgres", User: url.UserPassword(datasource.Username, password),
		Host: net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port)), Path: "/" + datasource.Database}
	setup, err := pgx.Connect(ctx, connectionURL.String())
	require.NoError(t, err)
	schema := "agentsql_closed_matrix_" + strconv.Itoa(index)
	_, err = setup.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = setup.Exec(cleanupContext, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = setup.Close(cleanupContext)
	})
	_, err = setup.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	_, err = setup.Exec(ctx, "CREATE TABLE "+schema+".closed_probe(id integer,label text)")
	require.NoError(t, err)
	_, err = setup.Exec(ctx, "INSERT INTO "+schema+".closed_probe VALUES (1,'closed-only')")
	require.NoError(t, err)

	binder, err := businessdb.NewPostgresExecutor(ctx, datasource, password, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, binder.Close()) })
	query := fmt.Sprintf("SELECT p.id,p.label FROM %s.closed_probe p WHERE p.id=1", schema)
	prepared, err := binder.BindClosedSelect(ctx, businessdb.BindRequest{RawSQL: query,
		Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID}}, executor.NewBudget(executor.DefaultLimits))
	require.NoError(t, err)
	require.Equal(t, businessdb.BinderModeCatalogClosedV1, prepared.Program().Mode)
	result, err := prepared.Execute(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1", "closed-only"}}, result.Rows)
	proof, err := prepared.VerifyPost(ctx, executor.NewBudget(executor.DefaultLimits))
	require.NoError(t, err)
	require.Equal(t, businessdb.BinderModeCatalogClosedV1, proof.Mode)
	require.NoError(t, prepared.Close(ctx))
}
