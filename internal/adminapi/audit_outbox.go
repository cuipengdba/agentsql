package adminapi

import (
	"net/http"
	"time"
)

type auditOutboxStatusView struct {
	Enabled          bool   `json:"enabled"`
	State            string `json:"state"`
	Pending          int    `json:"pending"`
	Retrying         int    `json:"retrying"`
	OldestAgeSeconds int64  `json:"oldest_age_seconds"`
}

func (handler *Handler) auditOutboxStatus(writer http.ResponseWriter, request *http.Request) {
	if handler.deps.Config.Store.Audit == nil || !handler.deps.Config.Store.Audit.Separate {
		handler.ok(writer, auditOutboxStatusView{State: "not_applicable"})
		return
	}
	pending, retrying, age, err := handler.deps.Runtime.Store.Outbox().TenantPendingStats(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	state := "healthy"
	if retrying > 0 || age >= time.Minute {
		state = "lagging"
	}
	if age >= 5*time.Minute {
		state = "critical"
	}
	handler.ok(writer, auditOutboxStatusView{
		Enabled: true, State: state, Pending: pending, Retrying: retrying,
		OldestAgeSeconds: int64(age.Seconds()),
	})
}
