package b5coordinator

import (
	"context"
	"errors"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5session"
)

type SessionFailure struct{ Code b5.ErrorCode }

func (failure *SessionFailure) Error() string { return string(failure.Code) }

func sessionFailureCode(err error) b5.ErrorCode {
	var failure *SessionFailure
	if errors.As(err, &failure) && failure.Code != "" {
		return failure.Code
	}
	return b5.ErrorSessionNotFoundOrDenied
}

// DirectorySessionGate preserves b5session's proof/owner/sticky-route rules.
// It performs Lookup, never Touch: invalid, stale and wrong-instance requests
// therefore cannot extend a victim transaction or acquire cleanup authority.
type DirectorySessionGate struct{ Directory *b5session.Directory }

func (gate DirectorySessionGate) Validate(ctx context.Context, authorization SessionAuthorization) error {
	if gate.Directory == nil {
		return &SessionFailure{Code: b5.ErrorSessionRouteUnavailable}
	}
	decision, err := gate.Directory.Lookup(ctx, b5session.ContinuationInput{AgentSQLSessionID: authorization.SessionID, McpSessionID: authorization.McpSessionID, PrincipalID: authorization.PrincipalID, InstanceID: authorization.InstanceID, Method: authorization.Method, RequestID: authorization.RequestID, OwnerEpoch: authorization.OwnerEpoch, ExpectedSeq: authorization.ExpectedSeq, BodyDigest: authorization.BodyDigest, Proof: authorization.Proof})
	if err != nil {
		return err
	}
	if !decision.Authorized {
		return &SessionFailure{Code: decision.Code}
	}
	return nil
}

var _ SessionGate = DirectorySessionGate{}
