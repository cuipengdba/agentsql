package chatbi

import (
	"errors"
	"net/http"
)

// These read-only responses match the SQLBot Community v1.10.2 browser
// bootstrap contract. The license record is SQLBot-specific and is not an
// assertion about AgentSQL's Apache-2.0 source license.
func (h *Handler) sqlbotLicenseStatus(w http.ResponseWriter, _ *http.Request) {
	okay(w, map[string]any{
		"license": nil,
		"message": "No license record found.",
		"status":  "no_record",
	})
}

func (h *Handler) sqlbotProtocolVersion(w http.ResponseWriter, _ *http.Request, _ Principal, _ string) {
	okay(w, "1.10.2")
}

// SQLBot's browser checks this endpoint before it opens the chat picker. The
// original endpoint has no response payload; model details and API keys must
// not be exposed by this compatibility probe.
func (h *Handler) sqlbotDefaultModel(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var model ModelConfig
	_, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &model)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			failure(w, ErrNotFound)
		} else {
			failure(w, ErrUnavailable)
		}
		return
	}
	if validateModelConfig(model) != nil {
		failure(w, ErrUnavailable)
		return
	}
	okay(w, nil)
}

// The SQLBot Community browser renders the question composer only when a
// conversation contains its initial, completed placeholder record. Keep that
// placeholder in the compatibility response, never in the query history or
// persisted AgentSQL record index. It has no SQL, data or execution authority.
func sqlbotChatView(chat Chat) Chat {
	if len(chat.Records) != 0 && chat.Records[0].FirstChat {
		return chat
	}
	first := Record{
		ID: chat.ID, Chat: chat.ID, Datasource: chat.Datasource,
		Created: chat.Created, Finish: true, FirstChat: true,
	}
	chat.Records = append([]Record{first}, chat.Records...)
	return chat
}

type sqlbotChatParameter struct {
	Key    string `json:"pkey"`
	Value  string `json:"pval"`
	ID     string `json:"id"`
	Type   string `json:"ptype"`
	SortNo int    `json:"sort_no"`
}

func (h *Handler) sqlbotChatParameters(w http.ResponseWriter, _ *http.Request, _ Principal, _ string) {
	// The v4.2 runtime uses three completed turns as model context, caps
	// controlled query results, and exposes SQL and audit information.
	// Editing these settings is not supported by this read-only endpoint.
	okay(w, []sqlbotChatParameter{
		{Key: "chat.hide_thinking_block", Value: "false", ID: "7520382585415929856", Type: "str"},
		{Key: "chat.expand_thinking_block", Value: "false", ID: "7520382585415929857", Type: "str"},
		{Key: "chat.limit_rows", Value: "true", ID: "7520382585415929858", Type: "str"},
		{Key: "chat.context_record_count", Value: "3", ID: "7520382585415929859", Type: "str"},
		{Key: "chat.show_sql", Value: "true", ID: "7520382585415929860", Type: "str"},
		{Key: "chat.show_log", Value: "true", ID: "7520382585415929861", Type: "str"},
	})
}
