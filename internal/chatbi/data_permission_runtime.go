package chatbi

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
)

// dataPermissionSnapshot is a local control-plane snapshot. Its rule items
// must be checked again after gateway work; a policy changed during remote
// execution cannot release a result calculated under a stale rule set.
func (service *Service) dataPermissionSnapshot(ctx context.Context, workspace ID) ([]DataPermissionRule, error) {
	if service == nil || service.Store == nil {
		return nil, ErrGateway
	}
	rules, err := service.Store.DataPermissionRules(ctx, workspace)
	if err != nil {
		return nil, ErrGateway
	}
	return rules, nil
}

func (service *Service) dataPermissionUnchanged(ctx context.Context, workspace ID, prior []DataPermissionRule) error {
	current, err := service.dataPermissionSnapshot(ctx, workspace)
	if err != nil || !sameDataPermissionSnapshot(prior, current) {
		return ErrGateway
	}
	return nil
}

func sameDataPermissionSnapshot(left, right []DataPermissionRule) bool {
	return reflect.DeepEqual(left, right)
}

// applicableDataPermissions selects only current-user, current-datasource
// items. A malformed active item closes; disabled drafts cannot affect data.
// Caller must compile every returned item against live gateway schema.
func applicableDataPermissions(rules []DataPermissionRule, principal Principal, binding Binding) ([]json.RawMessage, []json.RawMessage, error) {
	if !principal.Workspace.Valid() || principal.Workspace != binding.Workspace || !binding.Datasource.Valid() {
		return nil, nil, ErrGateway
	}
	rows := make([]json.RawMessage, 0)
	columns := make([]json.RawMessage, 0)
	for _, rule := range rules {
		if !rule.Enable || len(rule.Permissions) == 0 {
			continue
		}
		for _, raw := range rule.Permissions {
			if !supportedDataPermissionTarget(raw) {
				return nil, nil, ErrGateway
			}
		}
		userID, err := strconv.ParseInt(string(principal.User.ID), 10, 64)
		if err != nil || userID < 1 {
			return nil, nil, ErrGateway
		}
		targeted := false
		for _, id := range rule.Users {
			if id == userID {
				targeted = true
				break
			}
		}
		if !targeted {
			continue
		}
		for _, raw := range rule.Permissions {
			var item struct {
				Type       string          `json:"type"`
				Datasource json.RawMessage `json:"ds_id"`
			}
			if json.Unmarshal(raw, &item) != nil || (item.Type != "row" && item.Type != "column") {
				return nil, nil, ErrGateway
			}
			dsID, valid := parseSystemVariableID(item.Datasource)
			if !valid || dsID < 1 || dsID > maxSQLBotBrowserID {
				return nil, nil, ErrGateway
			}
			if strconv.FormatInt(dsID, 10) != string(binding.Datasource) {
				continue
			}
			if item.Type == "row" {
				rows = append(rows, raw)
			} else {
				columns = append(columns, raw)
			}
			if len(rows)+len(columns) > 16 {
				return nil, nil, ErrGateway
			}
		}
	}
	if len(rows) != 0 && binding.Dialect != "pg" && binding.Dialect != "mysql" ||
		len(columns) != 0 && binding.Dialect != "pg" && binding.Dialect != "mysql" {
		return nil, nil, ErrGateway
	}
	return rows, columns, nil
}

func compileApplicableColumnPermissions(items []json.RawMessage, datasource ID, columns []Column) (map[string]bool, error) {
	blocked := make(map[string]bool)
	for _, item := range items {
		fields, err := compileSQLBotColumnPermission(item, datasource, columns)
		if err != nil {
			return nil, ErrGateway
		}
		for key := range fields {
			blocked[key] = true
		}
	}
	return blocked, nil
}

func bindingWithPermissionColumns(binding Binding, blocked map[string]bool) (Binding, error) {
	if len(blocked) == 0 {
		return binding, nil
	}
	existing, err := disabledFields(binding)
	if err != nil {
		return Binding{}, ErrGateway
	}
	merged := make(map[string]bool, len(existing)+len(blocked))
	for key := range existing {
		merged[key] = true
	}
	for key := range blocked {
		merged[key] = true
	}
	if len(merged) > 4096 {
		return Binding{}, ErrGateway
	}
	encoded, err := json.Marshal(merged)
	if err != nil || len(encoded) > 2<<20 {
		return Binding{}, ErrGateway
	}
	binding.DisabledFields = string(encoded)
	return binding, nil
}

func compileApplicableRowPermissions(items []json.RawMessage, datasource ID, columns []Column, dialect string) (string, string, []string, error) {
	return compileApplicableRowPermissionsWithVariables(items, datasource, columns, dialect, nil)
}

func compileApplicableRowPermissionsWithVariables(items []json.RawMessage, datasource ID, columns []Column, dialect string, resolve func(int64) (string, bool)) (string, string, []string, error) {
	var schema, table string
	filters := make([]string, 0, len(items))
	for _, item := range items {
		currentSchema, currentTable, predicate, err := compileSQLBotRowPermissionWithVariables(item, datasource, columns, dialect, resolve)
		if err != nil || schema != "" && (schema != currentSchema || table != currentTable) {
			return "", "", nil, ErrGateway
		}
		schema, table = currentSchema, currentTable
		filters = append(filters, predicate)
	}
	return schema, table, filters, nil
}
