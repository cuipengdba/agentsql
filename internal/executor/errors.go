package executor

import (
	"errors"
	"fmt"
)

var (
	// ErrQueryTimeout indicates that a database or context deadline cancelled SQL.
	ErrQueryTimeout = errors.New("query timeout")
	// ErrReadOnlyViolated indicates that a write reached a read-only executor.
	ErrReadOnlyViolated = errors.New("read-only executor violation")
	// ErrDatasourceUnreachable indicates that a datasource could not be reached.
	ErrDatasourceUnreachable = errors.New("datasource unreachable")
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
