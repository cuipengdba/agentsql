package chatbi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// mcpAssistant accepts the old request body while deriving its caller and
// datasource exclusively from a current, operator-owned catalog bridge.
// Multiple sources require an audited model choice of an already mapped ID.
func (h *Handler) mcpAssistant(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("X-SQLBOT-TOKEN")) != 0 ||
		len(r.Header.Values("X-SQLBOT-ASSISTANT-TOKEN")) != 0 || len(r.Header.Values("X-SQLBOT-ASK-TOKEN")) != 0 {
		failure(w, ErrUnauthenticated)
		return
	}
	var input mcpAssistantInput
	if !readBody(w, r, &input) {
		return
	}
	_, stream, err := parseMCPAssistantInput(input)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid third-party assistant request")
		return
	}
	p, entries, err := h.service.fetchAssistantCatalog(r.Context(), input.URL, input.Authorization, nil)
	if err != nil {
		failure(w, err)
		return
	}
	if len(entries) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, "No registered datasource for this assistant")
		return
	}
	_, mappings, revision, err := h.service.resolveAssistantCatalogBridge(r.Context(), input.URL, input.Authorization)
	if err != nil || len(entries) > len(mappings) {
		failure(w, ErrUnauthenticated)
		return
	}
	for _, entry := range entries {
		if mappings[entry.CatalogID] != entry.Datasource {
			failure(w, ErrUnauthenticated)
			return
		}
	}
	selected := entries[0]
	if len(entries) > 1 {
		selector, ok := h.service.Model.(assistantCatalogSelector)
		if !ok {
			writeJSON(w, http.StatusUnprocessableEntity, "Datasource selection is unavailable")
			return
		}
		var model ModelConfig
		if _, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &model); err != nil {
			failure(w, ErrModel)
			return
		}
		if err := h.service.Store.auditAssistantCatalogSelection(r.Context(), p, len(entries)); err != nil {
			failure(w, err)
			return
		}
		chosen, err := selector.SelectAssistantCatalogSource(r.Context(), model, input.Question, entries)
		if err != nil {
			failure(w, err)
			return
		}
		found := false
		for _, entry := range entries {
			if entry.CatalogID == chosen {
				selected, found = entry, true
				break
			}
		}
		if !found {
			failure(w, ErrModel)
			return
		}
	}
	check := func(ctx context.Context) error {
		current, latest, err := h.service.fetchAssistantCatalog(ctx, input.URL, input.Authorization, nil)
		if err != nil || current.User.ID != p.User.ID || current.User.AuthRevision != p.User.AuthRevision ||
			current.Workspace != p.Workspace || len(latest) != len(entries) {
			return ErrUnauthenticated
		}
		for index := range entries {
			if latest[index].CatalogID != entries[index].CatalogID || latest[index].Datasource != entries[index].Datasource ||
				latest[index].Name != entries[index].Name {
				return ErrUnauthenticated
			}
		}
		_, currentMappings, currentRevision, err := h.service.resolveAssistantCatalogBridge(ctx, input.URL, input.Authorization)
		if err != nil || currentRevision != revision || currentMappings[selected.CatalogID] != selected.Datasource {
			return ErrUnauthenticated
		}
		return nil
	}
	if err := check(r.Context()); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.auditAssistantCatalogAccess(r.Context(), p, selected.CatalogID, selected.Datasource); err != nil {
		failure(w, err)
		return
	}
	chat, err := h.service.StartMCP(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.service.BindMCPSource(r.Context(), p, chat.ID, selected.Datasource); err != nil {
		failure(w, err)
		return
	}
	record, err := h.service.Ask(r.Context(), p, chat.ID, input.Question, check, nil)
	if err != nil {
		failure(w, err)
		return
	}
	if err := check(r.Context()); err != nil {
		failure(w, err)
		return
	}
	result := map[string]any{"success": true, "record_id": json.Number(record.ID), "sql": record.SQL}
	if record.Data != nil {
		catalogID := any(string(selected.CatalogID))
		if numericID, err := strconv.ParseInt(string(selected.CatalogID), 10, 64); err == nil {
			catalogID = numericID
		}
		result["data"] = map[string]any{
			"fields": record.Data.Fields, "data": record.Data.Data, "fields_info": record.Data.FieldsInfo,
			"sql": base64.StdEncoding.EncodeToString([]byte(record.SQL)), "datasource": catalogID,
		}
	}
	if !stream {
		writeJSON(w, http.StatusOK, result)
		return
	}
	// The original third-party assistant wraps Markdown in a text/event-stream
	// response, unlike in-chat JSON SSE. Emit it only after the query, audit,
	// persistence and final authorization check succeed.
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fence := "```"
	for strings.Contains(record.SQL, fence) {
		fence += "`"
	}
	_, _ = fmt.Fprintf(w, "> Record ID: %s\n> %s\n\n%ssql\n%s\n%s\n\n", record.ID, mcpMarkdownCell(input.Question), fence, record.SQL, fence)
	if record.Data != nil {
		_, _ = io.WriteString(w, mcpMarkdownTable(*record.Data))
	}
}
