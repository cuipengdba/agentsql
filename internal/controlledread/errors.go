package controlledread

import (
	"errors"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/discovery"
)

func safeDiscoveryError(err error) error {
	if errors.Is(err, executor.ErrPermissionDenied) {
		return discovery.NewPortError(discovery.CodePermissionDenied)
	}
	if errors.Is(err, executor.ErrQueryTimeout) {
		return discovery.NewPortError(discovery.CodeTimeout)
	}
	return discovery.NewPortError(discovery.CodeInternal)
}
