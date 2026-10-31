package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DataPermissionRule is an encrypted workspace control-plane record. Only a
// bounded PostgreSQL row-rule subset may be activated after live validation;
// unsupported active rules fail closed on the ChatBI data paths.
type DataPermissionRule struct {
	ID          int64             `json:"id"`
	OID         int64             `json:"oid"`
	Enable      bool              `json:"enable"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Permissions []json.RawMessage `json:"permission_list"`
	Users       []int64           `json:"user_list"`
	WhiteList   string            `json:"white_list_user"`
	Created     time.Time         `json:"create_time"`
}

var ErrInvalidDataPermission = errors.New("invalid or unsupported data permission rule")

func permissionRuleKey(workspace ID, id int64) objectKey {
	return objectKey{"data-permission-rule", string(workspace), "workspace", strconv.FormatInt(id, 10)}
}

func validPermissionRule(rule DataPermissionRule) bool {
	if rule.ID < 1 || rule.ID > maxSQLBotBrowserID || rule.OID < 1 || rule.OID > maxSQLBotBrowserID ||
		rule.Name == "" || len(rule.Name) > 128 || !utf8.ValidString(rule.Name) ||
		strings.TrimSpace(rule.Name) != rule.Name || strings.ContainsAny(rule.Name, "\r\n\x00") ||
		len(rule.Description) > 512 || !utf8.ValidString(rule.Description) || strings.ContainsRune(rule.Description, 0) ||
		rule.WhiteList != "[]" || len(rule.Permissions) > 32 || len(rule.Users) > 100 || rule.Created.IsZero() {
		return false
	}
	seen := make(map[int64]bool, len(rule.Users))
	for _, id := range rule.Users {
		if id < 1 || id > maxSQLBotBrowserID || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, item := range rule.Permissions {
		if len(item) == 0 || len(item) > 8192 || item[0] != '{' || !json.Valid(item) {
			return false
		}
	}
	return true
}

func (rule DataPermissionRule) browserResponse(expanded bool) map[string]any {
	ids := make([]int64, 0, len(rule.Permissions))
	items := make([]map[string]any, 0, len(rule.Permissions))
	for index, raw := range rule.Permissions {
		var item map[string]any
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var identity struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(raw, &identity)
		id, valid := parseSystemVariableID(identity.ID)
		if !valid || id < 1 || id > maxSQLBotBrowserID {
			id = browserFieldID(rule.ID, "permission:"+strconv.Itoa(index))
		}
		ids = append(ids, id)
		item["id"] = id
		if _, exists := item["enable"]; !exists {
			item["enable"] = true
		}
		if _, exists := item["white_list_user"]; !exists {
			item["white_list_user"] = "[]"
		}
		if _, exists := item["create_time"]; !exists {
			item["create_time"] = rule.Created
		}
		if _, exists := item["auth_target_id"]; !exists {
			item["auth_target_id"] = nil
		}
		if _, exists := item["auth_target_type"]; !exists {
			item["auth_target_type"] = "user"
		}
		if _, exists := item["ds_name"]; !exists {
			item["ds_name"] = ""
		}
		if _, exists := item["table_name"]; !exists {
			item["table_name"] = ""
		}
		if item["type"] == "row" {
			var tree any = map[string]any{}
			switch expression := item["expression_tree"].(type) {
			case string:
				_ = json.Unmarshal([]byte(expression), &tree)
			case map[string]any:
				tree = expression
				serialized, _ := json.Marshal(expression)
				item["expression_tree"] = string(serialized)
			}
			item["tree"] = tree
			item["permissions"] = "[]"
			item["permission_list"] = []any{}
		} else if item["type"] == "column" {
			var fields []any
			if value, ok := item["permissions"].(string); ok {
				_ = json.Unmarshal([]byte(value), &fields)
			} else if value, ok := item["permissions"].([]any); ok {
				fields = value
				serialized, _ := json.Marshal(value)
				item["permissions"] = string(serialized)
			}
			if fields == nil {
				fields = []any{}
			}
			item["permission_list"] = fields
			item["tree"] = map[string]any{}
		}
		items = append(items, item)
	}
	result := map[string]any{
		"id": rule.ID, "oid": rule.OID, "enable": rule.Enable, "name": rule.Name,
		"description": rule.Description, "permission_list": ids,
		"user_list": rule.Users, "white_list_user": rule.WhiteList,
		"create_time": rule.Created,
	}
	if expanded {
		result["permissions"] = items
		result["users"] = rule.Users
	}
	return result
}

func (s *Store) DataPermissionRules(ctx context.Context, workspace ID) ([]DataPermissionRule, error) {
	if s == nil || !workspace.Valid() {
		return nil, ErrNotFound
	}
	raw, err := s.list(ctx, "data-permission-rule", string(workspace), "workspace")
	if err != nil || len(raw) > 256 {
		return nil, ErrUnavailable
	}
	rules := make([]DataPermissionRule, 0, len(raw))
	for _, item := range raw {
		var rule DataPermissionRule
		if json.Unmarshal(item, &rule) != nil || !validPermissionRule(rule) || strconv.FormatInt(rule.OID, 10) != string(workspace) {
			return nil, ErrUnavailable
		}
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	return rules, nil
}

func (s *Store) DataPermissionRule(ctx context.Context, workspace ID, id int64) (DataPermissionRule, error) {
	if s == nil || !workspace.Valid() || id < 1 || id > maxSQLBotBrowserID {
		return DataPermissionRule{}, ErrNotFound
	}
	var rule DataPermissionRule
	_, err := s.get(ctx, permissionRuleKey(workspace, id), &rule)
	if err != nil {
		return DataPermissionRule{}, err
	}
	if !validPermissionRule(rule) || rule.ID != id || strconv.FormatInt(rule.OID, 10) != string(workspace) {
		return DataPermissionRule{}, ErrUnavailable
	}
	return rule, nil
}

func (h *Handler) dataPermissionList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	rules, err := h.service.Store.DataPermissionRules(r.Context(), p.Workspace)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	result := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		result = append(result, rule.browserResponse(true))
	}
	okay(w, result)
}

func (h *Handler) dataPermissionGet(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 || id > maxSQLBotBrowserID || strconv.FormatInt(id, 10) != r.PathValue("id") {
		failure(w, ErrInvalidDataPermission)
		return
	}
	rule, err := h.service.Store.DataPermissionRule(r.Context(), p.Workspace, id)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, rule.browserResponse(true))
}

func (h *Handler) dataPermissionSave(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID          json.RawMessage   `json:"id"`
		OID         json.RawMessage   `json:"oid"`
		Enable      *bool             `json:"enable"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Permissions []json.RawMessage `json:"permissions"`
		Users       []json.RawMessage `json:"users"`
		WhiteList   string            `json:"white_list_user"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if len(input.Users) > 100 {
		failure(w, ErrInvalidDataPermission)
		return
	}
	users := make([]int64, 0, len(input.Users))
	for _, raw := range input.Users {
		id, valid := parseSystemVariableID(raw)
		if !valid || id < 1 || id > maxSQLBotBrowserID {
			failure(w, ErrInvalidDataPermission)
			return
		}
		users = append(users, id)
	}
	if len(input.OID) > 0 && string(input.OID) != "null" {
		oid, valid := parseSystemVariableID(input.OID)
		if !valid || strconv.FormatInt(oid, 10) != string(p.Workspace) {
			failure(w, ErrInvalidDataPermission)
			return
		}
	}
	id := int64(0)
	if len(input.ID) > 0 && string(input.ID) != "null" && string(input.ID) != `""` {
		var valid bool
		id, valid = parseSystemVariableID(input.ID)
		if !valid || id < 1 || id > maxSQLBotBrowserID {
			failure(w, ErrInvalidDataPermission)
			return
		}
	}
	created := id == 0
	if created {
		fresh, err := newID()
		if err != nil {
			failure(w, err)
			return
		}
		id, _ = strconv.ParseInt(string(fresh), 10, 64)
	}
	oid, _ := strconv.ParseInt(string(p.Workspace), 10, 64)
	enabled := true
	if input.Enable != nil {
		enabled = *input.Enable
	}
	whiteList := input.WhiteList
	if whiteList == "" {
		whiteList = "[]"
	}
	rule := DataPermissionRule{ID: id, OID: oid, Enable: enabled,
		Name: input.Name, Description: input.Description, Permissions: input.Permissions,
		Users: users, WhiteList: whiteList, Created: time.Now().UTC()}
	if rule.Permissions == nil {
		rule.Permissions = []json.RawMessage{}
	}
	if rule.Users == nil {
		rule.Users = []int64{}
	}
	if !validPermissionRule(rule) {
		failure(w, ErrInvalidDataPermission)
		return
	}
	for _, target := range rule.Users {
		user, _, err := h.service.Store.User(r.Context(), ID(strconv.FormatInt(target, 10)))
		if err != nil || !user.canUse(p.Workspace) {
			failure(w, ErrInvalidDataPermission)
			return
		}
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	proof, err := h.validateDataPermissionActivation(r.Context(), p, rule)
	if err != nil {
		failure(w, err)
		return
	}
	saved, err := h.service.Store.SaveDataPermissionRule(r.Context(), p, rule, created, proof)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, saved.browserResponse(false))
}

func (h *Handler) dataPermissionDelete(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 || id > maxSQLBotBrowserID || strconv.FormatInt(id, 10) != r.PathValue("id") {
		failure(w, ErrInvalidDataPermission)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.DeleteDataPermissionRule(r.Context(), p, id); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}
