package chatbi

import (
	"context"
	"encoding/json"
)

// rowVariableSnapshot resolves only the variables named by an active row rule.
// Its revisions are checked after gateway work so changing a definition or a
// user's binding while a query runs cannot release a stale result.
type rowVariableSnapshot struct {
	service      *Service
	principal    Principal
	ctx          context.Context
	states       map[int64]rowVariableState
	values       map[int64][]string
	userRevision int64
	userDigest   [32]byte
}

type rowVariableState struct {
	revision int64
	digest   [32]byte
}

func (service *Service) newRowVariableSnapshot(ctx context.Context, principal Principal) *rowVariableSnapshot {
	return &rowVariableSnapshot{service: service, principal: principal, ctx: ctx,
		states: make(map[int64]rowVariableState), values: make(map[int64][]string)}
}

func (snapshot *rowVariableSnapshot) resolve(id int64) ([]string, bool) {
	if value, exists := snapshot.values[id]; exists {
		return value, true
	}
	if snapshot.service.live(snapshot.ctx, snapshot.principal) != nil {
		return nil, false
	}
	var current User
	userRevision, userDigest, err := snapshot.service.Store.getWithDigest(snapshot.ctx, userKey(snapshot.principal.User.ID), &current)
	if err != nil || current.AuthRevision != snapshot.principal.User.AuthRevision || !current.canUse(snapshot.principal.Workspace) ||
		(snapshot.userRevision != 0 && (snapshot.userRevision != userRevision || snapshot.userDigest != userDigest)) {
		return nil, false
	}
	var definition SystemVariable
	revision, digest, err := snapshot.service.Store.getWithDigest(snapshot.ctx, systemVariableKey(snapshot.principal.Workspace, id), &definition)
	if err != nil || revision < 1 || definition.ID != id || definition.Type != "custom" || !validSystemVariable(definition) {
		return nil, false
	}
	var selected []json.RawMessage
	for _, binding := range current.SystemVariables {
		if binding.Workspace == snapshot.principal.Workspace && binding.VariableID == id {
			if selected != nil {
				return nil, false
			}
			selected = binding.VariableValues
		}
	}
	if len(selected) == 0 || !validUserVariableValues(definition, selected) {
		return nil, false
	}
	values := make([]string, 0, len(selected))
	for _, raw := range selected {
		value, valid := rowVariableValue(definition.VarType, raw)
		if !valid {
			return nil, false
		}
		values = append(values, value)
	}
	snapshot.states[id] = rowVariableState{revision: revision, digest: digest}
	snapshot.values[id] = values
	snapshot.userRevision, snapshot.userDigest = userRevision, userDigest
	return values, true
}

func rowVariableValue(kind string, raw json.RawMessage) (string, bool) {
	if kind == "number" {
		_, valid := finiteNumber(string(raw))
		return string(raw), valid
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return "", false
	}
	return value, true
}

func (snapshot *rowVariableSnapshot) unchanged(ctx context.Context) error {
	if snapshot.service.live(ctx, snapshot.principal) != nil {
		return ErrGateway
	}
	if snapshot.userRevision != 0 {
		var user User
		revision, digest, err := snapshot.service.Store.getWithDigest(ctx, userKey(snapshot.principal.User.ID), &user)
		if err != nil || revision != snapshot.userRevision || digest != snapshot.userDigest {
			return ErrGateway
		}
	}
	for id, prior := range snapshot.states {
		var definition SystemVariable
		current, digest, err := snapshot.service.Store.getWithDigest(ctx, systemVariableKey(snapshot.principal.Workspace, id), &definition)
		if err != nil || current != prior.revision || digest != prior.digest || definition.ID != id {
			return ErrGateway
		}
	}
	return nil
}

// Activation checks that every referenced custom definition exists, while
// per-user bindings are evaluated on each actual read. Missing or invalid
// bindings then fail closed rather than silently omitting the SQL predicate.
func (service *Service) rowVariableDefinitionSample(ctx context.Context, workspace ID, id int64) ([]string, bool) {
	var definition SystemVariable
	_, err := service.Store.get(ctx, systemVariableKey(workspace, id), &definition)
	if err != nil || definition.ID != id || definition.Type != "custom" || !validSystemVariable(definition) {
		return nil, false
	}
	var values []json.RawMessage
	if json.Unmarshal(definition.Value, &values) != nil || len(values) == 0 {
		return nil, false
	}
	if definition.VarType == "kv" {
		var pair struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(values[0], &pair) != nil {
			return nil, false
		}
		encoded, _ := json.Marshal(pair.Value)
		value, valid := rowVariableValue(definition.VarType, encoded)
		return []string{value}, valid
	}
	value, valid := rowVariableValue(definition.VarType, values[0])
	return []string{value}, valid
}
