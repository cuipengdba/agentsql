package chatbi

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func legacyBrowserID(id ID) any {
	value, err := strconv.ParseInt(string(id), 10, 64)
	if err != nil || value > maxSQLBotBrowserID {
		return string(id)
	}
	return value
}

func legacyUserView(user User, workspace ID) map[string]any {
	workspaces := make([]any, 0, len(user.Memberships))
	for _, member := range user.Memberships {
		workspaces = append(workspaces, legacyBrowserID(member.Workspace))
	}
	variables := make([]map[string]any, 0)
	for _, binding := range user.SystemVariables {
		if binding.Workspace == workspace {
			variables = append(variables, map[string]any{"variableId": binding.VariableID, "variableValues": binding.VariableValues})
		}
	}
	return map[string]any{
		"id": legacyBrowserID(user.ID), "account": user.Account, "name": user.Name,
		"email": user.Email, "oid": legacyBrowserID(user.Workspace), "oid_list": workspaces,
		"language": user.Language, "status": user.Status, "origin": user.Origin,
		"isAdmin": user.Admin, "create_time": user.Created,
		"system_variables": variables,
	}
}

func (h *Handler) legacyUserRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid user ID")
		return
	}
	user, _, err := h.service.Store.User(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyUserView(user, p.Workspace))
}

func (h *Handler) legacyUserPage(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	page, pageErr := strconv.Atoi(r.PathValue("page"))
	size, sizeErr := strconv.Atoi(r.PathValue("size"))
	if pageErr != nil || sizeErr != nil || page < 1 || page > 100000 || size < 1 || size > 100 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid pagination")
		return
	}
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("keyword")))
	if len(keyword) > 100 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid keyword")
		return
	}
	statusText := r.URL.Query().Get("status")
	status := -1
	if statusText != "" {
		parsed, err := strconv.Atoi(statusText)
		if err != nil || parsed != 0 && parsed != 1 {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid status filter")
			return
		}
		status = parsed
	}
	order := r.URL.Query().Get("order_by")
	if order != "" && order != "account" && order != "name" && order != "email" && order != "status" && order != "create_time" {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid sort field")
		return
	}
	descText := r.URL.Query().Get("desc")
	if descText != "" && descText != "true" && descText != "false" {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid sort direction")
		return
	}
	desc := descText == "true"
	var workspaceFilter map[ID]bool
	if values := r.URL.Query()["oidlist"]; len(values) != 0 {
		workspaceFilter = make(map[ID]bool, len(values))
		for _, value := range values {
			id := ID(value)
			if !id.Valid() {
				writeJSON(w, http.StatusUnprocessableEntity, "Invalid workspace filter")
				return
			}
			workspaceFilter[id] = true
		}
	}
	var originFilter map[int]bool
	if values := r.URL.Query()["origins"]; len(values) != 0 {
		originFilter = make(map[int]bool, len(values))
		for _, value := range values {
			origin, err := strconv.Atoi(value)
			if err != nil || origin < 0 || origin > 100 {
				writeJSON(w, http.StatusUnprocessableEntity, "Invalid origin filter")
				return
			}
			originFilter[origin] = true
		}
	}
	items, err := h.service.Store.list(r.Context(), "user", "system", "system")
	if err != nil {
		failure(w, err)
		return
	}
	users := make([]User, 0, len(items))
	for _, raw := range items {
		var user User
		if json.Unmarshal(raw, &user) != nil || !user.ID.Valid() || user.Account == "" {
			failure(w, ErrUnavailable)
			return
		}
		if user.ID == "1" || status >= 0 && user.Status != status {
			continue
		}
		if len(originFilter) != 0 && !originFilter[user.Origin] {
			continue
		}
		if len(workspaceFilter) != 0 {
			member := false
			for _, membership := range user.Memberships {
				if workspaceFilter[membership.Workspace] {
					member = true
					break
				}
			}
			if !member {
				continue
			}
		}
		if keyword != "" && !strings.Contains(strings.ToLower(user.Account), keyword) &&
			!strings.Contains(strings.ToLower(user.Name), keyword) && !strings.Contains(strings.ToLower(user.Email), keyword) {
			continue
		}
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool {
		left, right := users[i], users[j]
		if order == "status" && left.Status != right.Status {
			if desc {
				return left.Status > right.Status
			}
			return left.Status < right.Status
		}
		if order == "create_time" && left.Created != right.Created {
			if desc {
				return left.Created > right.Created
			}
			return left.Created < right.Created
		}
		var a, b string
		switch order {
		case "name":
			a, b = left.Name, right.Name
		case "email":
			a, b = left.Email, right.Email
		case "status":
			a, b = strconv.Itoa(left.Status), strconv.Itoa(right.Status)
		case "create_time":
			a, b = strconv.FormatInt(left.Created, 10), strconv.FormatInt(right.Created, 10)
		default:
			a, b = left.Account, right.Account
		}
		if a == b {
			return left.ID < right.ID
		}
		if desc {
			return a > b
		}
		return a < b
	})
	total := len(users)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	views := make([]map[string]any, 0, end-start)
	for _, user := range users[start:end] {
		views = append(views, legacyUserView(user, p.Workspace))
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"items": views, "total": total, "page": page, "size": size, "total_pages": (total + size - 1) / size})
}

func (h *Handler) legacyUserStatus(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID     ID   `json:"id"`
		Status *int `json:"status"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if !input.ID.Valid() || input.Status == nil || *input.Status != 0 && *input.Status != 1 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid user status")
		return
	}
	if input.ID == p.User.ID {
		writeJSON(w, http.StatusForbidden, "Cannot change own account status")
		return
	}
	user, revision, err := h.service.Store.User(r.Context(), input.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if user.Admin || user.AuthRevision == math.MaxInt64 {
		writeJSON(w, http.StatusForbidden, "Cannot change protected account status")
		return
	}
	if user.Status != *input.Status {
		if err := h.service.semanticAdmin(r.Context(), p); err != nil {
			failure(w, err)
			return
		}
		user.Status = *input.Status
		user.AuthRevision++
		user.AllowLegacyTokens = false
		if err := h.service.Store.writeSemanticWithDetails(r.Context(), userKey(user.ID), user, revision, "user.status.change", map[string]any{"actor": p.User.ID}); err != nil {
			failure(w, err)
			return
		}
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}
