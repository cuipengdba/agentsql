package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/cuipengdba/agentsql/internal/version"
)

const (
	durableStreamPageSize = 100
	durableStreamMaxLag   = 1000
	durableStreamHistory  = 100
	durableStreamPoll     = time.Second
)

// streamDurable reads the audit database rather than the local event hub. The
// cursor is tenant-local and allocated in the same transaction as the audit.
func (handler *Handler) streamDurable(writer http.ResponseWriter, request *http.Request, tenantID string) {
	ctx, err := store.WithTenant(request.Context(), tenantID)
	if err != nil {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	repository := handler.deps.Runtime.Store.AuditLogs()
	latest, err := repository.LatestStreamSequence(ctx)
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit stream unavailable")
		return
	}
	cursor := latest - durableStreamHistory
	if cursor < 0 {
		cursor = 0
	}
	if raw := request.Header.Get("Last-Event-ID"); raw != "" {
		if len(raw) > 19 || strings.Trim(raw, "0123456789") != "" {
			handler.fail(writer, http.StatusBadRequest, "invalid audit stream cursor")
			return
		}
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 || cursor > latest {
			handler.fail(writer, http.StatusBadRequest, "invalid audit stream cursor")
			return
		}
	}
	if latest-cursor > durableStreamMaxLag {
		handler.fail(writer, http.StatusConflict, "audit stream cursor too old")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-store, no-transform")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(writer)
	hello, err := json.Marshal(struct {
		Version string `json:"version"`
		Demo    bool   `json:"demo"`
	}{Version: version.Version, Demo: false})
	if err != nil || writeStreamFrame(controller, writer, "event: hello\ndata:"+string(hello)+"\n\n") != nil {
		return
	}
	if err := handler.drainDurableStream(ctx, controller, writer, &cursor); err != nil {
		return
	}
	poll := time.NewTicker(durableStreamPoll)
	defer poll.Stop()
	heartbeat := time.NewTicker(handler.heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if payload, ok := requestTokenPayload(request); ok && time.Now().Unix() >= payload.Expires {
				return
			}
			if err := handler.drainDurableStream(ctx, controller, writer, &cursor); err != nil {
				return
			}
		case <-heartbeat.C:
			if writeStreamFrame(controller, writer, ": ping\n\n") != nil {
				return
			}
		}
	}
}

func (handler *Handler) drainDurableStream(ctx context.Context, controller *http.ResponseController, writer http.ResponseWriter, cursor *int64) error {
	repository := handler.deps.Runtime.Store.AuditLogs()
	latest, err := repository.LatestStreamSequence(ctx)
	if err != nil || latest < *cursor || latest-*cursor > durableStreamMaxLag {
		return fmt.Errorf("audit stream unavailable or cursor exceeded backlog")
	}
	for *cursor < latest {
		items, err := repository.StreamAfter(ctx, *cursor, durableStreamPageSize)
		if err != nil || len(items) == 0 {
			return fmt.Errorf("audit stream gap or unavailable")
		}
		for _, item := range items {
			data, err := json.Marshal(auditToStreamView(item.Audit))
			if err != nil {
				return fmt.Errorf("marshal audit stream event: %w", err)
			}
			frame := "event: audit\nid: " + strconv.FormatInt(item.Sequence, 10) + "\ndata:" + string(data) + "\n\n"
			if err := writeStreamFrame(controller, writer, frame); err != nil {
				return err
			}
			*cursor = item.Sequence
		}
	}
	return nil
}
