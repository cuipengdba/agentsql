package authorizedexecute

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
)

func TestEasyDeployBinderErrorsUseStableEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err    error
		reason Reason
	}{
		{businessdb.NewCapabilityFailure(businessdb.BinderCodeModeRequired), ReasonBinderModeRequired},
		{businessdb.NewCapabilityFailure(businessdb.BinderCodeModeUnsupported), ReasonBinderModeUnsupported},
		{businessdb.NewLockFailure(), ReasonBindLockFailed},
		{businessdb.NewIdentityDriftFailure(), ReasonBindIdentityDrift},
	}
	for _, test := range tests {
		if got := StableError(test.err).Reason; got != test.reason {
			t.Fatalf("got %s want %s", got, test.reason)
		}
	}
}
