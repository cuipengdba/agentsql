package executor

import (
	"errors"
	"fmt"
)

// DBErrorKind is the executor's dialect-independent database error category.
type DBErrorKind string

const (
	DBErrorKindObjectNotFound   DBErrorKind = "object_not_found"
	DBErrorKindColumnNotFound   DBErrorKind = "column_not_found"
	DBErrorKindAlreadyExists    DBErrorKind = "already_exists"
	DBErrorKindSyntax           DBErrorKind = "syntax"
	DBErrorKindSemantic         DBErrorKind = "semantic"
	DBErrorKindData             DBErrorKind = "data"
	DBErrorKindConstraint       DBErrorKind = "constraint"
	DBErrorKindRetryable        DBErrorKind = "retryable"
	DBErrorKindTransaction      DBErrorKind = "transaction"
	DBErrorKindResource         DBErrorKind = "resource"
	DBErrorKindTimeout          DBErrorKind = "timeout"
	DBErrorKindInterrupted      DBErrorKind = "interrupted"
	DBErrorKindPermission       DBErrorKind = "permission"
	DBErrorKindReadOnly         DBErrorKind = "read_only"
	DBErrorKindAuthentication   DBErrorKind = "authentication"
	DBErrorKindDatabaseNotFound DBErrorKind = "database_not_found"
	DBErrorKindConnection       DBErrorKind = "connection"
	DBErrorKindExecution        DBErrorKind = "execution"
)

// DBErrorCode is a stable public error code shared by database dialects.
type DBErrorCode string

const (
	DBErrorCodeObjectNotFound   DBErrorCode = "DB_OBJECT_NOT_FOUND"
	DBErrorCodeColumnNotFound   DBErrorCode = "DB_COLUMN_NOT_FOUND"
	DBErrorCodeAlreadyExists    DBErrorCode = "DB_OBJECT_ALREADY_EXISTS"
	DBErrorCodeSyntax           DBErrorCode = "DB_SYNTAX_ERROR"
	DBErrorCodeSemantic         DBErrorCode = "DB_SEMANTIC_ERROR"
	DBErrorCodeData             DBErrorCode = "DB_DATA_EXCEPTION"
	DBErrorCodeConstraint       DBErrorCode = "DB_CONSTRAINT_VIOLATION"
	DBErrorCodeRetryable        DBErrorCode = "DB_RETRYABLE_CONFLICT"
	DBErrorCodeTransaction      DBErrorCode = "DB_TRANSACTION_STATE"
	DBErrorCodeResource         DBErrorCode = "DB_RESOURCE_EXHAUSTED"
	DBErrorCodeTimeout          DBErrorCode = "DB_QUERY_TIMEOUT"
	DBErrorCodeInterrupted      DBErrorCode = "DB_QUERY_INTERRUPTED"
	DBErrorCodePermission       DBErrorCode = "DB_PERMISSION_DENIED"
	DBErrorCodeReadOnly         DBErrorCode = "DB_READ_ONLY_VIOLATION"
	DBErrorCodeAuthentication   DBErrorCode = "DB_AUTHENTICATION_FAILED"
	DBErrorCodeDatabaseNotFound DBErrorCode = "DB_DATABASE_NOT_FOUND"
	DBErrorCodeConnection       DBErrorCode = "DB_DATASOURCE_UNREACHABLE"
	DBErrorCodeExecution        DBErrorCode = "DB_EXECUTION_FAILED"

	// These non-database public codes are reserved here for the later pipeline
	// and audit steps. They are not emitted by executor classifiers.
	DBErrorCodeGatewayInternal      DBErrorCode = "GATEWAY_INTERNAL"
	DBErrorCodeAuditUnavailable     DBErrorCode = "AUDIT_UNAVAILABLE"
	DBErrorCodeAuditOverloaded      DBErrorCode = "AUDIT_OVERLOADED"
	DBErrorCodeCommitOutcomeUnknown DBErrorCode = "COMMIT_OUTCOME_UNKNOWN"
)

// DBStage identifies the database interaction that produced a DBError.
type DBStage string

const (
	DBStageParse    DBStage = "parse"
	DBStageConnect  DBStage = "connect"
	DBStagePing     DBStage = "ping"
	DBStageAcquire  DBStage = "acquire"
	DBStageBeginTx  DBStage = "begin_tx"
	DBStageExplain  DBStage = "explain"
	DBStageMetadata DBStage = "metadata"
	DBStageQuery    DBStage = "query"
	DBStageReadRows DBStage = "read_rows"
	DBStageExecute  DBStage = "execute"
	DBStageCommit   DBStage = "commit"
	DBStageRollback DBStage = "rollback"
)

var dbErrorMessages = map[DBErrorCode]string{
	DBErrorCodeObjectNotFound:   "表或对象不存在",
	DBErrorCodeColumnNotFound:   "列不存在",
	DBErrorCodeAlreadyExists:    "对象已存在",
	DBErrorCodeSyntax:           "SQL 语法有误",
	DBErrorCodeSemantic:         "SQL 语义有误",
	DBErrorCodeData:             "数据异常，语句未执行",
	DBErrorCodeConstraint:       "操作违反数据库约束，数据未被修改",
	DBErrorCodeRetryable:        "数据库锁冲突或并发冲突，可稍后重试",
	DBErrorCodeTransaction:      "事务状态异常",
	DBErrorCodeResource:         "数据库资源不足",
	DBErrorCodeTimeout:          "语句执行超时",
	DBErrorCodeInterrupted:      "语句执行被中断",
	DBErrorCodePermission:       "数据库账号权限不足",
	DBErrorCodeReadOnly:         "目标数据库或连接为只读，写入被拒绝",
	DBErrorCodeAuthentication:   "数据库认证失败",
	DBErrorCodeDatabaseNotFound: "目标数据库不存在",
	DBErrorCodeConnection:       "无法连接到数据源",
	DBErrorCodeExecution:        "数据库执行失败",
}

var dbErrorSuggestions = map[DBErrorCode]string{
	DBErrorCodeObjectNotFound:   "请检查对象名称和当前数据库",
	DBErrorCodeColumnNotFound:   "请检查列名、SELECT 列表与条件字段",
	DBErrorCodeAlreadyExists:    "请更换名称或检查现有对象",
	DBErrorCodeSyntax:           "请检查 SQL 语句的拼写与结构",
	DBErrorCodeSemantic:         "请检查字段、类型与语句结构",
	DBErrorCodeData:             "请检查输入数据的类型与取值",
	DBErrorCodeConstraint:       "请检查唯一值、外键关联与必填字段",
	DBErrorCodeRetryable:        "请稍后重试；网关不会自动重试写操作",
	DBErrorCodeTransaction:      "请检查事务控制语句后重试",
	DBErrorCodeResource:         "请缩小查询范围或联系数据库管理员",
	DBErrorCodeTimeout:          "请缩小查询范围或稍后重试",
	DBErrorCodeInterrupted:      "如仍需要结果请重新发起查询",
	DBErrorCodePermission:       "请联系数据库管理员授权",
	DBErrorCodeReadOnly:         "请在可写库上执行或调整语句",
	DBErrorCodeAuthentication:   "请检查数据源账号与密码配置",
	DBErrorCodeDatabaseNotFound: "请检查数据源中的数据库名配置",
	DBErrorCodeConnection:       "请检查数据源地址、网络与端口，或稍后重试",
	DBErrorCodeExecution:        "请检查语句后重试；如反复出现请查看审计记录并联系管理员",
}

const (
	genericDBErrorMessage    = "数据库执行失败"
	genericDBErrorSuggestion = "请检查语句后重试；如反复出现请查看审计记录并联系管理员"
)

// DBError is a safe database error. It deliberately does not retain the
// original driver error, so no unwrap chain can expose driver messages, SQL,
// connection details, or database object names.
type DBError struct {
	Kind       DBErrorKind `json:"kind"`
	Code       DBErrorCode `json:"code"`
	DriverCode string      `json:"-"`
	Stage      DBStage     `json:"stage"`
	compat     error
}

func (err DBError) Error() string {
	if message, ok := dbErrorMessages[err.Code]; ok {
		return message
	}
	return genericDBErrorMessage
}

// Is exposes only a deliberately selected compatibility sentinel. DBError has
// no Unwrap method because original database driver errors must remain hidden.
func (err DBError) Is(target error) bool {
	return err.compat != nil && errors.Is(err.compat, target)
}

// DriverCodeForLog returns the SQLSTATE or MySQL server error number for
// controlled structured server logging. It must not be placed in public API,
// audit, or ordinary error text.
func (err DBError) DriverCodeForLog() (string, bool) {
	return err.DriverCode, err.DriverCode != ""
}

// Suggestion returns the fixed safe remediation text for code.
func Suggestion(code DBErrorCode) string {
	if suggestion, ok := dbErrorSuggestions[code]; ok {
		return suggestion
	}
	return genericDBErrorSuggestion
}

func newDBError(
	kind DBErrorKind,
	code DBErrorCode,
	stage DBStage,
	driverCode string,
	compat error,
) *DBError {
	return &DBError{
		Kind:       kind,
		Code:       code,
		DriverCode: driverCode,
		Stage:      stage,
		compat:     compat,
	}
}

// NewDBError constructs a safe classified error for non-driver failures that
// must use the same public database error envelope. It deliberately accepts no
// underlying cause, so parser or driver details cannot enter an unwrap chain.
func NewDBError(kind DBErrorKind, code DBErrorCode, stage DBStage) *DBError {
	return newDBError(kind, code, stage, "", nil)
}

func addDBErrorCompat(err error, sentinel error) error {
	var databaseError *DBError
	if !errors.As(err, &databaseError) || sentinel == nil {
		return err
	}
	if databaseError.compat == nil {
		databaseError.compat = sentinel
	} else if !errors.Is(databaseError.compat, sentinel) {
		databaseError.compat = errors.Join(databaseError.compat, sentinel)
	}
	return err
}

var (
	// ErrQueryTimeout indicates that a database or context deadline cancelled SQL.
	ErrQueryTimeout = errors.New("query timeout")
	// ErrReadOnlyViolated indicates that a write reached a read-only executor.
	ErrReadOnlyViolated = errors.New("read-only executor violation")
	// ErrDatasourceUnreachable indicates that a datasource could not be reached.
	ErrDatasourceUnreachable = errors.New("datasource unreachable")
	// ErrPermissionDenied indicates that the datasource account cannot read the
	// requested database object. Driver messages are deliberately redacted.
	ErrPermissionDenied = errors.New("datasource permission denied")
	// ErrSessionExists indicates that a session ID is already bound.
	ErrSessionExists = errors.New("executor session already exists")
	// ErrSessionClosed indicates that a bound session has been released.
	ErrSessionClosed = errors.New("executor session is closed")
	// ErrTransactionDone indicates that a write transaction is already terminal.
	ErrTransactionDone = errors.New("write transaction is done")
	// ErrSessionTransactionActive rejects nesting the audit barrier transaction.
	ErrSessionTransactionActive = errors.New("session already has an active transaction")
)

type redactedError struct {
	message string
	cause   error
}

func (err redactedError) Error() string {
	return err.message
}

func (err redactedError) Unwrap() error {
	return err.cause
}

func safeError(message string, sentinel error, cause error) error {
	joined := sentinel
	if cause != nil {
		joined = errors.Join(sentinel, sanitizedCause(cause))
	}
	return redactedError{
		message: fmt.Sprintf("%s: %s", message, sentinel),
		cause:   joined,
	}
}

func sanitizedCause(cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("redacted database error type %T", cause)
}
