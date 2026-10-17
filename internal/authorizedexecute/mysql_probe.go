package authorizedexecute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

// ProbeMySQLSingleTableB2 checks both dedicated accounts and proves they
// address the same supported MySQL server. It does not enroll any policy.
func (gateway *Gateway) ProbeMySQLSingleTableB2(ctx context.Context, datasource model.Datasource,
	secret []byte, metadataUsername, metadataPassword string,
) (string, error) {
	if gateway == nil || gateway.manager == nil || ctx == nil || datasource.DBType != "mysql" ||
		metadataUsername == "" || metadataUsername == datasource.Username {
		return "", &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	opened, err := gateway.manager.OpenFreshReadOnly(datasource, secret)
	if err != nil {
		return "", fixedExecutionError(err)
	}
	defer opened.Close()
	mysql, ok := opened.(*businessdb.MySQLExecutor)
	if !ok {
		return "", &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	inspector, err := businessdb.NewMySQLColumnIdentityInspector(ctx, datasource, metadataUsername, metadataPassword)
	if err != nil {
		return "", fixedExecutionError(err)
	}
	defer inspector.Close()
	uuid, version, err := mysql.ProbeMySQLColumnBusiness(ctx)
	if err != nil {
		return "", StableError(err)
	}
	identity, err := inspector.ProbeServer(ctx)
	if err != nil {
		return "", StableError(err)
	}
	if uuid == "" || uuid != identity.ServerUUID || version != identity.ServerVersion {
		return "", &AuthError{Reason: ReasonBinderIncomplete}
	}
	hash := sha256.Sum256([]byte(strings.Join([]string{datasource.ID, uuid, version,
		"mysql-8-single-table-b2-v1"}, "\x00")))
	return hex.EncodeToString(hash[:]), nil
}
