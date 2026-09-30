// Package demoseed implements the fail-closed Live Demo seed plan.
package demoseed

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"gopkg.in/yaml.v3"
)

const manifestVersion = 1

var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Manifest contains only public connection metadata and environment-variable
// references. It intentionally has no plaintext secret, key, or DSN fields.
type Manifest struct {
	Version     int                  `yaml:"version"`
	Banner      string               `yaml:"banner"`
	QPSPerAgent int                  `yaml:"qps_per_agent"`
	Datasources []DatasourceManifest `yaml:"datasources"`
	Agents      []AgentManifest      `yaml:"agents"`
	MaskRules   []MaskRuleManifest   `yaml:"mask_rules"`
	Policies    []PolicyManifest     `yaml:"policies"`
	B5          B5Manifest           `yaml:"b5"`
	Rules       []RuleManifest       `yaml:"rules"`
}

type DatasourceManifest struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	DBType        string `yaml:"db_type"`
	Host          string `yaml:"host"`
	Port          int    `yaml:"port"`
	Database      string `yaml:"database"`
	Username      string `yaml:"username"`
	PasswordEnv   string `yaml:"password_env"`
	ConnLimit     int    `yaml:"conn_limit"`
	StmtTimeoutMS int    `yaml:"stmt_timeout_ms"`
	RowLimit      int    `yaml:"row_limit"`
}

type AgentManifest struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	Level     string `yaml:"level"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type MaskRuleManifest struct {
	ID            string `yaml:"id"`
	DatasourceID  string `yaml:"datasource_id"`
	SchemaName    string `yaml:"schema_name"`
	TableName     string `yaml:"table_name"`
	ColumnName    string `yaml:"column_name"`
	SensitiveType string `yaml:"sensitive_type"`
	Algo          string `yaml:"algo"`
}

type PolicyManifest struct {
	ID            string `yaml:"id"`
	AgentID       string `yaml:"agent_id"`
	DatasourceID  string `yaml:"datasource_id"`
	ObjectType    string `yaml:"object_type"`
	ObjectName    string `yaml:"object_name"`
	Columns       string `yaml:"columns,omitempty"`
	EnrollmentSQL string `yaml:"enrollment_sql,omitempty"`
	Action        string `yaml:"action"`
}

type B5Manifest struct {
	AgentID      string   `yaml:"agent_id"`
	DatasourceID string   `yaml:"datasource_id"`
	PolicyID     string   `yaml:"policy_id"`
	Statements   []string `yaml:"statements"`
}

type RuleManifest struct {
	ID      string `yaml:"id"`
	Enabled bool   `yaml:"enabled"`
}

// ParseManifest strictly decodes exactly one YAML document.
func ParseManifest(contents []byte) (Manifest, error) {
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode demo seed manifest: %w", err)
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if err == nil {
		return Manifest{}, errors.New("decode demo seed manifest: exactly one YAML document is required")
	}
	if !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("decode trailing demo seed manifest data: %w", err)
	}
	return manifest, nil
}

// Validate checks the manifest's fixed Live Demo contract against config and
// the executable built-in rule catalog.
func (manifest Manifest) Validate(cfg config.Config) error {
	if manifest.Version != manifestVersion {
		return fmt.Errorf("manifest version must be %d", manifestVersion)
	}
	if manifest.Banner != cfg.Demo.Banner {
		return errors.New("manifest banner differs from demo configuration")
	}
	if manifest.QPSPerAgent != cfg.Demo.EffectiveQPS() {
		return errors.New("manifest qps_per_agent differs from demo configuration")
	}
	if err := validateDatasources(manifest.Datasources); err != nil {
		return err
	}
	if err := validateAgents(manifest.Agents); err != nil {
		return err
	}
	if err := validateMaskRules(manifest.MaskRules); err != nil {
		return err
	}
	if err := validatePolicies(manifest.Policies); err != nil {
		return err
	}
	if err := validateB5(manifest.B5, manifest.Policies); err != nil {
		return err
	}
	if err := validateRules(manifest.Rules); err != nil {
		return err
	}
	return nil
}

func validateDatasources(items []DatasourceManifest) error {
	if len(items) != 2 {
		return fmt.Errorf("manifest must contain exactly 2 datasources, got %d", len(items))
	}
	expected := map[string]string{config.DemoDatasourcePG: "postgres", config.DemoDatasourceMySQL: "mysql"}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		dbType, ok := expected[item.ID]
		if !ok || seen[item.ID] {
			return fmt.Errorf("manifest contains unsupported or duplicate datasource id %q", item.ID)
		}
		seen[item.ID] = true
		if item.DBType != dbType || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Host) == "" ||
			item.Port < 1 || item.Port > 65535 || strings.TrimSpace(item.Database) == "" ||
			item.Username != "agentsql_demo_ro" || !envNamePattern.MatchString(item.PasswordEnv) {
			return fmt.Errorf("manifest datasource %q has invalid fixed or connection fields", item.ID)
		}
		if item.RowLimit != 20 || item.StmtTimeoutMS != 1500 || item.ConnLimit != 2 {
			return fmt.Errorf("manifest datasource %q must use row_limit=20, stmt_timeout_ms=1500, conn_limit=2", item.ID)
		}
	}
	return nil
}

func validateAgents(items []AgentManifest) error {
	if len(items) != 2 {
		return fmt.Errorf("manifest must contain exactly 2 agents, got %d", len(items))
	}
	expected := map[string]struct {
		level, env string
	}{
		"agent-demo-ro":  {level: "readonly", env: "AGENTSQL_DEMO_RO_KEY"},
		"agent-demo-dml": {level: "dml", env: "AGENTSQL_DEMO_DML_KEY"},
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		want, ok := expected[item.ID]
		if !ok || seen[item.ID] || item.Level != want.level || item.APIKeyEnv != want.env || strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("manifest agent %q differs from the fixed demo agent contract", item.ID)
		}
		seen[item.ID] = true
	}
	return nil
}

func validateMaskRules(items []MaskRuleManifest) error {
	if len(items) != 4 {
		return fmt.Errorf("manifest must contain exactly 4 mask rules, got %d", len(items))
	}
	expected := make(map[string]struct{}, 4)
	for _, datasourceID := range []string{config.DemoDatasourcePG, config.DemoDatasourceMySQL} {
		for _, column := range []string{"phone", "email"} {
			expected[datasourceID+"\x00"+column] = struct{}{}
		}
	}
	ids := make(map[string]struct{}, len(items))
	compiledByDatasource := make(map[string][]mask.Rule, 2)
	for _, item := range items {
		column := mask.NormalizeColumnName(item.ColumnName)
		key := item.DatasourceID + "\x00" + column
		if _, ok := expected[key]; !ok || item.SchemaName != "" || item.TableName != "" || item.SensitiveType != column || item.Algo != string(mask.AlgoMask) {
			return fmt.Errorf("manifest mask rule %q differs from the fixed demo mask contract", item.ID)
		}
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("manifest mask rule id is required")
		}
		if _, exists := ids[item.ID]; exists {
			return fmt.Errorf("manifest contains duplicate mask rule id %q", item.ID)
		}
		ids[item.ID] = struct{}{}
		compiledByDatasource[item.DatasourceID] = append(compiledByDatasource[item.DatasourceID], mask.Rule{
			Column: column, SensitiveType: mask.SensitiveType(item.SensitiveType), Algorithm: mask.Algorithm(item.Algo),
		})
	}
	// NewRedactor enforces normalized-column uniqueness within one datasource.
	for _, rules := range compiledByDatasource {
		if _, err := mask.NewRedactor(rules); err != nil {
			return fmt.Errorf("validate manifest mask rules: %w", err)
		}
	}
	return nil
}

func validatePolicies(items []PolicyManifest) error {
	type expectedPolicy struct{ agent, datasource, objectType, objectName, columns, enrollment, action string }
	wanted := map[string]expectedPolicy{
		"policy-demo-ro-pg-customers":                 {"agent-demo-ro", config.DemoDatasourcePG, "table", "public.customers", "", "", "allow"},
		"policy-demo-ro-pg-orders":                    {"agent-demo-ro", config.DemoDatasourcePG, "table", "public.orders", "", "", "allow"},
		"policy-demo-ro-pg-products":                  {"agent-demo-ro", config.DemoDatasourcePG, "table", "public.products", "", "", "allow"},
		"policy-demo-ro-pg-internal-notes":            {"agent-demo-ro", config.DemoDatasourcePG, "table", "public.internal_notes", "", "", "deny"},
		"policy-demo-ro-pg-demo-b2-customers":         {"agent-demo-ro", config.DemoDatasourcePG, "table", "public.demo_b2_customers", "", "", "allow"},
		"policy-demo-ro-pg-demo-b2-orders":            {"agent-demo-ro", config.DemoDatasourcePG, "table", "public.demo_b2_orders", "", "", "allow"},
		"policy-demo-ro-mysql-customers":              {"agent-demo-ro", config.DemoDatasourceMySQL, "table", "customers", "", "", "allow"},
		"policy-demo-ro-mysql-orders":                 {"agent-demo-ro", config.DemoDatasourceMySQL, "table", "orders", "", "", "allow"},
		"policy-demo-ro-mysql-products":               {"agent-demo-ro", config.DemoDatasourceMySQL, "table", "products", "", "", "allow"},
		"policy-demo-ro-mysql-internal-notes":         {"agent-demo-ro", config.DemoDatasourceMySQL, "table", "internal_notes", "", "", "deny"},
		"policy-demo-ro-pg-demo-b2-customers-columns": {"agent-demo-ro", config.DemoDatasourcePG, "column", "public.demo_b2_customers", "id,full_name,phone,email,region", "SELECT c.id, c.full_name, c.phone, c.email, c.region, o.status FROM public.demo_b2_customers c JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1", "allow"},
		"policy-demo-ro-pg-demo-b2-orders-columns":    {"agent-demo-ro", config.DemoDatasourcePG, "column", "public.demo_b2_orders", "id,customer_id,status", "SELECT c.id, c.full_name, c.phone, c.email, c.region, o.status FROM public.demo_b2_customers c JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1", "allow"},
		"policy-demo-ro-mysql-columns-unsupported":    {"agent-demo-ro", config.DemoDatasourceMySQL, "column", "customers", "full_name", "", "allow"},
		"policy-demo-dml-pg-demo-tx":                  {"agent-demo-dml", config.DemoDatasourcePG, "table", "public.demo_tx_accounts", "", "", "allow"},
		"policy-demo-dml-mysql-deny-all":              {"agent-demo-dml", config.DemoDatasourceMySQL, "table", "*", "", "", "deny"},
	}
	if len(items) != len(wanted) {
		return fmt.Errorf("manifest must contain exactly %d policies, got %d", len(wanted), len(items))
	}
	ids := make(map[string]struct{}, len(items))
	groups := make(map[string][]model.Policy)
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("manifest policy %q has invalid id or object_type", item.ID)
		}
		if _, exists := ids[item.ID]; exists {
			return fmt.Errorf("manifest contains duplicate policy id %q", item.ID)
		}
		ids[item.ID] = struct{}{}
		expected, ok := wanted[item.ID]
		if !ok || item.AgentID != expected.agent || item.DatasourceID != expected.datasource ||
			item.ObjectType != expected.objectType || item.ObjectName != expected.objectName || item.Columns != expected.columns ||
			item.EnrollmentSQL != expected.enrollment || item.Action != expected.action {
			return fmt.Errorf("manifest policy %q differs from the fixed demo policy contract", item.ID)
		}
		delete(wanted, item.ID)
		var columns *string
		if item.Columns != "" {
			value := item.Columns
			columns = &value
		}
		groups[item.AgentID+"\x00"+item.DatasourceID] = append(groups[item.AgentID+"\x00"+item.DatasourceID], model.Policy{
			ID: item.ID, AgentID: item.AgentID, DatasourceID: item.DatasourceID,
			ObjectType: item.ObjectType, ObjectName: item.ObjectName, Columns: columns, Action: item.Action,
		})
	}
	if len(wanted) != 0 {
		return errors.New("manifest policy list is incomplete")
	}
	resolver := policy.NewResolver()
	for group, policies := range groups {
		level := "readonly"
		if strings.HasPrefix(group, "agent-demo-dml\x00") {
			level = "dml"
		}
		if _, err := resolver.Resolve(policies, level); err != nil {
			return fmt.Errorf("validate manifest policy group: %w", err)
		}
	}
	return nil
}

func validateB5(item B5Manifest, policies []PolicyManifest) error {
	if item.AgentID != "agent-demo-dml" || item.DatasourceID != config.DemoDatasourcePG ||
		item.PolicyID != "policy-demo-dml-pg-demo-tx" || len(item.Statements) != 2 ||
		item.Statements[0] != "UPDATE public.demo_tx_accounts SET balance=balance+10 WHERE id=1" ||
		item.Statements[1] != "UPDATE public.demo_tx_accounts SET status='committed' WHERE id=1" {
		return errors.New("manifest B5 plan differs from the fixed demo transaction contract")
	}
	for _, policy := range policies {
		if policy.ID == item.PolicyID && policy.AgentID == item.AgentID && policy.DatasourceID == item.DatasourceID && policy.Action == "allow" {
			return nil
		}
	}
	return errors.New("manifest B5 policy is missing")
}

func validateRules(items []RuleManifest) error {
	wanted := make(map[string]struct{})
	for _, rule := range rules.BuiltinRuleOverrides() {
		wanted[rule.ID] = struct{}{}
	}
	if len(items) != len(wanted) {
		return fmt.Errorf("manifest must contain exactly %d executable built-in rules, got %d", len(wanted), len(items))
	}
	for _, item := range items {
		if !item.Enabled {
			return fmt.Errorf("manifest built-in rule %q must be enabled", item.ID)
		}
		if _, ok := wanted[item.ID]; !ok {
			return fmt.Errorf("manifest rule %q is not an executable built-in rule", item.ID)
		}
		delete(wanted, item.ID)
	}
	if len(wanted) != 0 {
		missing := make([]string, 0, len(wanted))
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return fmt.Errorf("manifest is missing built-in rules: %s", strings.Join(missing, ","))
	}
	return nil
}

// ValidateAPIKey accepts only the exact format emitted by store.GenerateAPIKey.
func ValidateAPIKey(value string) error {
	if !strings.HasPrefix(value, store.APIKeyPrefix) {
		return errors.New("API key must use the asql_ prefix")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, store.APIKeyPrefix))
	if err != nil || len(decoded) != 32 {
		return errors.New("API key must use the generated AgentSQL key format")
	}
	return nil
}
