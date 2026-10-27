package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func (service *Service) requireWorkspace(ctx context.Context, space ID) error {
	options, err := service.allWorkspaceOptions(ctx)
	if err != nil {
		return err
	}
	for _, option := range options {
		if option.ID == space {
			return nil
		}
	}
	return ErrNotFound
}

type memberPage struct {
	Page    int
	Size    int
	Keyword string
	Space   ID
}

func parseMemberPage(r *http.Request, fallback ID, candidate bool) (memberPage, bool) {
	page, e1 := strconv.Atoi(r.PathValue("page"))
	size, e2 := strconv.Atoi(r.PathValue("size"))
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("keyword")))
	space := fallback
	if value := r.URL.Query().Get("oid"); value != "" {
		space = ID(value)
	}
	legacyAllCandidates := candidate && page == 1 && size == 1000000
	if e1 != nil || e2 != nil || page < 1 || page > 100000 || size < 1 || size > 100 && !legacyAllCandidates || len(keyword) > 100 || !space.Valid() {
		return memberPage{}, false
	}
	return memberPage{Page: page, Size: size, Keyword: keyword, Space: space}, true
}

func memberPageEnvelope(items []map[string]any, page, size, total int) map[string]any {
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return map[string]any{"items": items[start:end], "total": total, "page": page, "size": size,
		"total_pages": (total + size - 1) / size}
}

func (h *Handler) memberUsers(r *http.Request, space ID, keyword string, candidates bool) ([]map[string]any, error) {
	raw, err := h.service.Store.list(r.Context(), "user", "system", "system")
	if err != nil {
		return nil, err
	}
	type entry struct {
		user User
		view map[string]any
	}
	selected := make([]entry, 0, len(raw))
	for _, body := range raw {
		var user User
		if json.Unmarshal(body, &user) != nil || !user.ID.Valid() || user.Account == "" {
			return nil, ErrUnavailable
		}
		if user.ID == "1" || keyword != "" && !strings.Contains(strings.ToLower(user.Account), keyword) &&
			!strings.Contains(strings.ToLower(user.Name), keyword) && !strings.Contains(strings.ToLower(user.Email), keyword) {
			continue
		}
		member := false
		weight := 0
		for _, membership := range user.Memberships {
			if membership.Workspace == space {
				member, weight = true, membership.Weight
				break
			}
		}
		if member == candidates {
			continue
		}
		var view map[string]any
		if candidates {
			view = map[string]any{"id": legacyBrowserID(user.ID), "account": user.Account, "name": user.Name}
		} else {
			view = legacyUserView(user)
			view["weight"] = weight
		}
		selected = append(selected, entry{user: user, view: view})
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].user.Account == selected[j].user.Account {
			return selected[i].user.ID < selected[j].user.ID
		}
		return selected[i].user.Account < selected[j].user.Account
	})
	views := make([]map[string]any, 0, len(selected))
	for _, item := range selected {
		views = append(views, item.view)
	}
	return views, nil
}

func (h *Handler) legacyMemberPage(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	query, valid := parseMemberPage(r, p.Workspace, false)
	if !valid {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid member page")
		return
	}
	if err := h.service.memberAdmin(r.Context(), p, query.Space, "read"); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.requireWorkspace(r.Context(), query.Space); err != nil {
		failure(w, err)
		return
	}
	items, err := h.memberUsers(r, query.Space, query.Keyword, false)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, memberPageEnvelope(items, query.Page, query.Size, len(items)))
}

func (h *Handler) legacyMemberCandidatePage(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	query, valid := parseMemberPage(r, p.Workspace, true)
	if !valid {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid member candidate page")
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.requireWorkspace(r.Context(), query.Space); err != nil {
		failure(w, err)
		return
	}
	items, err := h.memberUsers(r, query.Space, query.Keyword, true)
	if err != nil {
		failure(w, err)
		return
	}
	// The original dialog asks for one million candidates as a sentinel for
	// "all". Bound the response instead of materializing an unbounded browser
	// payload; larger deployments must use the paged AgentSQL management UI.
	if query.Size == 1000000 && len(items) > 10000 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, memberPageEnvelope(items, query.Page, query.Size, len(items)))
}

func (h *Handler) legacyMemberCandidate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("keyword")))
	if keyword == "" || len(keyword) > 100 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid candidate keyword")
		return
	}
	if err := h.service.memberAdmin(r.Context(), p, p.Workspace, "read"); err != nil {
		failure(w, err)
		return
	}
	items, err := h.memberUsers(r, p.Workspace, "", true)
	if err != nil {
		failure(w, err)
		return
	}
	for _, item := range items {
		if strings.EqualFold(item["account"].(string), keyword) || strings.EqualFold(item["name"].(string), keyword) {
			if err := h.currentIdentity(r.Context(), p, token); err != nil {
				failure(w, err)
				return
			}
			okay(w, item)
			return
		}
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}
