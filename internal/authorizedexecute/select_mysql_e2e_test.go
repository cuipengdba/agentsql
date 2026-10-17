package authorizedexecute

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMySQLColumnSelectS4RealServer(t *testing.T) {
	if os.Getenv("AGENTSQL_MYSQL_IDENTITY_E2E") != "1" {
		t.Skip("set AGENTSQL_MYSQL_IDENTITY_E2E=1 against the isolated MySQL fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := store.NewPasswordCipher(secret)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt("")
	require.NoError(t, err)
	datasource := model.Datasource{ID: "mysql-column-e2e", DBType: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "app", Username: "reader", PasswordEnc: encrypted, ConnLimit: 2,
		StmtTimeoutMS: 5000, RowLimit: 100}
	gateway := NewGateway(false)
	defer gateway.CloseAll()
	_, err = gateway.ProbeMySQLSingleTableB2(ctx, datasource, secret, "identity", "")
	require.NoError(t, err)
	lease, err := gateway.BindMySQLColumnCatalog(ctx, datasource, secret, "identity", "", "app", "customers")
	require.NoError(t, err)
	catalog := lease.Catalog()
	require.NoError(t, lease.Close())
	require.Positive(t, catalog.TableID)
	boundID := "mysql-binding"
	stable := "mysql:" + catalog.ServerUUID + ":" + strconv.FormatInt(catalog.TableID, 10)
	fingerprint := catalog.Fingerprint
	policy := model.Policy{ID: "mysql-policy", AgentID: "agent", DatasourceID: datasource.ID,
		ObjectType: "column", ObjectName: "app.customers", Action: "allow", Revision: 2,
		RelationBindingID: &boundID, RelationBinding: &model.RelationPolicyBinding{ID: boundID,
			PolicyID: "mysql-policy", DatasourceID: datasource.ID, SchemaName: "app", RelationName: "customers",
			StableObjectID: &stable, CatalogFingerprint: &fingerprint, Status: "healthy", Revision: 1}}
	for _, column := range catalog.Columns {
		if column.Name != "id" && column.Name != "phone" {
			continue
		}
		for _, usage := range []string{"output", "reference"} {
			policy.ColumnPermissions = append(policy.ColumnPermissions, model.PolicyColumnPermission{
				PolicyID: policy.ID, RelationEnrollmentID: boundID, ColumnOrdinal: column.Ordinal,
				ColumnName: column.Name, ColumnTypeDigest: column.TypeDigest, Usage: usage, ParentRevision: 2})
		}
	}
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "app", Table: "customers", Column: "phone",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	query := "SELECT id, phone FROM app.customers WHERE id = 1 ORDER BY customers.id LIMIT 2"
	audits := 0
	request := ColumnAuthorizationRequest{Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"},
		Policies: []model.Policy{policy}, Redactor: redactor, RowLimit: 2,
		PreliminaryAllowed: true, ControlRevisionDigest: "mysql-test-revision",
		MySQLMetadataUsername: "identity", MySQLMetadataPassword: "",
		DurableAudit: func(_ context.Context, audit ColumnAuthorizationAudit, _ *model.QueryResult, _ mask.RedactReport) error {
			audits++
			require.NotEmpty(t, audit.BinderDigest)
			return nil
		},
		FinalFence: func(context.Context) error { return nil },
	}
	allowed, err := executeColumnAuthorized(ctx, gateway, datasource, secret, query, request)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)
	require.Len(t, allowed.Result.Rows, 1)
	require.NotContains(t, allowed.Result.Rows[0][1], "13800005678")
	require.Equal(t, 1, audits)

	denied := request
	denied.Policies = []model.Policy{policy}
	denied.Policies[0].ColumnPermissions = append([]model.PolicyColumnPermission(nil), policy.ColumnPermissions...)
	filtered := denied.Policies[0].ColumnPermissions[:0]
	for _, permission := range denied.Policies[0].ColumnPermissions {
		if permission.ColumnName != "phone" || permission.Usage != "output" {
			filtered = append(filtered, permission)
		}
	}
	denied.Policies[0].ColumnPermissions = filtered
	blocked, err := executeColumnAuthorized(ctx, gateway, datasource, secret, query, denied)
	require.NoError(t, err)
	require.False(t, blocked.Allowed)
	require.Equal(t, ReasonColumnGrantMissing, blocked.Reason)
	require.Empty(t, blocked.Result.Rows)

	auditFailed := request
	auditFailed.DurableAudit = func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
		return errors.New("audit unavailable")
	}
	_, err = executeColumnAuthorized(ctx, gateway, datasource, secret, query, auditFailed)
	require.Error(t, err)
	fenceFailed := request
	fenceFailed.FinalFence = func(context.Context) error { return errors.New("fence lost") }
	_, err = executeColumnAuthorized(ctx, gateway, datasource, secret, query, fenceFailed)
	require.Error(t, err)
}
