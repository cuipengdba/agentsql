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
	ErrPermissionDenied       = errors.New("discovery permission denied")
	ErrRateLimited            = errors.New("discovery rate limited")
	ErrTimeout                = errors.New("discovery timeout")
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
	CodeInvalidRequest   ErrorCode = "DISCOVERY_INVALID_REQUEST"
	CodeScopeLimit       ErrorCode = "DISCOVERY_SCOPE_LIMIT"
	CodeCandidateLimit   ErrorCode = "DISCOVERY_CANDIDATE_LIMIT"
	CodeSampleLimit      ErrorCode = "DISCOVERY_SAMPLE_LIMIT"
	CodeScopeNotVisible  ErrorCode = "DISCOVERY_SCOPE_NOT_VISIBLE"
	CodeNotApplicable    ErrorCode = "DISCOVERY_NOT_APPLICABLE"
	CodeUnknownCategory  ErrorCode = "DISCOVERY_UNKNOWN_CATEGORY"
	CodePermissionDenied ErrorCode = "DISCOVERY_PERMISSION_DENIED"
	CodeRateLimited      ErrorCode = "DISCOVERY_RATE_LIMITED"
	CodeTimeout          ErrorCode = "DISCOVERY_TIMEOUT"
	CodeInternal         ErrorCode = "DISCOVERY_INTERNAL"
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

// NewPortError creates a redacted error that a trusted adapter may return
// through SchemaLister or LimitedQuerier. Only stable discovery sentinels are
// accepted; adapter and driver error text must never be attached.
func NewPortError(code ErrorCode) error {
	var sentinel error
	switch code {
	case CodeInvalidRequest:
		sentinel = ErrInvalidRequest
	case CodeScopeLimit:
		sentinel = ErrScopeLimitExceeded
	case CodeCandidateLimit:
		sentinel = ErrCandidateLimitExceeded
	case CodeSampleLimit:
		sentinel = ErrSampleLimitExceeded
	case CodeScopeNotVisible:
		sentinel = ErrScopeNotVisible
	case CodeNotApplicable:
		sentinel = ErrNotApplicable
	case CodeUnknownCategory:
		sentinel = ErrUnknownCategory
	case CodePermissionDenied:
		sentinel = ErrPermissionDenied
	case CodeRateLimited:
		sentinel = ErrRateLimited
	case CodeTimeout:
		sentinel = ErrTimeout
	default:
		code = CodeInternal
		sentinel = ErrInternal
	}
	return classified(code, sentinel)
}
