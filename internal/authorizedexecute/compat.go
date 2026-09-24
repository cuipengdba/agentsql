// Package authorizedexecute is the only package-level capability boundary for
// operations against business datasources. The concrete drivers and their raw
// SQL handles live below the nested internal/businessdb package.
package authorizedexecute

import (
	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
)

// Stable error aliases preserve the A4 public envelope without exposing a
// driver, pool, session, transaction, or arbitrary-SQL capability.
type (
	DBError     = businessdb.DBError
	DBErrorCode = businessdb.DBErrorCode
	DBErrorKind = businessdb.DBErrorKind
	DBStage     = businessdb.DBStage
	PoolStat    = businessdb.PoolStat
)

const (
	DBErrorCodeObjectNotFound       = businessdb.DBErrorCodeObjectNotFound
	DBErrorCodeColumnNotFound       = businessdb.DBErrorCodeColumnNotFound
	DBErrorCodeAlreadyExists        = businessdb.DBErrorCodeAlreadyExists
	DBErrorCodeSyntax               = businessdb.DBErrorCodeSyntax
	DBErrorCodeSemantic             = businessdb.DBErrorCodeSemantic
	DBErrorCodeData                 = businessdb.DBErrorCodeData
	DBErrorCodeConstraint           = businessdb.DBErrorCodeConstraint
	DBErrorCodeRetryable            = businessdb.DBErrorCodeRetryable
	DBErrorCodeTransaction          = businessdb.DBErrorCodeTransaction
	DBErrorCodeResource             = businessdb.DBErrorCodeResource
	DBErrorCodeTimeout              = businessdb.DBErrorCodeTimeout
	DBErrorCodeInterrupted          = businessdb.DBErrorCodeInterrupted
	DBErrorCodePermission           = businessdb.DBErrorCodePermission
	DBErrorCodeReadOnly             = businessdb.DBErrorCodeReadOnly
	DBErrorCodeAuthentication       = businessdb.DBErrorCodeAuthentication
	DBErrorCodeDatabaseNotFound     = businessdb.DBErrorCodeDatabaseNotFound
	DBErrorCodeConnection           = businessdb.DBErrorCodeConnection
	DBErrorCodeExecution            = businessdb.DBErrorCodeExecution
	DBErrorCodeGatewayInternal      = businessdb.DBErrorCodeGatewayInternal
	DBErrorCodeAuditUnavailable     = businessdb.DBErrorCodeAuditUnavailable
	DBErrorCodeAuditOverloaded      = businessdb.DBErrorCodeAuditOverloaded
	DBErrorCodeCommitOutcomeUnknown = businessdb.DBErrorCodeCommitOutcomeUnknown

	DBErrorKindObjectNotFound   = businessdb.DBErrorKindObjectNotFound
	DBErrorKindColumnNotFound   = businessdb.DBErrorKindColumnNotFound
	DBErrorKindAlreadyExists    = businessdb.DBErrorKindAlreadyExists
	DBErrorKindSyntax           = businessdb.DBErrorKindSyntax
	DBErrorKindSemantic         = businessdb.DBErrorKindSemantic
	DBErrorKindData             = businessdb.DBErrorKindData
	DBErrorKindConstraint       = businessdb.DBErrorKindConstraint
	DBErrorKindRetryable        = businessdb.DBErrorKindRetryable
	DBErrorKindTransaction      = businessdb.DBErrorKindTransaction
	DBErrorKindResource         = businessdb.DBErrorKindResource
	DBErrorKindTimeout          = businessdb.DBErrorKindTimeout
	DBErrorKindInterrupted      = businessdb.DBErrorKindInterrupted
	DBErrorKindPermission       = businessdb.DBErrorKindPermission
	DBErrorKindReadOnly         = businessdb.DBErrorKindReadOnly
	DBErrorKindAuthentication   = businessdb.DBErrorKindAuthentication
	DBErrorKindDatabaseNotFound = businessdb.DBErrorKindDatabaseNotFound
	DBErrorKindConnection       = businessdb.DBErrorKindConnection
	DBErrorKindExecution        = businessdb.DBErrorKindExecution

	DBStageParse    = businessdb.DBStageParse
	DBStageConnect  = businessdb.DBStageConnect
	DBStagePing     = businessdb.DBStagePing
	DBStageAcquire  = businessdb.DBStageAcquire
	DBStageBeginTx  = businessdb.DBStageBeginTx
	DBStageExplain  = businessdb.DBStageExplain
	DBStageMetadata = businessdb.DBStageMetadata
	DBStageQuery    = businessdb.DBStageQuery
	DBStageReadRows = businessdb.DBStageReadRows
	DBStageExecute  = businessdb.DBStageExecute
	DBStageCommit   = businessdb.DBStageCommit
	DBStageRollback = businessdb.DBStageRollback
)

var (
	ErrQueryTimeout             = businessdb.ErrQueryTimeout
	ErrReadOnlyViolated         = businessdb.ErrReadOnlyViolated
	ErrDatasourceUnreachable    = businessdb.ErrDatasourceUnreachable
	ErrPermissionDenied         = businessdb.ErrPermissionDenied
	ErrSessionExists            = businessdb.ErrSessionExists
	ErrSessionClosed            = businessdb.ErrSessionClosed
	ErrTransactionDone          = businessdb.ErrTransactionDone
	ErrSessionTransactionActive = businessdb.ErrSessionTransactionActive
)

func NewDBError(kind DBErrorKind, code DBErrorCode, stage DBStage) *DBError {
	return businessdb.NewDBError(kind, code, stage)
}

func Suggestion(code DBErrorCode) string { return businessdb.Suggestion(code) }
