package demoseed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
)

// DatasourcePinger is injectable so the default CLI can dial real databases
// while unit tests remain hermetic.
type DatasourcePinger interface {
	Ping(ctx context.Context, datasource model.Datasource, password string) error
}

type FeatureEnroller interface {
	EnrollPostgresPolicySelect(context.Context, model.Datasource, []byte, string, executor.Limits) (executor.PostgresPolicyEnrollment, error)
	EnrollPostgresPolicyDML(context.Context, model.Datasource, []byte, string, executor.Limits) (executor.PostgresPolicyEnrollment, error)
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
	B5Grants    int
	B2Mode      string
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
// compared and are never rotated; fixed-ID mask rules are updated to the
// current manifest so an upgraded demo database remains restart-safe.
func Run(
	ctx context.Context,
	metadataStore *store.Store,
	manifest Manifest,
	secrets Secrets,
	anchor time.Time,
	verifyOnly bool,
	pinger DatasourcePinger,
	enroller FeatureEnroller,
	masterSecret []byte,
) (Summary, error) {
	if ctx == nil || metadataStore == nil {
		return Summary{}, errors.New("demo seed store or context is unavailable")
	}
	if pinger == nil {
		return Summary{}, errors.New("demo seed datasource pinger is unavailable")
	}
	if enroller == nil || len(masterSecret) < 32 {
		return Summary{}, errors.New("demo seed feature enroller or master secret is unavailable")
	}

	storedDatasources := make(map[string]model.Datasource, len(manifest.Datasources))
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
		storedDatasources[item.ID] = stored
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
	b2Mode := ""
	for _, item := range manifest.Policies {
		expected, mode, err := enrolledPolicy(ctx, item, storedDatasources[item.DatasourceID], enroller, masterSecret)
		if err != nil {
			return Summary{}, err
		}
		if mode != "" {
			if b2Mode != "" && b2Mode != mode {
				return Summary{}, errors.New("demo seed PostgreSQL column policies selected inconsistent binder modes")
			}
			b2Mode = mode
		}
		if err := ensurePolicy(ctx, metadataStore, expected, verifyOnly); err != nil {
			return Summary{}, err
		}
	}
	b5Grants, b5Mode, err := ensureB5Grants(ctx, metadataStore, manifest.B5, storedDatasources[manifest.B5.DatasourceID], enroller, masterSecret, verifyOnly)
	if err != nil {
		return Summary{}, err
	}
	if b2Mode == "" {
		b2Mode = b5Mode
	}
	if b5Mode != "" && b2Mode != b5Mode {
		return Summary{}, errors.New("demo seed B2 and B5 selected inconsistent binder modes")
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
		B5Grants: b5Grants, B2Mode: b2Mode,
	}, nil
}

func enrolledPolicy(ctx context.Context, item PolicyManifest, datasource model.Datasource, enroller FeatureEnroller, secret []byte) (model.Policy, string, error) {
	expected := policyModel(item)
	if item.ObjectType != "column" {
		return expected, "", nil
	}
	if item.DatasourceID == "ds-demo-mysql" {
		// This remains staged and unusable. PostgreSQL protocol-3 readiness only
		// counts PostgreSQL bindings; the pipeline sees the marker and returns the
		// stable MySQL-unsupported response before opening a business connection.
		return expected, "", nil
	}
	enrollment, err := enroller.EnrollPostgresPolicySelect(ctx, datasource, secret, item.EnrollmentSQL, executor.DefaultLimits)
	if err != nil {
		return model.Policy{}, "", fmt.Errorf("enroll demo column policy %q: %w", item.ID, err)
	}
	var relation executor.PostgresPolicyRelation
	for _, candidate := range enrollment.Relations {
		if candidate.Schema+"."+candidate.Name == item.ObjectName {
			relation = candidate
			break
		}
	}
	if relation.RelationOID == 0 || relation.Kind != 'r' || relation.CatalogFingerprint == "" {
		return model.Policy{}, "", fmt.Errorf("enroll demo column policy %q: relation identity mismatch", item.ID)
	}
	bindingID := "binding-" + item.ID
	stable := executor.PostgresStableObjectID(relation.DatabaseOID, relation.RelationOID)
	fingerprint := relation.CatalogFingerprint
	expected.RelationBindingID = &bindingID
	expected.RelationBinding = &model.RelationPolicyBinding{ID: bindingID, SchemaName: relation.Schema, RelationName: relation.Name,
		StableObjectID: &stable, CatalogFingerprint: &fingerprint, Status: "healthy", Revision: 1}
	columns := make(map[string]executor.PostgresPolicyColumnUse)
	for _, use := range enrollment.ColumnUses {
		if use.RelationOID == relation.RelationOID {
			columns[use.Name] = use
		}
	}
	for _, name := range strings.Split(item.Columns, ",") {
		use, ok := columns[name]
		if !ok || use.RelationOID != relation.RelationOID || use.Attnum <= 0 {
			return model.Policy{}, "", fmt.Errorf("enroll demo column policy %q: column %q identity missing", item.ID, name)
		}
		for _, usage := range []string{"output", "reference"} {
			expected.ColumnPermissions = append(expected.ColumnPermissions, model.PolicyColumnPermission{PolicyID: item.ID,
				RelationEnrollmentID: bindingID, ColumnOrdinal: int(use.Attnum), ColumnName: name,
				ColumnTypeDigest: executor.PostgresColumnTypeDigest(use.TypeOID, use.TypeModifier, use.CollationOID), Usage: usage, ParentRevision: 1})
		}
	}
	return expected, enrollment.Mode, nil
}

func ensureB5Grants(ctx context.Context, opened *store.Store, manifest B5Manifest, datasource model.Datasource, enroller FeatureEnroller, secret []byte, verify bool) (int, string, error) {
	byKey := make(map[string]store.B5DMLGrant)
	mode := ""
	for _, sqlText := range manifest.Statements {
		enrollment, err := enroller.EnrollPostgresPolicyDML(ctx, datasource, secret, sqlText, executor.DefaultLimits)
		if err != nil {
			return 0, "", fmt.Errorf("enroll demo B5 statement: %w", err)
		}
		if mode != "" && mode != enrollment.Mode {
			return 0, "", errors.New("demo B5 statements selected inconsistent binder modes")
		}
		mode = enrollment.Mode
		if len(enrollment.Relations) != 1 || enrollment.Action != b5.ActionUpdate {
			return 0, "", errors.New("demo B5 enrollment is not one closed UPDATE relation")
		}
		relation := enrollment.Relations[0]
		base := store.B5DMLGrant{PolicyID: manifest.PolicyID, PolicyRevision: 1, PrincipalID: manifest.AgentID,
			DatasourceID: manifest.DatasourceID, Effect: b5.GrantAllow, Action: enrollment.Action,
			DatabaseOID: relation.DatabaseOID, RelationOID: relation.RelationOID, RelationKind: string([]byte{relation.Kind}),
			SchemaName: relation.Schema, RelationName: relation.Name, CatalogFingerprint: relation.CatalogFingerprint,
			ProofSchemaID: b5.DMLGrantProofSchemaID, ProofSchemaVersion: b5.DMLGrantProofSchemaVersion}
		action := base
		action.Element = b5.GrantElementAction
		if err := putB5Grant(byKey, "action:update", action); err != nil {
			return 0, "", err
		}
		for _, write := range enrollment.WriteTargets {
			grant := base
			grant.Element = b5.GrantElementWriteTarget
			kind := "COLUMN"
			if write.Kind == b5dml.WriteTargetRow {
				kind = "ROW"
			}
			grant.WriteTargetKind = &kind
			if write.Kind == b5dml.WriteTargetColumn {
				setB5GrantColumn(&grant, write.Attnum, write.Name, write.TypeOID, write.TypeModifier, write.CollationOID)
			}
			if err := putB5Grant(byKey, fmt.Sprintf("write:%s:%d", kind, write.Attnum), grant); err != nil {
				return 0, "", err
			}
		}
		for _, reference := range enrollment.ColumnUses {
			if reference.Usage != "reference" {
				continue
			}
			grant := base
			grant.Element = b5.GrantElementReference
			kind := "COLUMN"
			grant.ReferenceKind = &kind
			setB5GrantColumn(&grant, reference.Attnum, reference.Name, reference.TypeOID, reference.TypeModifier, reference.CollationOID)
			if err := putB5Grant(byKey, fmt.Sprintf("reference:%d", reference.Attnum), grant); err != nil {
				return 0, "", err
			}
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		expected := byKey[key]
		digest := sha256.Sum256([]byte(expected.GrantID + "\x00" + expected.CatalogFingerprint))
		expected.ProofDigest = digest[:]
		stored, err := opened.B5DMLGrants().Get(ctx, expected.GrantID)
		if errors.Is(err, store.ErrNotFound) {
			if verify {
				return 0, "", fmt.Errorf("demo seed verify: B5 grant %q is missing", expected.GrantID)
			}
			if _, err = opened.B5DMLGrants().Create(ctx, expected); err != nil {
				return 0, "", fmt.Errorf("create demo B5 grant %q: %w", expected.GrantID, err)
			}
			continue
		}
		if err != nil || !sameB5Grant(expected, stored) {
			return 0, "", fmt.Errorf("demo seed conflict: B5 grant %q differs", expected.GrantID)
		}
	}
	rows, err := opened.B5DMLGrants().List(ctx, manifest.AgentID, manifest.DatasourceID, b5.ActionUpdate, 1000)
	if err != nil || len(rows) != len(keys) {
		return 0, "", fmt.Errorf("demo seed B5 grant count mismatch: grants=%d want=%d", len(rows), len(keys))
	}
	return len(keys), mode, nil
}

func putB5Grant(target map[string]store.B5DMLGrant, key string, grant store.B5DMLGrant) error {
	if existing, ok := target[key]; ok && (existing.DatabaseOID != grant.DatabaseOID || existing.RelationOID != grant.RelationOID ||
		existing.CatalogFingerprint != grant.CatalogFingerprint || existing.RelationKind != grant.RelationKind) {
		return fmt.Errorf("demo B5 grant %q has inconsistent catalog identity across statements", key)
	}
	digest := sha256.Sum256([]byte(key))
	grant.GrantID = "grant-demo-b5-" + hex.EncodeToString(digest[:8])
	target[key] = grant
	return nil
}
func setB5GrantColumn(grant *store.B5DMLGrant, attnum int16, name string, oid uint32, modifier int32, collation uint32) {
	a, m := int(attnum), int(modifier)
	grant.ColumnAttnum, grant.ColumnName, grant.ColumnTypeOID = &a, &name, &oid
	grant.ColumnTypeModifier, grant.ColumnCollationOID = &m, &collation
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
		if verify {
			return fmt.Errorf("demo seed verify: mask rule %q differs", expected.ID)
		}
		if _, updateErr := opened.MaskRules().Update(ctx, expected); updateErr != nil {
			return fmt.Errorf("update demo mask rule %q: operation failed", expected.ID)
		}
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
	// The running gateway may append operational audits (for example protocol-3
	// activation) before verify-only executes. Verify the fixed daily demo
	// namespace exactly while preserving and ignoring unrelated runtime rows.
	bySession := make(map[string]model.AuditLog, len(expectedAudits))
	for _, stored := range page.List {
		if stored.SessionID == nil || !strings.HasPrefix(*stored.SessionID, store.DemoSeedSessionPrefix) {
			continue
		}
		if _, duplicate := bySession[*stored.SessionID]; duplicate {
			return fmt.Errorf("demo seed history contains duplicate session_id %q", *stored.SessionID)
		}
		bySession[*stored.SessionID] = stored
	}
	if len(bySession) != len(expectedAudits) {
		return fmt.Errorf("demo seed history count mismatch: audits=%d want=%d", len(bySession), len(expectedAudits))
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
	if err := ValidateHistory(recordedInSeedOrder, mustApprovalShapeForCounts(expectedApprovals), anchor); err != nil {
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
	return model.MaskRule{ID: item.ID, DatasourceID: &datasourceID, SchemaName: item.SchemaName, TableName: item.TableName,
		ColumnName: item.ColumnName, SensitiveType: item.SensitiveType, Algo: item.Algo, Enabled: true}
}

func policyModel(item PolicyManifest) model.Policy {
	var columns *string
	if item.Columns != "" {
		value := item.Columns
		columns = &value
	}
	return model.Policy{ID: item.ID, AgentID: item.AgentID, DatasourceID: item.DatasourceID,
		ObjectType: item.ObjectType, ObjectName: item.ObjectName, Columns: columns, Action: item.Action}
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
		expected.SchemaName == stored.SchemaName && expected.TableName == stored.TableName &&
		expected.ColumnName == stored.ColumnName && expected.SensitiveType == stored.SensitiveType &&
		expected.Algo == stored.Algo && expected.Enabled == stored.Enabled &&
		equalInt64(expected.RangeBucketWidth, stored.RangeBucketWidth) &&
		equalInt64(expected.RangeBucketOffset, stored.RangeBucketOffset) &&
		equalString(expected.RangeGranularity, stored.RangeGranularity)
}

func samePolicy(expected, stored model.Policy) bool {
	if !(expected.ID == stored.ID && expected.AgentID == stored.AgentID && expected.DatasourceID == stored.DatasourceID &&
		expected.ObjectType == stored.ObjectType && expected.ObjectName == stored.ObjectName &&
		equalString(expected.Columns, stored.Columns) && equalString(expected.RowFilter, stored.RowFilter) && expected.Action == stored.Action &&
		equalString(expected.RelationBindingID, stored.RelationBindingID)) {
		return false
	}
	if (expected.RelationBinding == nil) != (stored.RelationBinding == nil) || len(expected.ColumnPermissions) != len(stored.ColumnPermissions) {
		return false
	}
	if expected.RelationBinding != nil {
		left, right := expected.RelationBinding, stored.RelationBinding
		if left.ID != right.ID || left.SchemaName != right.SchemaName || left.RelationName != right.RelationName ||
			!equalString(left.StableObjectID, right.StableObjectID) || !equalString(left.CatalogFingerprint, right.CatalogFingerprint) || left.Status != right.Status {
			return false
		}
	}
	for index := range expected.ColumnPermissions {
		left, right := expected.ColumnPermissions[index], stored.ColumnPermissions[index]
		if left.RelationEnrollmentID != right.RelationEnrollmentID || left.ColumnOrdinal != right.ColumnOrdinal ||
			left.ColumnName != right.ColumnName || left.ColumnTypeDigest != right.ColumnTypeDigest || left.Usage != right.Usage {
			return false
		}
	}
	return true
}

func sameB5Grant(expected, stored store.B5DMLGrant) bool {
	return expected.GrantID == stored.GrantID && expected.PolicyID == stored.PolicyID && expected.PolicyRevision == stored.PolicyRevision &&
		expected.PrincipalID == stored.PrincipalID && expected.DatasourceID == stored.DatasourceID && expected.Effect == stored.Effect &&
		expected.Element == stored.Element && expected.Action == stored.Action && expected.DatabaseOID == stored.DatabaseOID &&
		expected.RelationOID == stored.RelationOID && expected.RelationKind == stored.RelationKind && expected.SchemaName == stored.SchemaName &&
		expected.RelationName == stored.RelationName && expected.CatalogFingerprint == stored.CatalogFingerprint &&
		equalString(expected.WriteTargetKind, stored.WriteTargetKind) && equalInt(expected.ColumnAttnum, stored.ColumnAttnum) &&
		equalString(expected.ColumnName, stored.ColumnName) && equalUint32(expected.ColumnTypeOID, stored.ColumnTypeOID) &&
		equalInt(expected.ColumnTypeModifier, stored.ColumnTypeModifier) && equalUint32(expected.ColumnCollationOID, stored.ColumnCollationOID) &&
		equalString(expected.ReferenceKind, stored.ReferenceKind) && expected.ProofSchemaID == stored.ProofSchemaID &&
		expected.ProofSchemaVersion == stored.ProofSchemaVersion && bytes.Equal(expected.ProofDigest, stored.ProofDigest)
}

func sameRule(expected, stored model.Rule) bool {
	return expected.ID == stored.ID && expected.DBType == stored.DBType && expected.Title == stored.Title &&
		expected.RiskLevel == stored.RiskLevel && expected.PatternType == stored.PatternType &&
		expected.Definition == stored.Definition && expected.Enabled == stored.Enabled && expected.Builtin == stored.Builtin
}

func sameAudit(expected, stored model.AuditLog) bool {
	if expected.TenantID == "" {
		expected.TenantID = store.DefaultTenantID
	}
	if !expected.TS.Equal(stored.TS) {
		return false
	}
	expected.ID = stored.ID
	expected.TS = stored.TS
	return reflect.DeepEqual(expected, stored)
}

func sameApproval(expected, stored model.Approval) bool {
	if expected.TenantID == "" {
		expected.TenantID = store.DefaultTenantID
	}
	return expected.ID == stored.ID && expected.TenantID == stored.TenantID && equalInt64(expected.AuditID, stored.AuditID) &&
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
func equalInt(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
func equalUint32(left, right *uint32) bool {
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
