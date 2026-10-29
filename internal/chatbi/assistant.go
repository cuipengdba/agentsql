package chatbi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

var ErrInvalidAssistant = errors.New("invalid assistant application")

// AssistantApplication is a workspace-owned application configuration. It is
// never a database credential or an authorization grant by itself.
type AssistantApplication struct {
	ID             ID        `json:"id,omitempty"`
	Workspace      ID        `json:"oid,omitempty"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Type           int       `json:"type"` // 0 basic, 1 advanced, 4 page embed
	Domain         string    `json:"domain"`
	DatasourceIDs  []ID      `json:"datasource_ids"`
	Configuration  string    `json:"configuration,omitempty"`
	UseCustomModel bool      `json:"enable_custom_model"`
	CustomModel    *ID       `json:"custom_model,omitempty"`
	Enabled        bool      `json:"enabled"`
	AppID          string    `json:"app_id,omitempty"`
	AppSecret      string    `json:"app_secret,omitempty"`
	Created        time.Time `json:"created_at"`
	Updated        time.Time `json:"updated_at"`
	Revision       int64     `json:"revision"`
}

func assistantKey(workspace, id ID) objectKey {
	return objectKey{"assistant", string(workspace), "workspace", string(id)}
}

func newAssistantCredential(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validAssistantDomain(raw string) bool {
	if raw == "" || len(raw) > 255 || strings.TrimSpace(raw) != raw {
		return false
	}
	parts := strings.Split(strings.ReplaceAll(raw, ";", ","), ",")
	if len(parts) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		u, err := url.Parse(part)
		if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") || seen[part] {
			return false
		}
		if u.Scheme == "http" {
			host := u.Hostname()
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return false
			}
		}
		seen[part] = true
	}
	return true
}

func assistantOriginAllowed(domains, asserted string) bool {
	if !validAssistantDomain(domains) || !validAssistantDomain(asserted) || strings.ContainsAny(asserted, ",;") {
		return false
	}
	asserted = strings.TrimSuffix(asserted, "/")
	for _, part := range strings.Split(strings.ReplaceAll(domains, ";", ","), ",") {
		if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(part), "/"), asserted) {
			return true
		}
	}
	return false
}

// A page application with an explicit datasource list cannot use the other
// bindings of the account named in its token. An empty list keeps the original
// page-embed behavior: the account's currently authorized sources are visible.
func (service *Service) embeddedSourceAllowed(ctx context.Context, p Principal, datasource ID) error {
	if p.EmbeddedApp == "" {
		return nil
	}
	var app AssistantApplication
	revision, err := service.Store.get(ctx, assistantKey(p.Workspace, p.EmbeddedApp), &app)
	if err != nil || revision != p.EmbeddedRevision || app.ID != p.EmbeddedApp || app.Workspace != p.Workspace || app.Type != 4 || !app.Enabled {
		return ErrUnauthenticated
	}
	if len(app.DatasourceIDs) == 0 {
		return nil
	}
	for _, allowed := range app.DatasourceIDs {
		if allowed == datasource {
			return nil
		}
	}
	return ErrNotFound
}

func (service *Service) Assistants(ctx context.Context, p Principal) ([]AssistantApplication, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return nil, err
	}
	items, err := service.Store.list(ctx, "assistant", string(p.Workspace), "workspace")
	if err != nil {
		return nil, err
	}
	result := make([]AssistantApplication, 0, len(items))
	for _, raw := range items {
		var app AssistantApplication
		if json.Unmarshal(raw, &app) != nil || !app.ID.Valid() || app.Workspace != p.Workspace {
			return nil, ErrUnavailable
		}
		result = append(result, app)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (service *Service) Assistant(ctx context.Context, p Principal, id ID) (AssistantApplication, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return AssistantApplication{}, err
	}
	if !id.Valid() {
		return AssistantApplication{}, ErrNotFound
	}
	var app AssistantApplication
	revision, err := service.Store.get(ctx, assistantKey(p.Workspace, id), &app)
	if err != nil {
		return AssistantApplication{}, err
	}
	if app.ID != id || app.Workspace != p.Workspace {
		return AssistantApplication{}, ErrUnavailable
	}
	app.Revision = revision
	return app, nil
}

func (service *Service) SaveAssistant(ctx context.Context, p Principal, app AssistantApplication) (AssistantApplication, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return AssistantApplication{}, err
	}
	app.Name = strings.TrimSpace(app.Name)
	if app.Name == "" || len([]rune(app.Name)) > 120 || len([]rune(app.Description)) > 2000 || (app.Type != 0 && app.Type != 1 && app.Type != 4) || !validAssistantDomain(app.Domain) || len(app.Configuration) > 8192 || len(app.DatasourceIDs) > 32 || app.Revision < 0 || (app.Workspace != "" && app.Workspace != p.Workspace) {
		return AssistantApplication{}, ErrInvalidAssistant
	}
	if app.Configuration != "" {
		var config map[string]json.RawMessage
		if json.Unmarshal([]byte(app.Configuration), &config) != nil || config == nil {
			return AssistantApplication{}, ErrInvalidAssistant
		}
	}
	if app.UseCustomModel {
		if app.CustomModel == nil || !app.CustomModel.Valid() {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		var model ModelConfig
		if _, err := service.Store.get(ctx, objectKey{"model", string(p.Workspace), "workspace", "default"}, &model); err != nil || validateModelConfig(model) != nil || model.ID != *app.CustomModel {
			return AssistantApplication{}, ErrInvalidAssistant
		}
	} else {
		app.CustomModel = nil
	}
	seen := map[ID]bool{}
	for _, source := range app.DatasourceIDs {
		if !source.Valid() || seen[source] {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		seen[source] = true
		if _, err := service.binding(ctx, p, source); err != nil {
			return AssistantApplication{}, err
		}
	}
	app.Workspace = p.Workspace
	var previous AssistantApplication
	if app.ID == "" {
		if app.Revision != 0 {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		if app.AppID != "" || app.AppSecret != "" {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		id, err := newID()
		if err != nil {
			return AssistantApplication{}, err
		}
		app.ID = id
		if app.Type == 4 {
			app.AppID, err = newAssistantCredential(18)
			if err != nil {
				return AssistantApplication{}, err
			}
			app.AppSecret, err = newAssistantCredential(32)
			if err != nil {
				return AssistantApplication{}, err
			}
		}
		app.Created = time.Now().UTC()
	} else {
		if !app.ID.Valid() || app.Revision < 1 {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		revision, err := service.Store.get(ctx, assistantKey(p.Workspace, app.ID), &previous)
		if err != nil {
			return AssistantApplication{}, err
		}
		if previous.ID != app.ID || previous.Workspace != p.Workspace {
			return AssistantApplication{}, ErrUnavailable
		}
		if revision != app.Revision {
			return AssistantApplication{}, ErrConflict
		}
		if previous.Type != app.Type {
			return AssistantApplication{}, ErrInvalidAssistant
		}
		app.AppID, app.AppSecret = previous.AppID, previous.AppSecret
		app.Created = previous.Created
	}
	app.Updated = time.Now().UTC()
	if err := service.semanticAdmin(ctx, p); err != nil {
		return AssistantApplication{}, err
	}
	if err := service.Store.writeSemantic(ctx, assistantKey(p.Workspace, app.ID), app, app.Revision, "assistant.save"); err != nil {
		return AssistantApplication{}, err
	}
	app.Revision++
	return app, nil
}

// RotateAssistantSecret invalidates the previous page-embed credential with an
// audited compare-and-swap write. Callers never choose their own credential.
func (service *Service) RotateAssistantSecret(ctx context.Context, p Principal, id ID) (AssistantApplication, error) {
	app, err := service.Assistant(ctx, p, id)
	if err != nil {
		return AssistantApplication{}, err
	}
	if app.Type != 4 || app.AppID == "" || app.AppSecret == "" {
		return AssistantApplication{}, ErrInvalidAssistant
	}
	app.AppSecret, err = newAssistantCredential(32)
	if err != nil {
		return AssistantApplication{}, err
	}
	app.Updated = time.Now().UTC()
	if err := service.semanticAdmin(ctx, p); err != nil {
		return AssistantApplication{}, err
	}
	if err := service.Store.writeSemantic(ctx, assistantKey(p.Workspace, id), app, app.Revision, "assistant.secret.rotate"); err != nil {
		return AssistantApplication{}, err
	}
	app.Revision++
	return app, nil
}

func (service *Service) DeleteAssistant(ctx context.Context, p Principal, id ID, revision int64) error {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return err
	}
	if !id.Valid() || revision < 1 {
		return ErrInvalidAssistant
	}
	return service.deleteAssistantWithImages(ctx, p.Workspace, id, revision)
}

// DeleteEmbeddedBatch commits all removals and their management audits in a
// single transaction, so the original UI's multi-selection never half-succeeds.
func (service *Service) DeleteEmbeddedBatch(ctx context.Context, p Principal, apps []AssistantApplication) error {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return err
	}
	if len(apps) == 0 || len(apps) > 100 {
		return ErrInvalidAssistant
	}
	seen := make(map[ID]bool, len(apps))
	for _, app := range apps {
		if !app.ID.Valid() || app.Type != 4 || app.Workspace != p.Workspace || app.Revision < 1 || seen[app.ID] {
			return ErrInvalidAssistant
		}
		seen[app.ID] = true
		current, err := service.Assistant(ctx, p, app.ID)
		if err != nil {
			return err
		}
		if current.Type != 4 || current.Revision != app.Revision {
			return ErrConflict
		}
	}
	tx, err := service.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, app := range apps {
		key := assistantKey(p.Workspace, app.ID)
		auditID, err := newID()
		if err != nil {
			return err
		}
		auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
		audit, err := service.Store.seal(auditKey, map[string]any{"action": "assistant.delete", "workspace": key.Scope, "id": key.ID, "time": time.Now().UTC()})
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`, key.Kind, key.Scope, key.Owner, key.ID, app.Revision)
		if err != nil {
			return ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil {
			return ErrUnavailable
		}
		if count != 1 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind='assistant-image' AND scope=? AND owner=?`, string(p.Workspace), string(app.ID)); err != nil {
			return ErrUnavailable
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`, auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
			return ErrUnavailable
		}
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
