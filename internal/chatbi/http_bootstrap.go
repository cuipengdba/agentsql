package chatbi

import (
	"errors"
	"net/http"
	"strconv"
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

func (h *Handler) sqlbotChatParameters(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	value, err := h.service.Store.ChatSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, chatParameterList(value, false))
}

// The original login and user-management pages read these settings before
// authentication. AgentSQL provisions user passwords explicitly and never
// publishes a shared default password to the browser.
func (h *Handler) sqlbotLoginParameters(w http.ResponseWriter, r *http.Request) {
	settings, err := h.service.Store.ChatSettings(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, loginParameterList(settings))
}

func loginParameterList(settings chatSettings) []sqlbotChatParameter {
	return []sqlbotChatParameter{
		{Key: "login.default_login", Value: "0", ID: "agentsql-login-default", Type: "str"},
		{Key: "login.initial_pwd_disabled", Value: strconv.FormatBool(settings.InitialPasswordDisabled), ID: "agentsql-login-initial-disabled", Type: "str"},
		{Key: "login.initial_pwd_hidden", Value: "true", ID: "agentsql-login-initial-hidden", Type: "str"},
	}
}

func (h *Handler) sqlbotDefaultPassword(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, "")
}
