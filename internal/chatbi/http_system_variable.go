package chatbi

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SystemVariable is workspace-scoped configuration. It has no query authority
// until the row-permission policy engine explicitly resolves it.
type SystemVariable struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	VarType   string          `json:"var_type"`
	Type      string          `json:"type"`
	Value     json.RawMessage `json:"value"`
	Created   time.Time       `json:"create_time"`
	CreatedBy ID              `json:"create_by"`
}

func validSystemVariable(variable SystemVariable) bool {
	if variable.ID < 0 || variable.ID > maxSQLBotBrowserID ||
		variable.Name == "" || len(variable.Name) > 128 || strings.TrimSpace(variable.Name) != variable.Name ||
		strings.ContainsAny(variable.Name, "\r\n\x00") || len(variable.Value) > 8192 ||
		(variable.VarType != "text" && variable.VarType != "number" && variable.VarType != "datetime") {
		return false
	}
	var values []json.RawMessage
	if json.Unmarshal(variable.Value, &values) != nil || len(values) == 0 || len(values) > 100 {
		return false
	}
	for _, raw := range values {
		if len(raw) > 512 || string(raw) == "null" {
			return false
		}
		switch variable.VarType {
		case "text", "datetime":
			var value string
			if json.Unmarshal(raw, &value) != nil || value == "" || len(value) > 500 {
				return false
			}
			if variable.VarType == "datetime" {
				if _, err := time.Parse(time.RFC3339, value); err != nil {
					if _, err = time.Parse("2006-01-02", value); err != nil {
						return false
					}
				}
			}
		case "number":
			if (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || !validFiniteNumber(string(raw)) {
				return false
			}
		}
	}
	return true
}

func validFiniteNumber(value string) bool {
	number, err := strconv.ParseFloat(value, 64)
	return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func systemVariableKey(workspace ID, id int64) objectKey {
	return objectKey{"system-variable", string(workspace), "workspace", strconv.FormatInt(id, 10)}
}

func parseSystemVariableID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	text := string(raw)
	if text[0] == '"' && json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	id, err := strconv.ParseInt(text, 10, 64)
	return id, err == nil && strconv.FormatInt(id, 10) == text
}

func (h *Handler) systemVariableList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.memberAdmin(r.Context(), p, p.Workspace, "grant"); err != nil {
		failure(w, err)
		return
	}
	var filter struct {
		Name string `json:"name"`
	}
	if r.ContentLength > 0 && !readBody(w, r, &filter) {
		return
	}
	if len(filter.Name) > 128 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	items, err := h.service.Store.SystemVariables(r.Context(), p.Workspace, filter.Name)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, items)
}

func (h *Handler) systemVariablePage(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	page, pageErr := strconv.Atoi(r.PathValue("page"))
	size, sizeErr := strconv.Atoi(r.PathValue("size"))
	if pageErr != nil || sizeErr != nil || page < 1 || size < 1 || size > 1000 || page > 100000 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	var filter struct {
		Name string `json:"name"`
	}
	if r.ContentLength > 0 && !readBody(w, r, &filter) {
		return
	}
	if len(filter.Name) > 128 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	items, err := h.service.Store.SystemVariables(r.Context(), p.Workspace, filter.Name)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	total := len(items)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	okay(w, map[string]any{"items": items[start:end], "page": page, "size": size, "total": total, "total_pages": (total + size - 1) / size})
}

func (h *Handler) systemVariableSave(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID      json.RawMessage `json:"id"`
		Name    string          `json:"name"`
		VarType string          `json:"var_type"`
		Value   json.RawMessage `json:"value"`
	}
	if !readBody(w, r, &input) {
		return
	}
	// The pinned original page sends a decimal string on edit, while callers
	// may send a JSON integer. Parse both without a float64 round trip.
	var id int64
	if len(input.ID) != 0 && string(input.ID) != "null" {
		parsed, valid := parseSystemVariableID(input.ID)
		if !valid {
			failure(w, ErrInvalidAccountChange)
			return
		}
		id = parsed
	}
	variable := SystemVariable{ID: id, Name: input.Name, VarType: input.VarType, Value: input.Value}
	if !validSystemVariable(variable) {
		failure(w, ErrInvalidAccountChange)
		return
	}
	if variable.ID == 0 {
		id, err := newID()
		if err != nil {
			failure(w, err)
			return
		}
		variable.ID, _ = strconv.ParseInt(string(id), 10, 64)
		variable.Created = time.Now().UTC()
		variable.CreatedBy = p.User.ID
	} else {
		variable.Created = time.Time{}
		variable.CreatedBy = ""
	}
	variable.Type = "custom"
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.SaveSystemVariable(r.Context(), p, variable); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (h *Handler) systemVariableDelete(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input []json.RawMessage
	if !readBody(w, r, &input) {
		return
	}
	if len(input) == 0 || len(input) > 100 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	ids := make([]int64, 0, len(input))
	for _, raw := range input {
		id, valid := parseSystemVariableID(raw)
		if !valid {
			failure(w, ErrInvalidAccountChange)
			return
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id < 1 || id > maxSQLBotBrowserID || i > 0 && id == ids[i-1] {
			failure(w, ErrInvalidAccountChange)
			return
		}
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.DeleteSystemVariables(r.Context(), p, ids); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
