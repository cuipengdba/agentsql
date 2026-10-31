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

// applicableRowPermissions selects only current-user, current-datasource
// items. An active column rule remains unsupported and closes before any
// gateway call. A malformed active item also closes; disabled drafts cannot
// affect data. Caller must compile every returned item against live schema.
func applicableRowPermissions(rules []DataPermissionRule, principal Principal, binding Binding) ([]json.RawMessage, error) {
	if !principal.Workspace.Valid() || principal.Workspace != binding.Workspace || !binding.Datasource.Valid() {
		return nil, ErrGateway
	}
	items := make([]json.RawMessage, 0)
	for _, rule := range rules {
		if !rule.Enable || len(rule.Permissions) == 0 {
			continue
		}
		for _, raw := range rule.Permissions {
			if !supportedDataPermissionTarget(raw) {
				return nil, ErrGateway
			}
		}
		userID, err := strconv.ParseInt(string(principal.User.ID), 10, 64)
		if err != nil || userID < 1 {
			return nil, ErrGateway
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
				return nil, ErrGateway
			}
			dsID, valid := parseSystemVariableID(item.Datasource)
			if !valid || dsID < 1 || dsID > maxSQLBotBrowserID {
				return nil, ErrGateway
			}
			if strconv.FormatInt(dsID, 10) != string(binding.Datasource) {
				continue
			}
			if item.Type != "row" {
				return nil, ErrGateway
			}
			items = append(items, raw)
			if len(items) > 16 {
				return nil, ErrGateway
			}
		}
	}
	if len(items) != 0 && binding.Dialect != "pg" {
		return nil, ErrGateway
	}
	return items, nil
}

func compileApplicableRowPermissions(items []json.RawMessage, datasource ID, columns []Column) (string, string, []string, error) {
	var schema, table string
	filters := make([]string, 0, len(items))
	for _, item := range items {
		currentSchema, currentTable, predicate, err := compileSQLBotRowPermission(item, datasource, columns)
		if err != nil || schema != "" && (schema != currentSchema || table != currentTable) {
			return "", "", nil, ErrGateway
		}
		schema, table = currentSchema, currentTable
		filters = append(filters, predicate)
	}
	return schema, table, filters, nil
}
