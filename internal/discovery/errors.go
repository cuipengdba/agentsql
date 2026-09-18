package discovery

import "errors"

var (
	ErrInvalidRequest         = errors.New("discovery request is invalid")
	ErrScopeLimitExceeded     = errors.New("discovery scope limit exceeded")
	ErrCandidateLimitExceeded = errors.New("discovery candidate limit exceeded")
	ErrSampleLimitExceeded    = errors.New("discovery sample limit exceeded")
	ErrScopeNotVisible        = errors.New("discovery scope is not visible")
	ErrNotApplicable          = errors.New("discovery finding is not applicable")
	ErrUnknownCategory        = errors.New("unknown discovery category")
	ErrInternal               = errors.New("discovery internal error")
)

// Compatibility aliases use the shorter terminology found in the design.
var (
	ErrRangeExceeded  = ErrScopeLimitExceeded
	ErrCandidateLimit = ErrCandidateLimitExceeded
)

// ErrorCode is a stable error classification for later HTTP mapping.
type ErrorCode string

const (
	CodeInvalidRequest  ErrorCode = "DISCOVERY_INVALID_REQUEST"
	CodeScopeLimit      ErrorCode = "DISCOVERY_SCOPE_LIMIT"
	CodeCandidateLimit  ErrorCode = "DISCOVERY_CANDIDATE_LIMIT"
	CodeSampleLimit     ErrorCode = "DISCOVERY_SAMPLE_LIMIT"
	CodeScopeNotVisible ErrorCode = "DISCOVERY_SCOPE_NOT_VISIBLE"
	CodeNotApplicable   ErrorCode = "DISCOVERY_NOT_APPLICABLE"
	CodeUnknownCategory ErrorCode = "DISCOVERY_UNKNOWN_CATEGORY"
	CodeInternal        ErrorCode = "DISCOVERY_INTERNAL"
)

// ClassifiedError deliberately carries no adapter error text or sample data.
type ClassifiedError struct {
	Code ErrorCode
	err  error
}

func (err *ClassifiedError) Error() string {
	if err == nil || err.err == nil {
		return ErrInternal.Error()
	}
	return err.err.Error()
}

func (err *ClassifiedError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.err
}

func classified(code ErrorCode, sentinel error) error {
	return &ClassifiedError{Code: code, err: sentinel}
}
