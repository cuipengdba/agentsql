package chatbi

import (
	"context"
	"encoding/json"
	"time"
)

// These are server-held bindings. The row-policy engine resolves their current
// definition and user snapshot before using a value.
func (s *Store) validateUserVariableBindings(ctx context.Context, workspace ID, raw []json.RawMessage) ([]UserVariableBinding, error) {
	if !workspace.Valid() || len(raw) > 32 {
		return nil, ErrInvalidAccountChange
	}
	if len(raw) == 0 {
		return nil, nil
	}
	definitions, err := s.SystemVariables(ctx, workspace, "")
	if err != nil {
		return nil, err
	}
	available := make(map[int64]SystemVariable, len(definitions))
	for _, definition := range definitions {
		available[definition.ID] = definition
	}
	seen := map[int64]bool{}
	bindings := make([]UserVariableBinding, 0, len(raw))
	for _, item := range raw {
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || len(fields) != 2 {
			return nil, ErrInvalidAccountChange
		}
		id, valid := parseSystemVariableID(fields["variableId"])
		definition, exists := available[id]
		if !valid || !exists || seen[id] {
			return nil, ErrInvalidAccountChange
		}
		var values []json.RawMessage
		if json.Unmarshal(fields["variableValues"], &values) != nil || len(values) == 0 || len(values) > 100 ||
			!validUserVariableValues(definition, values) {
			return nil, ErrInvalidAccountChange
		}
		seen[id] = true
		bindings = append(bindings, UserVariableBinding{Workspace: workspace, VariableID: id, VariableValues: values})
	}
	return bindings, nil
}

func validUserVariableValues(definition SystemVariable, values []json.RawMessage) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if len(value) == 0 || len(value) > 512 || string(value) == "null" || seen[string(value)] {
			return false
		}
		seen[string(value)] = true
	}
	switch definition.VarType {
	case "text", "kv":
		var allowed []string
		if definition.VarType == "text" {
			if json.Unmarshal(definition.Value, &allowed) != nil {
				return false
			}
		} else {
			var pairs []struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(definition.Value, &pairs) != nil {
				return false
			}
			for _, pair := range pairs {
				allowed = append(allowed, pair.Value)
			}
		}
		for _, raw := range values {
			var selected string
			if json.Unmarshal(raw, &selected) != nil || selected == "" || !containsVariableValue(allowed, selected) {
				return false
			}
		}
	case "number":
		if len(values) != 1 || len(values[0]) == 0 || values[0][0] == '"' {
			return false
		}
		var bounds []float64
		if json.Unmarshal(definition.Value, &bounds) != nil || len(bounds) != 2 {
			return false
		}
		selected, valid := finiteNumber(string(values[0]))
		return valid && selected >= bounds[0] && selected <= bounds[1]
	case "datetime":
		if len(values) != 1 {
			return false
		}
		var bounds []string
		var selected string
		if json.Unmarshal(definition.Value, &bounds) != nil || len(bounds) != 2 ||
			json.Unmarshal(values[0], &selected) != nil {
			return false
		}
		start, startOK := parseUserVariableDate(bounds[0])
		end, endOK := parseUserVariableDate(bounds[1])
		date, dateOK := parseUserVariableDate(selected)
		return startOK && endOK && dateOK && !date.Before(start) && !date.After(end)
	default:
		return false
	}
	return true
}

func parseUserVariableDate(value string) (time.Time, bool) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		date, err = time.Parse(time.RFC3339, value)
	}
	return date, err == nil
}

func containsVariableValue(allowed []string, value string) bool {
	for _, item := range allowed {
		if item == value {
			return true
		}
	}
	return false
}

func replaceUserVariableBindings(current []UserVariableBinding, workspace ID, assigned []UserVariableBinding, memberships []Membership) []UserVariableBinding {
	active := make(map[ID]bool, len(memberships))
	for _, membership := range memberships {
		active[membership.Workspace] = true
	}
	result := make([]UserVariableBinding, 0, len(current)+len(assigned))
	for _, binding := range current {
		if binding.Workspace != workspace && active[binding.Workspace] {
			result = append(result, binding)
		}
	}
	return append(result, assigned...)
}

func retainUserVariableBindings(current []UserVariableBinding, memberships []Membership) []UserVariableBinding {
	active := make(map[ID]bool, len(memberships))
	for _, membership := range memberships {
		active[membership.Workspace] = true
	}
	result := make([]UserVariableBinding, 0, len(current))
	for _, binding := range current {
		if active[binding.Workspace] {
			result = append(result, binding)
		}
	}
	return result
}
