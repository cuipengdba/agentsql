package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
)

// mcpAssistant accepts the old request body while deriving its caller and
// datasource exclusively from a current, operator-owned catalog bridge. One
// catalog source is supported here; a multi-source catalog fails closed until
// the source-choice contract has original-client and authorization evidence.
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
	if len(entries) != 1 {
		writeJSON(w, http.StatusUnprocessableEntity, "Select one registered datasource for this assistant")
		return
	}
	selected := entries[0]
	_, mappings, revision, err := h.service.resolveAssistantCatalogBridge(r.Context(), input.URL, input.Authorization)
	if err != nil || mappings[selected.CatalogID] != selected.Datasource {
		failure(w, ErrUnauthenticated)
		return
	}
	check := func(ctx context.Context) error {
		current, latest, err := h.service.fetchAssistantCatalog(ctx, input.URL, input.Authorization, nil)
		if err != nil || current.User.ID != p.User.ID || current.User.AuthRevision != p.User.AuthRevision ||
			current.Workspace != p.Workspace || len(latest) != 1 || latest[0].CatalogID != selected.CatalogID ||
			latest[0].Datasource != selected.Datasource {
			return ErrUnauthenticated
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
		result["data"] = map[string]any{"fields": record.Data.Fields, "data": record.Data.Data, "fields_info": record.Data.FieldsInfo}
	}
	if !stream {
		writeJSON(w, http.StatusOK, result)
		return
	}
	// Emit framed SSE JSON only after the query, audit, persistence and final
	// authorization check succeed. The original route's success event sequence
	// still needs a working upstream/client baseline before parity is claimed.
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, event := range []map[string]any{
		{"type": "id", "id": record.ID},
		{"type": "sql", "content": record.SQL},
		{"type": "sql-data", "content": "execute-success"},
		{"type": "finish"},
	} {
		raw, err := json.Marshal(event)
		if err != nil {
			return
		}
		if _, err := w.Write(append(append([]byte("data:"), raw...), []byte("\n\n")...)); err != nil {
			return
		}
	}
}
