package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// BindingRotation is an operator-only update to the private gateway mapping.
// The browser never supplies these credentials or a database connection target.
type BindingRotation struct {
	Workspace         ID     `json:"workspace"`
	User              ID     `json:"user"`
	Datasource        ID     `json:"datasource"`
	ExpectedBindingID string `json:"expected_binding_id"`
	BindingID         string `json:"binding_id"`
	ConnectorToken    string `json:"connector_token"`
	AgentKey          string `json:"agent_key"`
	ResolveName       string `json:"resolve_name"`
}

func ReadBindingRotation(path string) (BindingRotation, error) {
	file, err := os.Open(path)
	if err != nil {
		return BindingRotation{}, errors.New("cannot read private binding rotation")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 1<<20 {
		return BindingRotation{}, errors.New("invalid private binding rotation")
	}
	var rotation BindingRotation
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&rotation) != nil || decoder.Decode(new(any)) != io.EOF || !validBindingRotation(rotation) {
		return BindingRotation{}, errors.New("invalid private binding rotation")
	}
	return rotation, nil
}

func validBindingRotation(rotation BindingRotation) bool {
	return rotation.Workspace.Valid() && rotation.User.Valid() && rotation.Datasource.Valid() &&
		len(rotation.ExpectedBindingID) > 0 && len(rotation.ExpectedBindingID) <= 256 &&
		len(rotation.BindingID) > 0 && len(rotation.BindingID) <= 256 &&
		len(rotation.ConnectorToken) > 0 && len(rotation.ConnectorToken) <= 16384 &&
		len(rotation.AgentKey) > 0 && len(rotation.AgentKey) <= 4096 &&
		len(rotation.ResolveName) <= 100 &&
		!strings.ContainsAny(rotation.ExpectedBindingID+rotation.BindingID+rotation.ConnectorToken+rotation.AgentKey+rotation.ResolveName, "\x00\r\n")
}

// RotateBinding checks the new gateway mapping before committing it. The
// encrypted binding revision changes atomically with a management audit, so
// in-flight queries using the prior mapping cannot release their results.
func (service *Service) RotateBinding(ctx context.Context, rotation BindingRotation) error {
	if service == nil || service.Store == nil || service.Gateway == nil || !validBindingRotation(rotation) {
		return ErrUnavailable
	}
	user, _, err := service.Store.User(ctx, rotation.User)
	if err != nil || !user.canUse(rotation.Workspace) {
		return ErrUnauthenticated
	}
	principal := Principal{User: user, Workspace: rotation.Workspace}
	current, revision, digest, err := service.bindingSnapshot(ctx, principal, rotation.Datasource)
	if err != nil {
		return err
	}
	if !databaseBinding(current) || current.BindingID != rotation.ExpectedBindingID {
		return ErrConflict
	}
	replacement := current
	replacement.BindingID = rotation.BindingID
	replacement.ConnectorToken = rotation.ConnectorToken
	replacement.AgentKey = rotation.AgentKey
	replacement.ResolveName = rotation.ResolveName
	if _, err := service.semanticSchema(ctx, principal, replacement); err != nil {
		return err
	}
	if err := service.bindingUnchanged(ctx, principal, current, revision, digest); err != nil {
		return err
	}
	return service.Store.writeSemantic(ctx, bindingKey(principal, current.Datasource), replacement, revision, "connections.binding.rotate")
}
