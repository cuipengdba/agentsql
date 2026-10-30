package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

func NewInstallation(account, password string) (Snapshot, error) {
	if account == "" || len(account) > 100 {
		return Snapshot{}, errors.New("invalid administrator account")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Snapshot{}, err
	}
	secret := make([]byte, 48)
	if _, err := rand.Read(secret); err != nil {
		return Snapshot{}, ErrUnavailable
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	return Snapshot{Version: 1, SourceVersion: "native", Keys: IdentityKeys{SigningKey: base64.RawURLEncoding.EncodeToString(secret), LoginPEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}, Users: []User{{ID: "1", Account: account, Name: account, Workspace: "1", Language: "zh-CN", Status: 1, Admin: true, Memberships: []Membership{{Workspace: "1", Name: "Default", Weight: 1}}, PasswordHash: hash, PasswordFormat: "bcrypt", AuthRevision: 1}}}, nil
}

type ConnectionConfig struct {
	Bindings          []Binding                `json:"bindings"`
	Models            []WorkspaceModel         `json:"models"`
	Recommendations   []RecommendationConfig   `json:"recommendations"`
	AssistantCatalogs []AssistantCatalogBridge `json:"assistant_catalogs"`
}

func ReadConnections(path string) (ConnectionConfig, error) {
	var config ConnectionConfig
	file, err := os.Open(path)
	if err != nil {
		return config, errors.New("cannot read private connection configuration")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return ConnectionConfig{}, errors.New("invalid private connection configuration")
	}
	if len(config.Bindings) == 0 && len(config.Models) == 0 && len(config.Recommendations) == 0 && len(config.AssistantCatalogs) == 0 {
		return config, errors.New("no connection changes provided")
	}
	return config, nil
}

// ConfigureConnections is an operator-only filesystem command, not a browser
// trust boundary. The complete update and its local audit entry commit together.
func (s *Store) ConfigureConnections(ctx context.Context, config ConnectionConfig) error {
	type entry struct {
		key  objectKey
		body []byte
	}
	entries := []entry{}
	seen := map[objectKey]bool{}
	stagedBindings := map[objectKey]Binding{}
	appendEntry := func(key objectKey, value any) error {
		if seen[key] {
			return errors.New("duplicate connection update")
		}
		seen[key] = true
		body, err := s.seal(key, value)
		if err != nil {
			return err
		}
		entries = append(entries, entry{key, body})
		return nil
	}
	for _, binding := range config.Bindings {
		user, _, err := s.User(ctx, binding.User)
		if err != nil || !user.canUse(binding.Workspace) || !binding.Datasource.Valid() || binding.Name == "" || binding.BindingID == "" || binding.ConnectorToken == "" || binding.AgentKey == "" || (binding.Dialect != "pg" && binding.Dialect != "mysql") {
			return errors.New("invalid connection identity or binding")
		}
		key := objectKey{"binding", string(binding.Workspace), string(binding.User), string(binding.Datasource)}
		if err := appendEntry(key, binding); err != nil {
			return err
		}
		stagedBindings[key] = binding
	}
	for _, model := range config.Models {
		if !model.Workspace.Valid() || validateModelConfig(model.Model) != nil {
			return errors.New("invalid model configuration")
		}
		if err := appendEntry(objectKey{"model", string(model.Workspace), "workspace", "default"}, model.Model); err != nil {
			return err
		}
	}
	for _, recommendation := range config.Recommendations {
		if err := validateRecommendations(recommendation); err != nil {
			return err
		}
		if err := appendEntry(recommendationKey(recommendation.Workspace, recommendation.Datasource), recommendation); err != nil {
			return err
		}
	}
	for _, bridge := range config.AssistantCatalogs {
		credentials, err := parseMCPAssistantCatalogIdentity(bridge.Endpoint, bridge.Authorization)
		if err != nil || !bridge.Workspace.Valid() || !bridge.User.Valid() || len(bridge.Sources) == 0 || len(bridge.Sources) > 32 ||
			!validAssistantCatalogNetworks(bridge.AllowedCIDRs) ||
			strings.HasPrefix(bridge.Endpoint, "http://") && !bridge.AllowHTTP {
			return errors.New("invalid assistant catalog registration")
		}
		enabled := bridge.Enabled == nil || *bridge.Enabled
		if enabled {
			user, _, err := s.User(ctx, bridge.User)
			if err != nil || user.Admin || !user.canUse(bridge.Workspace) {
				return errors.New("invalid assistant catalog caller")
			}
		}
		mapped := map[ID]bool{}
		for _, source := range bridge.Sources {
			if !source.CatalogID.Valid() || !source.Datasource.Valid() || mapped[source.CatalogID] {
				return errors.New("invalid assistant catalog source mapping")
			}
			mapped[source.CatalogID] = true
			if enabled {
				key := objectKey{"binding", string(bridge.Workspace), string(bridge.User), string(source.Datasource)}
				binding, staged := stagedBindings[key]
				if !staged {
					if _, err := s.get(ctx, key, &binding); err != nil {
						return errors.New("assistant catalog source has no owned binding")
					}
				}
				if binding.Workspace != bridge.Workspace || binding.User != bridge.User || binding.Datasource != source.Datasource {
					return errors.New("assistant catalog source binding mismatch")
				}
			}
		}
		record := assistantCatalogBridgeRecord{Endpoint: bridge.Endpoint, Workspace: bridge.Workspace,
			User: bridge.User, Sources: bridge.Sources, AllowedCIDRs: bridge.AllowedCIDRs,
			AllowHTTP: bridge.AllowHTTP, Enabled: enabled}
		if err := appendEntry(s.assistantCatalogBridgeKey(bridge.Endpoint, credentials), record); err != nil {
			return err
		}
	}
	id, err := newID()
	if err != nil {
		return err
	}
	if err := appendEntry(objectKey{"management-audit", "system", "system", string(id)}, map[string]any{"action": "connections.configure", "time": time.Now().UTC(), "models": len(config.Models), "bindings": len(config.Bindings), "recommendations": len(config.Recommendations), "assistant_catalogs": len(config.AssistantCatalogs)}); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, entry := range entries {
		if entry.key.Kind == "binding" {
			var retired int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM chatbi_objects WHERE kind='retired-datasource' AND scope=? AND owner=? AND id=?`,
				entry.key.Scope, entry.key.Owner, entry.key.ID).Scan(&retired)
			if err == nil {
				return ErrConflict
			}
			if !controlNoRows(err) {
				return ErrUnavailable
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT(kind,scope,owner,id) DO UPDATE SET revision=chatbi_objects.revision+1,body=excluded.body`, entry.key.Kind, entry.key.Scope, entry.key.Owner, entry.key.ID, entry.body)
		if err != nil {
			return ErrUnavailable
		}
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
