package demoseed

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
)

// DatasourcePinger is injectable so the default CLI can dial real databases
// while unit tests remain hermetic.
type DatasourcePinger interface {
	Ping(ctx context.Context, datasource model.Datasource, password string) error
}

type LookupEnv func(string) (string, bool)

type Secrets struct {
	Passwords map[string]string
	APIKeys   map[string]string
}

type Summary struct {
	Datasources int
	Agents      int
	MaskRules   int
	Policies    int
	Rules       int
	Audits      int
	Approvals   int
}

// LoadSecrets resolves every secret reference before any write occurs.
func LoadSecrets(manifest Manifest, lookup LookupEnv) (Secrets, error) {
	if lookup == nil {
		return Secrets{}, errors.New("demo seed environment lookup is unavailable")
	}
	result := Secrets{Passwords: make(map[string]string), APIKeys: make(map[string]string)}
	for _, datasource := range manifest.Datasources {
		value, ok := lookup(datasource.PasswordEnv)
		if !ok || value == "" {
			return Secrets{}, fmt.Errorf("required environment variable %s is missing or empty", datasource.PasswordEnv)
		}
		result.Passwords[datasource.ID] = value
	}
	for _, agent := range manifest.Agents {
		value, ok := lookup(agent.APIKeyEnv)
		if !ok || value == "" {
			return Secrets{}, fmt.Errorf("required environment variable %s is missing or empty", agent.APIKeyEnv)
		}
		if err := ValidateAPIKey(value); err != nil {
			return Secrets{}, fmt.Errorf("environment variable %s is not a valid AgentSQL API key", agent.APIKeyEnv)
		}
		result.APIKeys[agent.ID] = value
	}
	return result, nil
}

// Run creates or verifies the complete fixed demo seed. Existing rows are
// always compared and are never updated or rotated.
func Run(
	ctx context.Context,
	metadataStore *store.Store,
	manifest Manifest,
	secrets Secrets,
	anchor time.Time,
	verifyOnly bool,
	pinger DatasourcePinger,
) (Summary, error) {
	if ctx == nil || metadataStore == nil {
		return Summary{}, errors.New("demo seed store or context is unavailable")
	}
	if pinger == nil {
		return Summary{}, errors.New("demo seed datasource pinger is unavailable")
	}

	for _, item := range manifest.Datasources {
		expected := datasourceModel(item)
		stored, err := ensureDatasource(ctx, metadataStore, expected, secrets.Passwords[item.ID], verifyOnly)
		if err != nil {
			return Summary{}, err
		}
		plaintext, err := metadataStore.Datasources().DecryptPassword(stored.PasswordEnc)
		if err != nil {
			return Summary{}, fmt.Errorf("verify datasource %q password encryption: stored password cannot be decrypted", item.ID)
		}
		if plaintext != secrets.Passwords[item.ID] {
			return Summary{}, fmt.Errorf("demo seed conflict: datasource %q password differs", item.ID)
		}
		if err := pinger.Ping(ctx, stored, plaintext); err != nil {
			return Summary{}, fmt.Errorf("connectivity probe failed for datasource %q", item.ID)
		}
	}

	for _, item := range manifest.Agents {
		if err := ensureAgent(ctx, metadataStore, agentModel(item, secrets.APIKeys[item.ID]), verifyOnly); err != nil {
			return Summary{}, err
		}
	}
	for _, item := range manifest.MaskRules {
		if err := ensureMaskRule(ctx, metadataStore, maskRuleModel(item), verifyOnly); err != nil {
			return Summary{}, err
		}
	}
	for _, item := range manifest.Policies {
		if err := ensurePolicy(ctx, metadataStore, policyModel(item), verifyOnly); err != nil {
			return Summary{}, err
		}
	}
	catalog := make(map[string]model.Rule)
	for _, rule := range rules.BuiltinRuleOverrides() {
		catalog[rule.ID] = rule
	}
	for _, item := range manifest.Rules {
		expected := catalog[item.ID]
		expected.Enabled = item.Enabled
		if err := ensureRule(ctx, metadataStore, expected, verifyOnly); err != nil {
			return Summary{}, err
		}
	}

	if err := verifyFixedCollections(ctx, metadataStore, manifest); err != nil {
		return Summary{}, err
	}
	audits, approvals, err := GenerateHistory(anchor)
	if err != nil {
		return Summary{}, err
	}
	if verifyOnly {
		if err := verifyHistory(ctx, metadataStore, audits, approvals, anchor); err != nil {
			return Summary{}, err
		}
	} else {
		if _, _, err := metadataStore.SeedHistoricalAudits(ctx, audits, approvals); err != nil {
			return Summary{}, fmt.Errorf("seed demo historical data: %w", err)
		}
		if err := verifyHistory(ctx, metadataStore, audits, approvals, anchor); err != nil {
			return Summary{}, err
		}
	}
	return Summary{
		Datasources: len(manifest.Datasources), Agents: len(manifest.Agents),
		MaskRules: len(manifest.MaskRules), Policies: len(manifest.Policies), Rules: len(manifest.Rules),
		Audits: len(audits), Approvals: len(approvals),
	}, nil
}

func ensureDatasource(ctx context.Context, opened *store.Store, expected model.Datasource, password string, verify bool) (model.Datasource, error) {
	stored, err := opened.Datasources().Get(ctx, expected.ID)
	if errors.Is(err, store.ErrNotFound) {
		if verify {
			return model.Datasource{}, fmt.Errorf("demo seed verify: datasource %q is missing", expected.ID)
		}
		created, createErr := opened.Datasources().Create(ctx, expected, password)
		if createErr != nil {
			return model.Datasource{}, fmt.Errorf("create demo datasource %q: operation failed", expected.ID)
		}
		return created, nil
	}
	if err != nil {
		return model.Datasource{}, fmt.Errorf("read demo datasource %q: operation failed", expected.ID)
	}
	if !sameDatasource(expected, stored) {
		return model.Datasource{}, fmt.Errorf("demo seed conflict: datasource %q differs", expected.ID)
	}
	return stored, nil
}

func ensureAgent(ctx context.Context, opened *store.Store, expected model.Agent, verify bool) error {
	stored, err := opened.Agents().Get(ctx, expected.ID)
	if errors.Is(err, store.ErrNotFound) {
		if verify {
			return fmt.Errorf("demo seed verify: agent %q is missing", expected.ID)
		}
		if _, createErr := opened.Agents().Create(ctx, expected); createErr != nil {
			return fmt.Errorf("create demo agent %q: operation failed", expected.ID)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read demo agent %q: operation failed", expected.ID)
	}
	if !sameAgent(expected, stored) {
		return fmt.Errorf("demo seed conflict: agent %q differs", expected.ID)
	}
	return nil
}

func ensureMaskRule(ctx context.Context, opened *store.Store, expected model.MaskRule, verify bool) error {
	stored, err := opened.MaskRules().Get(ctx, expected.ID)
	if errors.Is(err, store.ErrNotFound) {
		if verify {
			return fmt.Errorf("demo seed verify: mask rule %q is missing", expected.ID)
		}
		if _, createErr := opened.MaskRules().Create(ctx, expected); createErr != nil {
			return fmt.Errorf("create demo mask rule %q: operation failed", expected.ID)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read demo mask rule %q: operation failed", expected.ID)
	}
	if !sameMaskRule(expected, stored) {
		return fmt.Errorf("demo seed conflict: mask rule %q differs", expected.ID)
	}
	return nil
}

func ensurePolicy(ctx context.Context, opened *store.Store, expected model.Policy, verify bool) error {
	stored, err := opened.Policies().Get(ctx, expected.ID)
	if errors.Is(err, store.ErrNotFound) {
		if verify {
			return fmt.Errorf("demo seed verify: policy %q is missing", expected.ID)
		}
		if _, createErr := opened.Policies().Create(ctx, expected); createErr != nil {
			return fmt.Errorf("create demo policy %q: operation failed", expected.ID)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read demo policy %q: operation failed", expected.ID)
	}
	if !samePolicy(expected, stored) {
		return fmt.Errorf("demo seed conflict: policy %q differs", expected.ID)
	}
	return nil
}

func ensureRule(ctx context.Context, opened *store.Store, expected model.Rule, verify bool) error {
	stored, err := opened.Rules().Get(ctx, expected.ID)
	if errors.Is(err, store.ErrNotFound) {
		if verify {
			return fmt.Errorf("demo seed verify: built-in rule %q is missing", expected.ID)
		}
		if _, createErr := opened.Rules().Create(ctx, expected); createErr != nil {
			return fmt.Errorf("create demo built-in rule %q: operation failed", expected.ID)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read demo built-in rule %q: operation failed", expected.ID)
	}
	if !sameRule(expected, stored) {
		return fmt.Errorf("demo seed conflict: built-in rule %q differs", expected.ID)
	}
	return nil
}

func verifyFixedCollections(ctx context.Context, opened *store.Store, manifest Manifest) error {
	datasources, err := opened.Datasources().List(ctx)
	if err != nil || len(datasources) != len(manifest.Datasources) {
		return fmt.Errorf("demo seed fixed count mismatch: datasources=%d want=%d", len(datasources), len(manifest.Datasources))
	}
	agents, err := opened.Agents().List(ctx)
	if err != nil || len(agents) != len(manifest.Agents) {
		return fmt.Errorf("demo seed fixed count mismatch: agents=%d want=%d", len(agents), len(manifest.Agents))
	}
	masks, err := opened.MaskRules().List(ctx)
	if err != nil || len(masks) != len(manifest.MaskRules) {
		return fmt.Errorf("demo seed fixed count mismatch: mask_rules=%d want=%d", len(masks), len(manifest.MaskRules))
	}
	policies, err := opened.Policies().List(ctx)
	if err != nil || len(policies) != len(manifest.Policies) {
		return fmt.Errorf("demo seed fixed count mismatch: policies=%d want=%d", len(policies), len(manifest.Policies))
	}
	storedRules, err := opened.Rules().List(ctx, "")
	if err != nil || len(storedRules) != len(manifest.Rules) {
		return fmt.Errorf("demo seed fixed count mismatch: rules=%d want=%d", len(storedRules), len(manifest.Rules))
	}
	return nil
}

func verifyHistory(ctx context.Context, opened *store.Store, expectedAudits []model.AuditLog, expectedApprovals []model.Approval, anchor time.Time) error {
	page, err := opened.AuditLogs().Page(ctx, 1, 1000)
	if err != nil {
		return fmt.Errorf("verify demo audits: read failed")
	}
	if page.Total != int64(len(expectedAudits)) || len(page.List) != len(expectedAudits) {
		return fmt.Errorf("demo seed history count mismatch: audits=%d want=%d", page.Total, len(expectedAudits))
	}
	bySession := make(map[string]model.AuditLog, len(page.List))
	for _, stored := range page.List {
		if stored.SessionID == nil || !strings.HasPrefix(*stored.SessionID, store.DemoSeedSessionPrefix) {
			return errors.New("demo seed history contains a non-demo or empty session_id")
		}
		if _, duplicate := bySession[*stored.SessionID]; duplicate {
			return fmt.Errorf("demo seed history contains duplicate session_id %q", *stored.SessionID)
		}
		bySession[*stored.SessionID] = stored
	}
	recordedInSeedOrder := make([]model.AuditLog, len(expectedAudits))
	for index, expected := range expectedAudits {
		stored, ok := bySession[*expected.SessionID]
		if !ok {
			return fmt.Errorf("demo seed audit %q is missing", *expected.SessionID)
		}
		if !sameAudit(expected, stored) {
			return fmt.Errorf("demo seed conflict: audit %q differs", *expected.SessionID)
		}
		recordedInSeedOrder[index] = stored
	}
	if err := ValidateHistory(page.List, mustApprovalShapeForCounts(expectedApprovals), anchor); err != nil {
		return fmt.Errorf("verify demo audit distribution: %w", err)
	}

	approvalPage, err := opened.Approvals().ListPage(ctx, "", 1, 100)
	if err != nil {
		return fmt.Errorf("verify demo approvals: read failed")
	}
	if approvalPage.Total != int64(len(expectedApprovals)) || len(approvalPage.List) != len(expectedApprovals) {
		return fmt.Errorf("demo seed approval count mismatch: approvals=%d want=%d", approvalPage.Total, len(expectedApprovals))
	}
	storedByID := make(map[string]model.Approval, len(approvalPage.List))
	for _, approval := range approvalPage.List {
		storedByID[approval.ID] = approval
	}
	approvalIndex := 0
	for auditIndex, auditLog := range expectedAudits {
		if auditLog.Decision != "approve" {
			continue
		}
		expected := expectedApprovals[approvalIndex]
		expected.AuditID = &recordedInSeedOrder[auditIndex].ID
		stored, ok := storedByID[expected.ID]
		if !ok || !sameApproval(expected, stored) {
			return fmt.Errorf("demo seed conflict: approval %q differs or is missing", expected.ID)
		}
		approvalIndex++
	}
	return nil
}

func mustApprovalShapeForCounts(source []model.Approval) []model.Approval {
	return append([]model.Approval(nil), source...)
}

func datasourceModel(item DatasourceManifest) model.Datasource {
	return model.Datasource{ID: item.ID, Name: item.Name, DBType: item.DBType, Host: item.Host, Port: item.Port,
		Database: item.Database, Username: item.Username, ConnLimit: item.ConnLimit,
		StmtTimeoutMS: item.StmtTimeoutMS, RowLimit: item.RowLimit}
}

func agentModel(item AgentManifest, apiKey string) model.Agent {
	return model.Agent{ID: item.ID, Name: item.Name, Status: "active", APIKeyHash: store.HashAPIKey(apiKey), Level: item.Level}
}

func maskRuleModel(item MaskRuleManifest) model.MaskRule {
	datasourceID := item.DatasourceID
	return model.MaskRule{ID: item.ID, DatasourceID: &datasourceID, TableName: item.TableName,
		ColumnName: item.ColumnName, SensitiveType: item.SensitiveType, Algo: item.Algo, Enabled: true}
}

func policyModel(item PolicyManifest) model.Policy {
	return model.Policy{ID: item.ID, AgentID: item.AgentID, DatasourceID: item.DatasourceID,
		ObjectType: item.ObjectType, ObjectName: item.ObjectName, Action: item.Action}
}

func sameDatasource(expected, stored model.Datasource) bool {
	return expected.ID == stored.ID && expected.Name == stored.Name && expected.DBType == stored.DBType &&
		expected.Host == stored.Host && expected.Port == stored.Port && expected.Database == stored.Database &&
		expected.Username == stored.Username && expected.ConnLimit == stored.ConnLimit &&
		expected.StmtTimeoutMS == stored.StmtTimeoutMS && expected.RowLimit == stored.RowLimit
}

func sameAgent(expected, stored model.Agent) bool {
	return expected.ID == stored.ID && expected.Name == stored.Name && equalString(expected.Owner, stored.Owner) &&
		expected.Status == stored.Status && expected.APIKeyHash == stored.APIKeyHash && expected.Level == stored.Level &&
		equalTime(expected.ExpiresAt, stored.ExpiresAt)
}

func sameMaskRule(expected, stored model.MaskRule) bool {
	return expected.ID == stored.ID && equalString(expected.DatasourceID, stored.DatasourceID) &&
		expected.TableName == stored.TableName && expected.ColumnName == stored.ColumnName &&
		expected.SensitiveType == stored.SensitiveType && expected.Algo == stored.Algo
}

func samePolicy(expected, stored model.Policy) bool {
	return expected.ID == stored.ID && expected.AgentID == stored.AgentID && expected.DatasourceID == stored.DatasourceID &&
		expected.ObjectType == stored.ObjectType && expected.ObjectName == stored.ObjectName &&
		equalString(expected.Columns, stored.Columns) && equalString(expected.RowFilter, stored.RowFilter) && expected.Action == stored.Action
}

func sameRule(expected, stored model.Rule) bool {
	return expected.ID == stored.ID && expected.DBType == stored.DBType && expected.Title == stored.Title &&
		expected.RiskLevel == stored.RiskLevel && expected.PatternType == stored.PatternType &&
		expected.Definition == stored.Definition && expected.Enabled == stored.Enabled && expected.Builtin == stored.Builtin
}

func sameAudit(expected, stored model.AuditLog) bool {
	if !expected.TS.Equal(stored.TS) {
		return false
	}
	expected.ID = stored.ID
	expected.TS = stored.TS
	return reflect.DeepEqual(expected, stored)
}

func sameApproval(expected, stored model.Approval) bool {
	return expected.ID == stored.ID && equalInt64(expected.AuditID, stored.AuditID) &&
		equalString(expected.AgentID, stored.AgentID) && equalString(expected.SQLRaw, stored.SQLRaw) &&
		equalString(expected.Reason, stored.Reason) && expected.Status == stored.Status &&
		equalString(expected.Approver, stored.Approver) && equalTime(expected.DecidedAt, stored.DecidedAt)
}

func equalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
func equalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
func equalTime(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

func SortedFixedIDs(manifest Manifest) (datasources, agents []string) {
	for _, item := range manifest.Datasources {
		datasources = append(datasources, item.ID)
	}
	for _, item := range manifest.Agents {
		agents = append(agents, item.ID)
	}
	sort.Strings(datasources)
	sort.Strings(agents)
	return datasources, agents
}
