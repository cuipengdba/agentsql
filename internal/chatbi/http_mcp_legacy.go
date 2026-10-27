package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// These request shapes are compatible with SQLBot's MCP HTTP helper. Authority
// always comes from AgentSQL's live token and membership, never an input oid.
func (h *Handler) mcpAccessToken(w http.ResponseWriter, r *http.Request) {
	if !h.loginLimit.Allow() {
		writeJSON(w, http.StatusTooManyRequests, "Too many login requests")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readBody(w, r, &input) {
		return
	}
	_, token, err := h.service.Identity.Login(r.Context(), input.Username, input.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, "Incorrect account or password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": token})
}

func (h *Handler) mcpStart(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
		OID      *ID    `json:"oid"`
	}
	if !readBody(w, r, &input) {
		return
	}
	token := input.Token
	if token == "" {
		if !h.loginLimit.Allow() {
			writeJSON(w, http.StatusTooManyRequests, "Too many login requests")
			return
		}
		_, issued, err := h.service.Identity.Login(r.Context(), input.Username, input.Password)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, "Incorrect account or password")
			return
		}
		token = issued
	} else if input.Username != "" || input.Password != "" {
		failure(w, ErrUnauthenticated)
		return
	}
	p, ok := h.mcpBodyPrincipal(w, r, token, input.OID)
	if !ok {
		return
	}
	chat, err := h.service.StartMCP(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	if _, err := h.service.Identity.Verify(r.Context(), token); err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "chat_id": json.Number(chat.ID)})
}

func (h *Handler) mcpBodyPrincipal(w http.ResponseWriter, r *http.Request, token string, selected *ID) (Principal, bool) {
	// Body-token routes must not silently choose between two credentials.
	if token == "" || len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("X-SQLBOT-TOKEN")) != 0 || len(r.Header.Values("X-SQLBOT-ASSISTANT-TOKEN")) != 0 {
		failure(w, ErrUnauthenticated)
		return Principal{}, false
	}
	p, err := h.service.Identity.Verify(r.Context(), token)
	if err != nil || p.EmbeddedApp != "" {
		failure(w, ErrUnauthenticated)
		return Principal{}, false
	}
	if selected != nil {
		if !selected.Valid() || !p.User.canUse(*selected) {
			failure(w, ErrUnauthenticated)
			return Principal{}, false
		}
		p.Workspace = *selected
	}
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return Principal{}, false
	}
	return p, true
}

func (h *Handler) mcpWorkspaceList(w http.ResponseWriter, r *http.Request) {
	var token string // SQLBot posts a JSON string, not an object.
	if !readBody(w, r, &token) {
		return
	}
	p, ok := h.mcpBodyPrincipal(w, r, token, nil)
	if !ok {
		return
	}
	result := make([]map[string]any, 0, len(p.User.Memberships))
	for _, member := range p.User.Memberships {
		id, err := strconv.ParseInt(string(member.Workspace), 10, 64)
		if err != nil || id < 1 || id > maxSQLBotBrowserID {
			failure(w, ErrUnavailable)
			return
		}
		name := member.Name
		if name == "" {
			name = "Workspace " + string(member.Workspace)
		}
		result = append(result, map[string]any{"id": id, "name": name})
	}
	if _, err := h.service.Identity.Verify(r.Context(), token); err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) mcpDatasourceList(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
		OID   *ID    `json:"oid"`
	}
	if !readBody(w, r, &input) {
		return
	}
	p, ok := h.mcpBodyPrincipal(w, r, input.Token, input.OID)
	if !ok {
		return
	}
	items, err := h.service.Store.list(r.Context(), "binding", string(p.Workspace), string(p.User.ID))
	if err != nil {
		failure(w, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		var b Binding
		if json.Unmarshal(raw, &b) != nil || b.Workspace != p.Workspace || b.User != p.User.ID {
			failure(w, ErrUnavailable)
			return
		}
		if err := h.service.embeddedSourceAllowed(r.Context(), p, b.Datasource); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			failure(w, err)
			return
		}
		kind := b.Type
		if kind == "" {
			kind = b.Dialect
		}
		// Only public metadata. In particular, configuration may contain database
		// credentials and must never be exposed through the MCP listing route.
		result = append(result, map[string]any{"id": b.Datasource, "oid": b.Workspace,
			"name": b.Name, "type": kind, "description": b.Description, "status": "Success"})
	}
	sort.Slice(result, func(i, j int) bool { return result[i]["name"].(string) < result[j]["name"].(string) })
	if _, err := h.service.Identity.Verify(r.Context(), input.Token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// The upstream model-list shape carries only an oid. AgentSQL requires a live
// body token so a caller cannot enumerate models across workspaces. The
// response exposes model display metadata only, never endpoint or API key.
func (h *Handler) mcpModelList(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
		OID   *ID    `json:"oid"`
	}
	if !readBody(w, r, &input) {
		return
	}
	p, ok := h.mcpBodyPrincipal(w, r, input.Token, input.OID)
	if !ok {
		return
	}
	var model ModelConfig
	_, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &model)
	if errors.Is(err, ErrNotFound) {
		if err := h.mcpCurrentIdentity(r.Context(), p, input.Token); err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	if err != nil || validateModelConfig(model) != nil {
		failure(w, ErrUnavailable)
		return
	}
	if err := h.mcpCurrentIdentity(r.Context(), p, input.Token); err != nil {
		failure(w, err)
		return
	}
	name := model.Name
	if name == "" {
		name = model.Model
	}
	writeJSON(w, http.StatusOK, []map[string]any{{"id": legacyBrowserID(model.ID),
		"name": name, "default_model": true, "supplier": 0}})
}

func (h *Handler) mcpCurrentIdentity(ctx context.Context, p Principal, token string) error {
	current, err := h.service.Identity.Verify(ctx, token)
	if err != nil || current.EmbeddedApp != "" || current.User.ID != p.User.ID ||
		current.User.AuthRevision != p.User.AuthRevision || !current.User.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	return nil
}

func (h *Handler) mcpQuestion(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token      string          `json:"token"`
		Chat       ID              `json:"chat_id"`
		Question   string          `json:"question"`
		Datasource json.RawMessage `json:"datasource_id"`
		Custom     json.RawMessage `json:"custom_model"`
		Stream     *bool           `json:"stream"`
		ReturnImg  *bool           `json:"return_img"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if !input.Chat.Valid() || strings.TrimSpace(input.Question) == "" || len(input.Question) > 16000 ||
		len(input.Custom) != 0 && string(input.Custom) != "null" {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid MCP question or unsupported custom model")
		return
	}
	p, ok := h.mcpBodyPrincipal(w, r, input.Token, nil)
	if !ok {
		return
	}
	// A started MCP session may have chosen a different member workspace. Find
	// it only in the authenticated user's current memberships; the original
	// token remains the revocable parent of the session.
	var chat Chat
	found := false
	for _, member := range p.User.Memberships {
		candidate := p
		candidate.Workspace = member.Workspace
		current, _, err := h.service.chat(r.Context(), candidate, input.Chat)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			failure(w, err)
			return
		}
		p, chat, found = candidate, current, true
		break
	}
	if !found {
		failure(w, ErrNotFound)
		return
	}
	if err := h.mcpCurrentIdentity(r.Context(), p, input.Token); err != nil {
		failure(w, err)
		return
	}
	if chat.Origin != 1 {
		failure(w, ErrUnauthenticated)
		return
	}
	selected := chat.Datasource
	if len(input.Datasource) != 0 && string(input.Datasource) != "null" && string(input.Datasource) != `""` {
		var requested ID
		if json.Unmarshal(input.Datasource, &requested) != nil {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
			return
		}
		if selected != "" && selected != requested {
			failure(w, ErrConflict)
			return
		}
		selected = requested
	}
	if selected == "" {
		items, err := h.service.Store.list(r.Context(), "binding", string(p.Workspace), string(p.User.ID))
		if err != nil {
			failure(w, err)
			return
		}
		if len(items) != 1 {
			writeJSON(w, http.StatusUnprocessableEntity, "Select a datasource for this conversation")
			return
		}
		var only Binding
		if json.Unmarshal(items[0], &only) != nil || only.Workspace != p.Workspace || only.User != p.User.ID {
			failure(w, ErrUnavailable)
			return
		}
		selected = only.Datasource
	}
	if chat.Datasource == "" {
		if err := h.service.BindMCPSource(r.Context(), p, input.Chat, selected); err != nil {
			failure(w, err)
			return
		}
	}
	check := func(ctx context.Context) error { return h.mcpCurrentIdentity(ctx, p, input.Token) }
	record, err := h.service.Ask(r.Context(), p, input.Chat, input.Question, check, nil)
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
	if record.Chart != "" && (input.ReturnImg == nil || *input.ReturnImg) {
		var chart any
		if json.Unmarshal([]byte(record.Chart), &chart) == nil {
			result["chart"] = chart
		}
	}
	if input.Stream != nil && !*input.Stream {
		writeJSON(w, http.StatusOK, result)
		return
	}
	// SQLBot's MCP stream is Markdown over text/event-stream. Emit only after
	// the controlled query, audit, local commit and final identity check pass.
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "> Record ID: %s\n> %s\n\n```sql\n%s\n```\n\n", record.ID, input.Question, record.SQL)
	if record.Data != nil {
		publicData, _ := json.Marshal(result["data"])
		_, _ = w.Write(publicData)
		_, _ = w.Write([]byte("\n"))
	}
}
