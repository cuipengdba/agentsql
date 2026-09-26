package authorizedexecute

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

// Gateway owns business credentials and pools without exporting a raw SQL,
// connection, session, transaction, driver or database/sql handle.
type Gateway struct {
	manager      *businessdb.Manager
	reservations *ReservationPool
	readOnly     bool
	column       ColumnAuthorizationProvider
}

// ColumnAuthorizationProvider is used by fixed internal SELECT producers
// (currently controlledread sampling) that cannot attach an agent pipeline
// snapshot themselves. It returns request facts plus the control-snapshot
// close barrier that must run after seal and before any result is returned.
type ColumnAuthorizationProvider interface {
	BeginColumnAuthorization(context.Context, string, model.Datasource) (ColumnAuthorizationRequest, func() error, error)
}

type columnAuthorizationRouter interface {
	ColumnAuthorizationEnabled(model.Datasource) bool
}

type GatewayOption func(*Gateway)

func WithColumnAuthorizationProvider(provider ColumnAuthorizationProvider) GatewayOption {
	return func(gateway *Gateway) {
		if provider != nil {
			gateway.column = provider
		}
	}
}

type TableRef struct{ Schema, Table string }
type ColumnRef struct{ Schema, Table, Column string }
type SchemaColumn struct {
	Schema, Table, Column, DataType string
	Ordinal                         int
}

// PostgresB2Capability is a non-executable startup attestation. It contains no
// SQL handle, credential, pool, or mutable binder object.
type PostgresB2Capability struct {
	ServerMajor                                                           int
	ABI, ExtensionVersion, ExtensionHash, NodeManifestHash, AllowlistHash string
	Matview                                                               bool
}

func NewGateway(readOnly bool, options ...GatewayOption) *Gateway {
	gateway := &Gateway{manager: businessdb.NewManager(readOnly), reservations: NewReservationPool(DefaultReservationLimits), readOnly: readOnly}
	for _, option := range options {
		if option != nil {
			option(gateway)
		}
	}
	return gateway
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

// ProbePostgresB2Capability validates the same extension ABI and PG14-18
// capability manifest that the locked SELECT path consumes.
func (gateway *Gateway) ProbePostgresB2Capability(ctx context.Context, datasource model.Datasource, secret []byte) (PostgresB2Capability, error) {
	if datasource.DBType != "postgres" {
		return PostgresB2Capability{}, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	opened, err := gateway.open(datasource, secret)
	if err != nil {
		return PostgresB2Capability{}, fixedExecutionError(err)
	}
	postgres, ok := opened.(*businessdb.PostgresExecutor)
	if !ok {
		return PostgresB2Capability{}, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	capability, err := postgres.ProbePostgresBinderCapability(ctx, NewBudget(DefaultLimits))
	if err != nil {
		return PostgresB2Capability{}, StableError(err)
	}
	return PostgresB2Capability{
		ServerMajor: capability.ServerMajor, ABI: capability.ABI,
		ExtensionVersion: capability.ExtensionVersion, ExtensionHash: capability.ExtensionHash,
		NodeManifestHash: capability.NodeManifestHash, AllowlistHash: capability.AllowlistHash,
		Matview: capability.Matview,
	}, nil
}

// ProbeReservation proves that the request-level concurrency and memory
// reservation primitive is live before protocol 3 can be activated.
func (gateway *Gateway) ProbeReservation(datasourceID string) error {
	if gateway == nil || gateway.reservations == nil || strings.TrimSpace(datasourceID) == "" {
		return &AuthError{Reason: ReasonConcurrencyLimit}
	}
	reservation, err := gateway.reservations.Reserve("b2-activation", "b2-activation", datasourceID, defaultStatementMemoryReservation())
	if err != nil {
		return err
	}
	reservation.Release()
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
	if ctx == nil || len(columns) == 0 || len(columns) > DefaultLimits.OutputColumns || limit <= 0 || limit > 1_000 {
		return model.QueryResult{}, &AuthError{Reason: ReasonRequestTooLarge}
	}
	tableName, err := quoteTypedIdentifier(datasource.DBType, table.Table)
	if err != nil {
		return model.QueryResult{}, &AuthError{Reason: ReasonExpressionShape}
	}
	if table.Schema != "" {
		schema, quoteErr := quoteTypedIdentifier(datasource.DBType, table.Schema)
		if quoteErr != nil {
			return model.QueryResult{}, &AuthError{Reason: ReasonExpressionShape}
		}
		tableName = schema + "." + tableName
	}
	projections := make([]string, len(columns))
	for index, column := range columns {
		if column.Schema != table.Schema || column.Table != table.Table {
			return model.QueryResult{}, &AuthError{Reason: ReasonExpressionShape}
		}
		projections[index], err = quoteTypedIdentifier(datasource.DBType, column.Column)
		if err != nil {
			return model.QueryResult{}, &AuthError{Reason: ReasonExpressionShape}
		}
	}
	sqlText := "SELECT " + strings.Join(projections, ",") + " FROM " + tableName + " LIMIT " + strconv.Itoa(limit)
	statement, err := gateway.AuthorizedExecute(ctx, datasource, secret, sqlText, "")
	if err != nil {
		return model.QueryResult{}, err
	}
	result, queryErr := statement.Query(ctx, limit)
	var columnErr error
	if column, ok := statement.(ColumnAuthorizedStatement); ok && queryErr == nil {
		outcome, available := column.ColumnAuthorizationResult()
		if !available {
			columnErr = &AuthError{Reason: ReasonAuthorizationProofInvalid}
		} else if !outcome.Allowed {
			columnErr = &AuthError{Reason: outcome.Reason}
		}
	}
	closeErr := statement.Close()
	if queryErr != nil || columnErr != nil || closeErr != nil {
		return model.QueryResult{}, errors.Join(queryErr, columnErr, closeErr)
	}
	if err := ValidateResult(result, false, DefaultLimits); err != nil {
		return model.QueryResult{}, err
	}
	return result, nil
}

func quoteTypedIdentifier(dialect, value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("invalid identifier")
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return "", fmt.Errorf("invalid identifier")
		}
	}
	switch dialect {
	case "postgres":
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`, nil
	case "mysql":
		return "`" + strings.ReplaceAll(value, "`", "``") + "`", nil
	default:
		return "", fmt.Errorf("unsupported dialect")
	}
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
