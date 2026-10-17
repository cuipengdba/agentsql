package authorizedexecute

import (
	"context"
	"errors"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

type MySQLColumnCatalog = businessdb.MySQLBoundCatalog
type MySQLCatalogColumn = businessdb.MySQLBoundColumn

// MySQLCatalogLease offers only fixed catalog facts, never SQL execution.
type MySQLCatalogLease struct {
	opened    businessdb.Executor
	inspector *businessdb.MySQLColumnIdentityInspector
	bound     *businessdb.MySQLColumnCatalogLease
}

func (gateway *Gateway) BindMySQLColumnCatalog(ctx context.Context, datasource model.Datasource,
	secret []byte, metadataUsername, metadataPassword, schema, table string,
) (_ *MySQLCatalogLease, returnedErr error) {
	if gateway == nil || gateway.manager == nil || ctx == nil || datasource.DBType != "mysql" ||
		metadataUsername == "" || metadataUsername == datasource.Username {
		return nil, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	opened, err := gateway.manager.OpenFreshReadOnly(datasource, secret)
	if err != nil {
		return nil, fixedExecutionError(err)
	}
	lease := &MySQLCatalogLease{opened: opened}
	defer func() {
		if returnedErr != nil {
			_ = lease.Close()
		}
	}()
	mysql, ok := opened.(*businessdb.MySQLExecutor)
	if !ok {
		return nil, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	lease.inspector, err = businessdb.NewMySQLColumnIdentityInspector(ctx, datasource, metadataUsername, metadataPassword)
	if err != nil {
		return nil, fixedExecutionError(err)
	}
	lease.bound, err = mysql.BindMySQLColumnCatalog(ctx, schema, table, lease.inspector)
	if err != nil {
		return nil, fixedExecutionError(err)
	}
	return lease, nil
}

func (lease *MySQLCatalogLease) Catalog() MySQLColumnCatalog {
	if lease == nil || lease.bound == nil {
		return MySQLColumnCatalog{}
	}
	return lease.bound.Catalog()
}

func (lease *MySQLCatalogLease) Close() error {
	if lease == nil {
		return nil
	}
	var errs []error
	if lease.bound != nil {
		errs = append(errs, lease.bound.Close())
		lease.bound = nil
	}
	if lease.inspector != nil {
		errs = append(errs, lease.inspector.Close())
		lease.inspector = nil
	}
	if lease.opened != nil {
		errs = append(errs, lease.opened.Close())
		lease.opened = nil
	}
	return errors.Join(errs...)
}
