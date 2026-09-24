package authorizedexecute

import (
	"context"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
)

// Statement is a request-scoped capability for exactly one caller statement.
// It deliberately has no method that accepts SQL, credentials, a connection,
// or a transaction. S4 will add the private authorization proof consumed by
// this capability; S2 makes bypass through a raw database handle impossible.
type Statement interface {
	Dialect() string
	Explain(context.Context) (model.ExplainInfo, error)
	Query(context.Context, int) (model.QueryResult, error)
	Execute(context.Context) (model.QueryResult, error)
	ExecuteTransactional(context.Context, func(model.QueryResult) error) (model.QueryResult, error)
	TableHasIndex(string, string) (bool, error)
	TableRowCount(string, string) (int64, error)
	TransactionState() (rules.TransactionState, error)
	MysqlTransactionState() (rules.MysqlTransactionState, error)
	// ReleaseReservation releases request-scoped concurrency and buffer quota
	// without closing a caller session. Wrappers must forward this method so
	// successful, failed, and canceled requests cannot leak global accounting.
	ReleaseReservation()
	Close() error
}

var ErrCommitOutcomeUnknown = errors.New("authorized execution commit outcome unknown")

// AuthorizedExecute is the sole raw-statement ingress to the business database
// capability domain. The returned capability can only operate on this exact
// statement and cannot be repurposed as an arbitrary SQL sink.
func (gateway *Gateway) AuthorizedExecute(
	ctx context.Context,
	datasource model.Datasource,
	secret []byte,
	sqlText string,
	sessionID string,
) (Statement, error) {
	if err := ValidatePreParse(sqlText, nil, DefaultLimits); err != nil {
		return nil, err
	}
	databaseExecutor, err := gateway.open(datasource, secret)
	if err != nil {
		return nil, fixedExecutionError(err)
	}
	if databaseExecutor.Dialect() != datasource.DBType {
		return nil, &AuthError{Reason: ReasonDatabaseFailure}
	}
	scope := scopeFromContext(ctx)
	memory := defaultStatementMemoryReservation()
	reservation, err := gateway.reservations.Reserve(scope.agent, scope.tenant, datasource.ID, memory)
	if err != nil {
		return nil, err
	}
	statement := &boundStatement{sql: sqlText, executor: databaseExecutor, reservation: reservation}
	if sessionID != "" {
		statement.session, err = databaseExecutor.OpenSession(ctx, sessionID)
		if err != nil {
			reservation.Release()
			return nil, fixedExecutionError(err)
		}
		if statement.session == nil {
			reservation.Release()
			return nil, &AuthError{Reason: ReasonDatabaseFailure}
		}
	}
	return statement, nil
}

func defaultStatementMemoryReservation() int64 {
	return int64(DefaultLimits.RawResultBytes) +
		int64(DefaultLimits.MaskedResultBytes) +
		int64(DefaultLimits.EnvelopeBytes)
}

type boundStatement struct {
	sql         string
	executor    businessdb.Executor
	session     businessdb.Session
	reservation *Reservation
}

func (statement *boundStatement) Dialect() string { return statement.executor.Dialect() }

func (statement *boundStatement) Explain(ctx context.Context) (model.ExplainInfo, error) {
	if statement.session != nil {
		value, err := statement.session.Explain(ctx, statement.sql)
		return value, fixedExecutionError(err)
	}
	value, err := statement.executor.Explain(ctx, statement.sql)
	return value, fixedExecutionError(err)
}

func (statement *boundStatement) Query(ctx context.Context, rowLimit int) (model.QueryResult, error) {
	var result model.QueryResult
	var err error
	if statement.session != nil {
		result, err = statement.session.Query(ctx, statement.sql, rowLimit)
	} else {
		result, err = statement.executor.Query(ctx, statement.sql, rowLimit)
	}
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	if err := ValidateResult(result, false, DefaultLimits); err != nil {
		return model.QueryResult{}, err
	}
	return result, nil
}

func (statement *boundStatement) Execute(ctx context.Context) (model.QueryResult, error) {
	var result model.QueryResult
	var err error
	if statement.session != nil {
		result, err = statement.session.Execute(ctx, statement.sql)
	} else {
		result, err = statement.executor.Execute(ctx, statement.sql)
	}
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	return result, nil
}

func (statement *boundStatement) ExecuteTransactional(
	ctx context.Context,
	beforeCommit func(model.QueryResult) error,
) (model.QueryResult, error) {
	var tx businessdb.WriteTx
	var err error
	if statement.session != nil {
		tx, err = statement.session.BeginWriteTx(ctx)
	} else {
		tx, err = statement.executor.BeginWriteTx(ctx)
	}
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	if tx == nil {
		return model.QueryResult{}, &AuthError{Reason: ReasonDatabaseFailure}
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	result, err := tx.Execute(ctx, statement.sql)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	if beforeCommit != nil {
		if err := beforeCommit(result); err != nil {
			return model.QueryResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.QueryResult{}, ErrCommitOutcomeUnknown
	}
	committed = true
	return result, nil
}

func (statement *boundStatement) metadata() any {
	if statement.session != nil {
		return statement.session
	}
	return statement.executor
}

func (statement *boundStatement) TableHasIndex(schema, table string) (bool, error) {
	provider, ok := statement.metadata().(rules.MetadataProvider)
	if !ok {
		return false, fmt.Errorf("metadata capability unavailable")
	}
	return provider.TableHasIndex(schema, table)
}

func (statement *boundStatement) TableRowCount(schema, table string) (int64, error) {
	provider, ok := statement.metadata().(rules.MetadataProvider)
	if !ok {
		return 0, fmt.Errorf("metadata capability unavailable")
	}
	return provider.TableRowCount(schema, table)
}

func (statement *boundStatement) TransactionState() (rules.TransactionState, error) {
	provider, ok := statement.metadata().(rules.TransactionMetadataProvider)
	if !ok {
		return rules.TransactionState{}, fmt.Errorf("transaction metadata capability unavailable")
	}
	return provider.TransactionState()
}

func (statement *boundStatement) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	provider, ok := statement.metadata().(rules.MysqlTransactionMetadataProvider)
	if !ok {
		return rules.MysqlTransactionState{}, fmt.Errorf("transaction metadata capability unavailable")
	}
	return provider.MysqlTransactionState()
}

func (statement *boundStatement) Close() error {
	if statement == nil {
		return nil
	}
	if statement.reservation != nil {
		statement.reservation.Release()
		statement.reservation = nil
	}
	if statement.session != nil {
		err := statement.session.Close()
		statement.session = nil
		return fixedExecutionError(err)
	}
	return nil
}

// ReleaseReservation drops only process quota accounting. Session lifecycle is
// intentionally separate so a caller cannot obtain or manipulate its raw
// connection while request-scoped memory/concurrency is always released.
func (statement *boundStatement) ReleaseReservation() {
	if statement != nil && statement.reservation != nil {
		statement.reservation.Release()
		statement.reservation = nil
	}
}

var _ Statement = (*boundStatement)(nil)

func fixedExecutionError(err error) error {
	if err == nil {
		return nil
	}
	var databaseError *businessdb.DBError
	if errors.As(err, &databaseError) {
		// DBError is already a fixed A4 mapping and deliberately retains no
		// driver message, detail, hint, query text, or connection string.
		return databaseError
	}
	return StableError(err)
}
