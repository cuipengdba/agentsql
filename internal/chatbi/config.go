package chatbi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen           string `yaml:"listen"`
	StorePath        string `yaml:"store_path"`
	SecretEnv        string `yaml:"secret_env"`
	Gateway          string `yaml:"gateway"`
	AllowGatewayHTTP bool   `yaml:"allow_gateway_http"`
	GatewayCA        string `yaml:"gateway_ca_file"`
	MaxDataRequests  int    `yaml:"max_concurrent_data_requests"`
}

func LoadConfig(path string) (Config, error) {
	var config Config
	file, err := os.Open(path)
	if err != nil {
		return config, errors.New("cannot open ChatBI configuration")
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 65537))
	decoder.KnownFields(true)
	if decoder.Decode(&config) != nil {
		return config, errors.New("invalid ChatBI configuration")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return config, errors.New("multiple ChatBI configuration documents are not allowed")
	}
	if config.Listen == "" {
		config.Listen = "127.0.0.1:8000"
	}
	if _, _, err := net.SplitHostPort(config.Listen); err != nil {
		return config, errors.New("ChatBI listen must be host:port")
	}
	if config.StorePath == "" {
		return config, errors.New("ChatBI store_path is required")
	}
	if config.SecretEnv == "" {
		config.SecretEnv = "AGENTSQL_CHATBI_SECRET"
	}
	if config.MaxDataRequests == 0 {
		config.MaxDataRequests = 16
	}
	if config.MaxDataRequests < 1 || config.MaxDataRequests > 16 {
		return config, errors.New("ChatBI max_concurrent_data_requests must be between 1 and 16")
	}
	if _, err := validateEndpoint(config.Gateway, config.AllowGatewayHTTP); err != nil || !strings.HasSuffix(config.Gateway, "/mcp") {
		return config, errors.New("ChatBI gateway must be an allowed /mcp endpoint")
	}
	return config, nil
}

type IdentityKeys struct {
	SigningKey string `json:"signing_key"`
	LoginPEM   string `json:"login_private_key_pem"`
}

type Snapshot struct {
	Version       int              `json:"format_version"`
	SourceVersion string           `json:"source_version"`
	SourceCommit  string           `json:"source_commit"`
	Keys          IdentityKeys     `json:"identity_keys"`
	Users         []User           `json:"users"`
	Bindings      []Binding        `json:"bindings"`
	Models        []WorkspaceModel `json:"models"`
}
type WorkspaceModel struct {
	Workspace ID          `json:"workspace"`
	Model     ModelConfig `json:"model"`
}
type Preflight struct {
	SourceVersion     string   `json:"source_version"`
	Users             int      `json:"users"`
	Bindings          int      `json:"bindings"`
	Models            int      `json:"models"`
	LegacyLoginKey    bool     `json:"legacy_login_key"`
	CompleteMigration bool     `json:"complete_migration"`
	Pending           []string `json:"pending"`
}

func ReadSnapshot(path string) (Snapshot, Preflight, error) {
	var snapshot Snapshot
	var result Preflight
	file, err := os.Open(path)
	if err != nil {
		return snapshot, result, errors.New("cannot open private migration snapshot")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (16<<20)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF {
		return Snapshot{}, result, errors.New("invalid private migration snapshot")
	}
	if snapshot.Version != 1 || snapshot.SourceVersion != "v1.10.2" || snapshot.SourceCommit != "aa41fc1033bb974a703df9200a98ec8b9eec5f25" || len(snapshot.Keys.SigningKey) < 32 || len(snapshot.Users) == 0 {
		return Snapshot{}, result, errors.New("unsupported migration baseline or missing identities")
	}
	users := map[ID]User{}
	if _, err := parseLoginKey([]byte(snapshot.Keys.LoginPEM)); err != nil {
		return Snapshot{}, result, err
	}
	accounts := map[string]bool{}
	for _, user := range snapshot.Users {
		if validateUser(user) != nil || accounts[user.Account] || users[user.ID].ID != "" {
			return Snapshot{}, result, errors.New("duplicate or invalid imported identity")
		}
		users[user.ID] = user
		accounts[user.Account] = true
	}
	bindings := map[string]bool{}
	for _, b := range snapshot.Bindings {
		user, ok := users[b.User]
		key := string(b.Workspace) + "/" + string(b.User) + "/" + string(b.Datasource)
		if !ok || !user.canUse(b.Workspace) || !b.Datasource.Valid() || bindings[key] || b.BindingID == "" || b.ConnectorToken == "" || b.AgentKey == "" || b.Name == "" || (b.Dialect != "pg" && b.Dialect != "mysql") {
			return Snapshot{}, result, errors.New("invalid or duplicate imported binding")
		}
		bindings[key] = true
	}
	models := map[ID]bool{}
	for _, m := range snapshot.Models {
		if !m.Workspace.Valid() || models[m.Workspace] || validateModelConfig(m.Model) != nil {
			return Snapshot{}, result, errors.New("invalid or duplicate workspace model")
		}
		models[m.Workspace] = true
	}
	result = Preflight{SourceVersion: snapshot.SourceVersion, Users: len(snapshot.Users), Bindings: len(snapshot.Bindings), Models: len(snapshot.Models), LegacyLoginKey: snapshot.Keys.LoginPEM != "", CompleteMigration: false, Pending: []string{"API keys and assistant secrets", "historical chats and records", "dashboards and file data", "complete permission and application migration", "original-client differential acceptance"}}
	return snapshot, result, nil
}

// ImportSnapshot is initialization-only. Build a fresh target and retain the
// original system. No merge/overwrite path is exposed for existing identities.
func (s *Store) ImportSnapshot(ctx context.Context, snapshot Snapshot) error {
	var existing IdentityKeys
	if _, err := s.get(ctx, objectKey{"system", "system", "system", "identity-keys"}, &existing); !errors.Is(err, ErrNotFound) {
		return ErrConflict
	}
	if _, err := NewIdentity(s, []byte(snapshot.Keys.SigningKey), []byte(snapshot.Keys.LoginPEM)); err != nil {
		return err
	}
	// The caller imports into a fresh sibling file and publishes it only after
	// success; a failure must never make a partially initialized file runnable.
	for _, user := range snapshot.Users {
		if err := s.CreateUser(ctx, user); err != nil {
			return err
		}
	}
	for _, binding := range snapshot.Bindings {
		key := objectKey{"binding", string(binding.Workspace), string(binding.User), string(binding.Datasource)}
		if err := s.put(ctx, key, binding, 0); err != nil {
			return err
		}
	}
	for _, model := range snapshot.Models {
		if err := s.put(ctx, objectKey{"model", string(model.Workspace), "workspace", "default"}, model.Model, 0); err != nil {
			return err
		}
	}
	return s.put(ctx, objectKey{"system", "system", "system", "identity-keys"}, snapshot.Keys, 0)
}

func Assemble(ctx context.Context, config Config) (*Service, error) {
	secret := os.Getenv(config.SecretEnv)
	if len(secret) != 32 {
		return nil, errors.New("ChatBI encryption secret must contain exactly 32 bytes")
	}
	store, err := OpenStore(ctx, config.StorePath, []byte(secret))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Service, error) { _ = store.Close(); return nil, err }
	var keys IdentityKeys
	if _, err := store.get(ctx, objectKey{"system", "system", "system", "identity-keys"}, &keys); err != nil {
		return fail(errors.New("ChatBI has not been initialized; import a validated private snapshot"))
	}
	identity, err := NewIdentity(store, []byte(keys.SigningKey), []byte(keys.LoginPEM))
	if err != nil {
		return fail(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if config.GatewayCA != "" {
		raw, err := os.ReadFile(config.GatewayCA)
		if err != nil {
			return fail(errors.New("cannot read gateway CA"))
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return fail(errors.New("invalid gateway CA"))
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	return &Service{Store: store, Identity: identity, Gateway: &ControlledGateway{Endpoint: config.Gateway, AllowHTTP: config.AllowGatewayHTTP, Transport: transport}, Model: OpenAIModel{}}, nil
}
