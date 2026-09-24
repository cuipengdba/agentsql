package authorizedexecute

import (
	"context"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

// Gateway owns business credentials and pools without exporting a raw SQL,
// connection, session, transaction, driver or database/sql handle.
type Gateway struct {
	manager      *businessdb.Manager
	reservations *ReservationPool
	readOnly     bool
}

type TableRef struct{ Schema, Table string }
type ColumnRef struct{ Schema, Table, Column string }
type SchemaColumn struct {
	Schema, Table, Column, DataType string
	Ordinal                         int
}

func NewGateway(readOnly bool) *Gateway {
	return &Gateway{manager: businessdb.NewManager(readOnly), reservations: NewReservationPool(DefaultReservationLimits), readOnly: readOnly}
}

// ProbeDatasource performs only connect, ping and close for CLI enrollment.
// It never returns the opened pool or any SQL-capable object.
func ProbeDatasource(ctx context.Context, datasource model.Datasource, password string) error {
	var opened businessdb.Executor
	var err error
	switch datasource.DBType {
	case "postgres":
		opened, err = businessdb.NewPostgresExecutor(ctx, datasource, password, true)
	case "mysql":
		opened, err = businessdb.NewMySQLExecutor(ctx, datasource, password, true)
	default:
		return &AuthError{Reason: ReasonDatabaseFailure}
	}
	if err != nil {
		return fixedExecutionError(err)
	}
	if closeErr := opened.Close(); closeErr != nil {
		return fixedExecutionError(closeErr)
	}
	return nil
}

func (gateway *Gateway) open(datasource model.Datasource, secret []byte) (businessdb.Executor, error) {
	if gateway == nil || gateway.manager == nil {
		return nil, fmt.Errorf("authorized datasource gateway is unavailable")
	}
	return gateway.manager.GetOrOpen(datasource, secret)
}

func (gateway *Gateway) Ping(ctx context.Context, datasource model.Datasource, secret []byte) error {
	executor, err := gateway.open(datasource, secret)
	if err != nil {
		return err
	}
	if err := executor.Ping(ctx); err != nil {
		return fixedExecutionError(err)
	}
	return nil
}

func (gateway *Gateway) ListSchema(ctx context.Context, datasource model.Datasource, secret []byte, tables []TableRef) ([]SchemaColumn, error) {
	executor, err := gateway.open(datasource, secret)
	if err != nil {
		return nil, fixedExecutionError(err)
	}
	requested := make([]businessdb.SchemaTable, len(tables))
	for index, table := range tables {
		requested[index] = businessdb.SchemaTable{Schema: table.Schema, Table: table.Table}
	}
	columns, err := businessdb.ListSchema(ctx, executor, datasource.Database, requested)
	if err != nil {
		return nil, fixedExecutionError(err)
	}
	result := make([]SchemaColumn, len(columns))
	for index, column := range columns {
		result[index] = SchemaColumn{Schema: column.Schema, Table: column.Table, Column: column.Column, DataType: column.DataType, Ordinal: column.Ordinal}
	}
	return result, nil
}

func (gateway *Gateway) Sample(ctx context.Context, datasource model.Datasource, secret []byte, table TableRef, columns []ColumnRef, limit int) (model.QueryResult, error) {
	executor, err := gateway.open(datasource, secret)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	requested := make([]businessdb.SampleColumn, len(columns))
	for index, column := range columns {
		requested[index] = businessdb.SampleColumn{Schema: column.Schema, Table: column.Table, Column: column.Column}
	}
	result, err := businessdb.Sample(ctx, executor, businessdb.SchemaTable{Schema: table.Schema, Table: table.Table}, requested, limit)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	if err := ValidateResult(result, false, DefaultLimits); err != nil {
		return model.QueryResult{}, err
	}
	return result, nil
}

func (gateway *Gateway) SnapshotPools() []PoolStat {
	if gateway == nil || gateway.manager == nil {
		return nil
	}
	return gateway.manager.SnapshotPools()
}
func (gateway *Gateway) Close(id string) error {
	if gateway == nil || gateway.manager == nil {
		return nil
	}
	return gateway.manager.Close(id)
}
func (gateway *Gateway) CloseAll() error {
	if gateway == nil || gateway.manager == nil {
		return nil
	}
	return gateway.manager.CloseAll()
}
