package executor

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
)

func classifyContextError(ctx context.Context, stage DBStage, cause error) (error, bool) {
	var contextError error
	if ctx != nil {
		contextError = ctx.Err()
	}
	if errors.Is(cause, context.DeadlineExceeded) ||
		errors.Is(contextError, context.DeadlineExceeded) {
		return newDBError(
			DBErrorKindTimeout,
			DBErrorCodeTimeout,
			stage,
			"",
			ErrQueryTimeout,
		), true
	}
	if errors.Is(cause, context.Canceled) || errors.Is(contextError, context.Canceled) {
		return newDBError(
			DBErrorKindInterrupted,
			DBErrorCodeInterrupted,
			stage,
			"",
			ErrQueryTimeout,
		), true
	}
	return nil, false
}

func isNetworkConnectionError(cause error) bool {
	if cause == nil {
		return false
	}
	if errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) ||
		errors.Is(cause, driver.ErrBadConn) {
		return true
	}
	var networkError net.Error
	if errors.As(cause, &networkError) {
		return true
	}

	// Some database/client boundaries flatten a connection failure to text.
	// Match only well-known connection phrases, never numeric client codes alone,
	// and never retain the source string in DBError.
	message := strings.ToLower(cause.Error())
	for _, marker := range []string{
		"broken pipe",
		"connection refused",
		"connection reset by peer",
		"server has gone away",
		"lost connection to mysql server",
		"can't connect to mysql server",
		"can't connect to local mysql server",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
