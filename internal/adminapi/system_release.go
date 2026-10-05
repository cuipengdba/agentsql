package adminapi

import (
	"net/http"

	"github.com/cuipengdba/agentsql/internal/version"
)

// systemRelease exposes only the running binary's release identity and the
// supported upgrade workflow. It never accepts a URL or fetches a release.
func (handler *Handler) systemRelease(writer http.ResponseWriter, _ *http.Request) {
	handler.ok(writer, struct {
		Version     string `json:"version"`
		UpgradeMode string `json:"upgrade_mode"`
	}{Version: version.Version, UpgradeMode: "check-and-dry-run"})
}
