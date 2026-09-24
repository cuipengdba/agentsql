package authorizedexecute

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
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

type columnAuthorizationContextKey struct{}

// WithColumnAuthorization binds non-executable control-snapshot facts to the
// sole AuthorizedExecute façade. The request deliberately cannot carry SQL,
// credentials, a datasource, or a database capability.
func WithColumnAuthorization(ctx context.Context, request ColumnAuthorizationRequest) context.Context {
	if ctx == nil {
		return nil
	}
	request.Policies = clonePolicies(request.Policies)
	if request.Agent.Owner != nil {
		value := *request.Agent.Owner
		request.Agent.Owner = &value
	}
	if request.Agent.ExpiresAt != nil {
		value := *request.Agent.ExpiresAt
		request.Agent.ExpiresAt = &value
	}
	return context.WithValue(ctx, columnAuthorizationContextKey{}, request)
}

// ColumnAuthorizedStatement exposes only the sealed outcome of the exact
// statement routed by AuthorizedExecute; it cannot accept or execute SQL.
type ColumnAuthorizedStatement interface {
	Statement
	ColumnAuthorizationResult() (AuthorizedSelectResult, bool)
}

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
	if ctx == nil {
		return nil, &AuthError{Reason: ReasonDatabaseFailure}
	}
	column, explicitColumn := ctx.Value(columnAuthorizationContextKey{}).(ColumnAuthorizationRequest)
	var closeControl func() error
	if explicitColumn || gateway.column != nil {
		selectStatement, parseErr := isSingleSelect(datasource, sqlText)
		if explicitColumn && (parseErr != nil || !selectStatement || sessionID != "") {
			return nil, &AuthError{Reason: ReasonStatementClassDenied}
		}
		if !explicitColumn && parseErr == nil && selectStatement {
			scope := scopeFromContext(ctx)
			if scope.agent == "" {
				return nil, &AuthError{Reason: ReasonAgentDenied}
			}
			var beginErr error
			column, closeControl, beginErr = gateway.column.BeginColumnAuthorization(ctx, scope.agent, datasource)
			if beginErr != nil {
				return nil, beginErr
			}
			explicitColumn = true
		}
	}
	if explicitColumn {
		return &columnBoundStatement{
			gateway: gateway, datasource: datasource, secret: append([]byte(nil), secret...),
			sql: sqlText, request: column, closeControl: closeControl,
		}, nil
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

func isSingleSelect(datasource model.Datasource, sqlText string) (bool, error) {
	approved, err := parser.NewParser(model.DBDialect(datasource.DBType))
	if err != nil {
		return false, err
	}
	ast, err := approved.Parse(sqlText)
	if err != nil {
		return false, err
	}
	return ast != nil && !ast.IsMulti && ast.StmtType == model.StmtType("SELECT"), nil
}

type columnBoundStatement struct {
	mu           sync.Mutex
	gateway      *Gateway
	datasource   model.Datasource
	secret       []byte
	sql          string
	request      ColumnAuthorizationRequest
	executed     bool
	closed       bool
	outcome      AuthorizedSelectResult
	closeControl func() error
}

func (statement *columnBoundStatement) Dialect() string { return statement.datasource.DBType }
func (statement *columnBoundStatement) Execute(ctx context.Context) (model.QueryResult, error) {
	statement.mu.Lock()
	defer statement.mu.Unlock()
	if statement.closed || statement.executed || statement.gateway == nil {
		return model.QueryResult{}, &AuthError{Reason: ReasonPreparedState}
	}
	statement.executed = true
	outcome, err := statement.gateway.authorizedSelect(ctx, authorizedSelectRequest{
		ColumnAuthorizationRequest: statement.request,
		Datasource:                 statement.datasource,
		Secret:                     append([]byte(nil), statement.secret...),
		SQL:                        statement.sql,
	})
	controlErr := statement.closeControlLocked()
	if err != nil {
		return model.QueryResult{}, errors.Join(err, controlErr)
	}
	if controlErr != nil {
		return model.QueryResult{}, controlErr
	}
	statement.outcome = cloneAuthorizedSelectResult(outcome)
	returned := cloneAuthorizedSelectResult(outcome)
	return returned.Result, nil
}
func (statement *columnBoundStatement) Query(ctx context.Context, _ int) (model.QueryResult, error) {
	return statement.Execute(ctx)
}
func (statement *columnBoundStatement) Explain(context.Context) (model.ExplainInfo, error) {
	return model.ExplainInfo{}, &AuthError{Reason: ReasonStatementClassDenied}
}
func (statement *columnBoundStatement) ExecuteTransactional(context.Context, func(model.QueryResult) error) (model.QueryResult, error) {
	return model.QueryResult{}, &AuthError{Reason: ReasonStatementClassDenied}
}
func (statement *columnBoundStatement) TableHasIndex(string, string) (bool, error) {
	return false, &AuthError{Reason: ReasonStatementClassDenied}
}
func (statement *columnBoundStatement) TableRowCount(string, string) (int64, error) {
	return 0, &AuthError{Reason: ReasonStatementClassDenied}
}
func (statement *columnBoundStatement) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, &AuthError{Reason: ReasonStatementClassDenied}
}
func (statement *columnBoundStatement) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{}, &AuthError{Reason: ReasonStatementClassDenied}
}
func (statement *columnBoundStatement) ReleaseReservation() {}
func (statement *columnBoundStatement) Close() error {
	statement.mu.Lock()
	defer statement.mu.Unlock()
	statement.closed = true
	statement.secret = nil
	statement.sql = ""
	return statement.closeControlLocked()
}

func (statement *columnBoundStatement) closeControlLocked() error {
	if statement.closeControl == nil {
		return nil
	}
	closeControl := statement.closeControl
	statement.closeControl = nil
	return closeControl()
}
func (statement *columnBoundStatement) ColumnAuthorizationResult() (AuthorizedSelectResult, bool) {
	statement.mu.Lock()
	defer statement.mu.Unlock()
	if !statement.executed {
		return AuthorizedSelectResult{}, false
	}
	return cloneAuthorizedSelectResult(statement.outcome), true
}

func cloneAuthorizedSelectResult(source AuthorizedSelectResult) AuthorizedSelectResult {
	result := source
	result.Result.Columns = append([]string(nil), source.Result.Columns...)
	result.Result.Rows = make([][]string, len(source.Result.Rows))
	for index := range source.Result.Rows {
		result.Result.Rows[index] = append([]string(nil), source.Result.Rows[index]...)
	}
	result.Audit.Details = append([]ColumnAuditDetail(nil), source.Audit.Details...)
	result.Encoded = append([]byte(nil), source.Encoded...)
	return result
}

func clonePolicies(source []model.Policy) []model.Policy {
	result := append([]model.Policy(nil), source...)
	for index := range result {
		if source[index].RelationBinding != nil {
			binding := *source[index].RelationBinding
			if binding.StableObjectID != nil {
				value := *binding.StableObjectID
				binding.StableObjectID = &value
			}
			if binding.CatalogFingerprint != nil {
				value := *binding.CatalogFingerprint
				binding.CatalogFingerprint = &value
			}
			result[index].RelationBinding = &binding
		}
		result[index].ColumnPermissions = append([]model.PolicyColumnPermission(nil), source[index].ColumnPermissions...)
		result[index].ColumnStaging = append([]model.PolicyColumnPermissionStaging(nil), source[index].ColumnStaging...)
	}
	return result
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
