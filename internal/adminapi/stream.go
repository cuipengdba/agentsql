package adminapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/version"
)

const streamWriteTimeout = 5 * time.Second

func (handler *Handler) stream(writer http.ResponseWriter, request *http.Request) {
	if !supportsFlush(writer) {
		handler.fail(writer, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	select {
	case handler.streamSlots <- struct{}{}:
		defer func() { <-handler.streamSlots }()
	default:
		handler.fail(writer, http.StatusServiceUnavailable, "event stream connection limit reached")
		return
	}

	hub := handler.deps.Runtime.Events
	events, cancel := hub.Subscribe()
	defer cancel()
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-store, no-transform")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")

	hello, err := json.Marshal(struct {
		Version string `json:"version"`
		Demo    bool   `json:"demo"`
	}{Version: version.Version, Demo: false})
	if err != nil {
		return
	}
	controller := http.NewResponseController(writer)
	if err := writeStreamFrame(controller, writer, "event: hello\ndata:"+string(hello)+"\n\n"); err != nil {
		return
	}

	// Drain queued history before heartbeats. The Hub guarantees live events are
	// queued after the history snapshot under the same lock.
	for {
		select {
		case event, open := <-events:
			if !open || handler.writeAuditEvent(controller, writer, event) != nil {
				return
			}
		default:
			goto live
		}
	}

live:
	ticker := time.NewTicker(handler.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if err := handler.writeAuditEvent(controller, writer, event); err != nil {
				return
			}
		case <-ticker.C:
			if err := writeStreamFrame(controller, writer, ": ping\n\n"); err != nil {
				return
			}
		}
	}
}

func (handler *Handler) writeAuditEvent(controller *http.ResponseController, writer http.ResponseWriter, event eventbus.Event) error {
	data, err := json.Marshal(auditToStreamView(event.Audit))
	if err != nil {
		return fmt.Errorf("marshal audit stream event: %w", err)
	}
	frame := "event: audit\nid: " + strconv.FormatInt(event.Audit.ID, 10) + "\ndata:" + string(data) + "\n\n"
	return writeStreamFrame(controller, writer, frame)
}

func writeStreamFrame(controller *http.ResponseController, writer http.ResponseWriter, frame string) error {
	if err := controller.SetWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("set event stream write deadline: %w", err)
	}
	if _, err := io.WriteString(writer, frame); err != nil {
		return fmt.Errorf("write event stream frame: %w", err)
	}
	if err := controller.Flush(); err != nil {
		return fmt.Errorf("flush event stream frame: %w", err)
	}
	return nil
}

func supportsFlush(writer http.ResponseWriter) bool {
	for writer != nil {
		if _, ok := writer.(http.Flusher); ok {
			return true
		}
		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		writer = unwrapper.Unwrap()
	}
	return false
}
