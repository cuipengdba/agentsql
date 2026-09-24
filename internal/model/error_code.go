package model

// DBErrorCode is a stable public error code shared by database, pipeline, and
// audit boundaries.
type DBErrorCode string

const (
	DBErrorCodeObjectNotFound       DBErrorCode = "DB_OBJECT_NOT_FOUND"
	DBErrorCodeColumnNotFound       DBErrorCode = "DB_COLUMN_NOT_FOUND"
	DBErrorCodeAlreadyExists        DBErrorCode = "DB_OBJECT_ALREADY_EXISTS"
	DBErrorCodeSyntax               DBErrorCode = "DB_SYNTAX_ERROR"
	DBErrorCodeSemantic             DBErrorCode = "DB_SEMANTIC_ERROR"
	DBErrorCodeData                 DBErrorCode = "DB_DATA_EXCEPTION"
	DBErrorCodeConstraint           DBErrorCode = "DB_CONSTRAINT_VIOLATION"
	DBErrorCodeRetryable            DBErrorCode = "DB_RETRYABLE_CONFLICT"
	DBErrorCodeTransaction          DBErrorCode = "DB_TRANSACTION_STATE"
	DBErrorCodeResource             DBErrorCode = "DB_RESOURCE_EXHAUSTED"
	DBErrorCodeTimeout              DBErrorCode = "DB_QUERY_TIMEOUT"
	DBErrorCodeInterrupted          DBErrorCode = "DB_QUERY_INTERRUPTED"
	DBErrorCodePermission           DBErrorCode = "DB_PERMISSION_DENIED"
	DBErrorCodeReadOnly             DBErrorCode = "DB_READ_ONLY_VIOLATION"
	DBErrorCodeAuthentication       DBErrorCode = "DB_AUTHENTICATION_FAILED"
	DBErrorCodeDatabaseNotFound     DBErrorCode = "DB_DATABASE_NOT_FOUND"
	DBErrorCodeConnection           DBErrorCode = "DB_DATASOURCE_UNREACHABLE"
	DBErrorCodeExecution            DBErrorCode = "DB_EXECUTION_FAILED"
	DBErrorCodeGatewayInternal      DBErrorCode = "GATEWAY_INTERNAL"
	DBErrorCodeAuditUnavailable     DBErrorCode = "AUDIT_UNAVAILABLE"
	DBErrorCodeAuditOverloaded      DBErrorCode = "AUDIT_OVERLOADED"
	DBErrorCodeCommitOutcomeUnknown DBErrorCode = "COMMIT_OUTCOME_UNKNOWN"
)
