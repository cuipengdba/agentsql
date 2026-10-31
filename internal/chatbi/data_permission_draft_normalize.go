package chatbi

import (
	"encoding/json"
	"strconv"
)

// normalizePermissionDrafts assigns stable server-side IDs to rule items.
// SQLBot's save response refers to these IDs in permission_list while its
// expanded read carries the editable items. Neither shape activates a rule.
func normalizePermissionDrafts(incoming []json.RawMessage, oldBody []byte, store *Store, key objectKey) ([]json.RawMessage, error) {
	prior := make(map[int64]bool)
	if len(oldBody) != 0 {
		var previous DataPermissionRule
		if store.decode(key, oldBody, &previous) != nil {
			return nil, ErrUnavailable
		}
		for _, raw := range previous.Permissions {
			if id, valid := permissionDraftID(raw); valid {
				prior[id] = true
			}
		}
	}
	used := make(map[int64]bool)
	result := make([]json.RawMessage, 0, len(incoming))
	for _, raw := range incoming {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			return nil, ErrInvalidDataPermission
		}
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil || (kind != "row" && kind != "column") {
			return nil, ErrInvalidDataPermission
		}
		for _, field := range []string{"ds_id", "table_id"} {
			id, valid := parseSystemVariableID(item[field])
			if !valid || id < 1 || id > maxSQLBotBrowserID {
				return nil, ErrInvalidDataPermission
			}
		}
		if kind == "row" {
			if !validDraftExpression(item["expression_tree"]) {
				return nil, ErrInvalidDataPermission
			}
		} else if rawFields, exists := item["permissions"]; exists {
			var serialized string
			if json.Unmarshal(rawFields, &serialized) == nil {
				var fields []json.RawMessage
				if json.Unmarshal([]byte(serialized), &fields) != nil {
					return nil, ErrInvalidDataPermission
				}
			} else {
				var fields []json.RawMessage
				if json.Unmarshal(rawFields, &fields) != nil {
					return nil, ErrInvalidDataPermission
				}
			}
		}
		id, valid := permissionDraftID(raw)
		if !valid || !prior[id] || used[id] {
			var created ID
			var err error
			for attempt := 0; attempt < 8; attempt++ {
				created, err = newID()
				if err != nil {
					return nil, err
				}
				id, _ = strconv.ParseInt(string(created), 10, 64)
				if !used[id] && !prior[id] {
					break
				}
			}
			if used[id] || prior[id] {
				return nil, ErrUnavailable
			}
		}
		used[id] = true
		encoded, _ := json.Marshal(id)
		item["id"] = encoded
		updated, err := json.Marshal(item)
		if err != nil || len(updated) > 8192 {
			return nil, ErrInvalidDataPermission
		}
		result = append(result, updated)
	}
	return result, nil
}

func validDraftExpression(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var expression string
	if json.Unmarshal(raw, &expression) == nil {
		raw = []byte(expression)
	}
	var tree map[string]json.RawMessage
	return len(raw) <= 8192 && json.Unmarshal(raw, &tree) == nil && tree != nil
}

func permissionDraftID(raw json.RawMessage) (int64, bool) {
	var item struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return 0, false
	}
	id, valid := parseSystemVariableID(item.ID)
	return id, valid && id > 0 && id <= maxSQLBotBrowserID
}
