package chatbi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
)

// The original rule schema permits role and explicit-target forms that this
// compatibility layer cannot currently enforce. An active unsupported target
// must never turn into a no-op for the affected workspace.
func supportedDataPermissionTarget(raw json.RawMessage) bool {
	var target struct {
		Type      *string         `json:"auth_target_type"`
		ID        json.RawMessage `json:"auth_target_id"`
		WhiteList *string         `json:"white_list_user"`
	}
	if json.Unmarshal(raw, &target) != nil || target.Type != nil && *target.Type != "user" ||
		len(target.ID) != 0 && string(target.ID) != "null" ||
		target.WhiteList != nil && *target.WhiteList != "[]" {
		return false
	}
	return true
}

type dataPermissionActivation struct {
	workspace ID
	ruleID    int64
	digest    [32]byte
}

func activationDigest(rule DataPermissionRule) ([32]byte, error) {
	encoded, err := json.Marshal(struct {
		ID          int64             `json:"id"`
		OID         int64             `json:"oid"`
		Enable      bool              `json:"enable"`
		Permissions []json.RawMessage `json:"permissions"`
		Users       []int64           `json:"users"`
	}{rule.ID, rule.OID, rule.Enable, rule.Permissions, rule.Users})
	if err != nil {
		return [32]byte{}, ErrInvalidDataPermission
	}
	return sha256.Sum256(encoded), nil
}

func (proof *dataPermissionActivation) matches(workspace ID, rule DataPermissionRule) bool {
	if proof == nil || proof.workspace != workspace || proof.ruleID != rule.ID {
		return false
	}
	digest, err := activationDigest(rule)
	return err == nil && proof.digest == digest
}

// validateDataPermissionActivation validates every item before a rule group can
// become active. The live schema is only a preflight; each read still compiles
// against the requesting user's current gateway schema and fails closed if it
// changes. Unsupported expression or target forms remain rejected.
func (h *Handler) validateDataPermissionActivation(ctx context.Context, principal Principal, rule DataPermissionRule) (*dataPermissionActivation, error) {
	if !rule.Enable || len(rule.Permissions) == 0 {
		return nil, nil
	}
	if len(rule.Permissions) > 16 {
		return nil, ErrInvalidDataPermission
	}
	type relation struct{ schema, table string }
	tables := make(map[ID]relation)
	for _, raw := range rule.Permissions {
		if !supportedDataPermissionTarget(raw) {
			return nil, ErrInvalidDataPermission
		}
		var item struct {
			Type       string          `json:"type"`
			Datasource json.RawMessage `json:"ds_id"`
		}
		if json.Unmarshal(raw, &item) != nil || (item.Type != "row" && item.Type != "column") {
			return nil, ErrInvalidDataPermission
		}
		dsID, valid := parseSystemVariableID(item.Datasource)
		if !valid || dsID < 1 || dsID > maxSQLBotBrowserID {
			return nil, ErrInvalidDataPermission
		}
		datasource := ID(strconv.FormatInt(dsID, 10))
		binding, revision, digest, err := h.service.bindingSnapshot(ctx, principal, datasource)
		if err != nil {
			return nil, ErrInvalidDataPermission
		}
		if item.Type == "row" && binding.Dialect != "pg" && binding.Dialect != "mysql" ||
			item.Type == "column" && binding.Dialect != "pg" && binding.Dialect != "mysql" {
			return nil, ErrInvalidDataPermission
		}
		columns, err := h.service.gatewaySchema(ctx, principal, binding)
		if err != nil {
			return nil, err
		}
		if item.Type == "row" {
			referenced := make(map[int64]bool)
			schema, table, _, err := compileSQLBotRowPermissionWithVariables(raw, datasource, columns, binding.Dialect,
				func(id int64) (string, bool) {
					referenced[id] = true
					return h.service.rowVariableDefinitionSample(ctx, principal.Workspace, id)
				})
			prior, exists := tables[datasource]
			if err != nil || exists && prior != (relation{schema, table}) {
				return nil, ErrInvalidDataPermission
			}
			if len(referenced) != 0 {
				for _, targetID := range rule.Users {
					user, _, err := h.service.Store.User(ctx, ID(strconv.FormatInt(targetID, 10)))
					if err != nil || !user.canUse(principal.Workspace) {
						return nil, ErrInvalidDataPermission
					}
					variables := h.service.newRowVariableSnapshot(ctx, Principal{User: user, Workspace: principal.Workspace})
					actualSchema, actualTable, _, err := compileSQLBotRowPermissionWithVariables(raw, datasource, columns, binding.Dialect, variables.resolve)
					if err != nil || actualSchema != schema || actualTable != table || variables.unchanged(ctx) != nil {
						return nil, ErrInvalidDataPermission
					}
				}
			}
			tables[datasource] = relation{schema, table}
		} else if _, err := compileSQLBotColumnPermission(raw, datasource, columns); err != nil {
			return nil, ErrInvalidDataPermission
		}
		if err := h.service.bindingUnchanged(ctx, principal, binding, revision, digest); err != nil {
			return nil, err
		}
	}
	digest, err := activationDigest(rule)
	if err != nil {
		return nil, err
	}
	return &dataPermissionActivation{workspace: principal.Workspace, ruleID: rule.ID, digest: digest}, nil
}
