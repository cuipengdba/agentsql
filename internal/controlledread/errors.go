package controlledread

import (
	"errors"

	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/executor"
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
