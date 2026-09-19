package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAgentRepositoryCRUDAndHashedKeyStorage(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.Agents()
	plaintext, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	created, err := repository.Create(context.Background(), model.Agent{
		ID:         "ag_crud",
		Name:       "Read Agent",
		Owner:      pointer("DBA"),
		Status:     "active",
		APIKeyHash: hash,
		Level:      "readonly",
		ExpiresAt:  &expiresAt,
	})
	require.NoError(t, err)
	require.Equal(t, "ag_crud", created.ID)
	require.Equal(t, hash, created.APIKeyHash)
	require.NotEqual(t, plaintext, created.APIKeyHash)
	require.False(t, created.CreatedAt.IsZero())
	require.False(t, created.UpdatedAt.IsZero())

	var storedKey string
	require.NoError(t, opened.metaDB.QueryRow(
		"SELECT api_key_hash FROM agents WHERE id = ?",
		created.ID,
	).Scan(&storedKey))
	require.Equal(t, hash, storedKey)
	require.NotContains(t, storedKey, plaintext)
	_, err = repository.Create(context.Background(), model.Agent{
		ID:         "ag_plaintext",
		Name:       "Unsafe Agent",
		Status:     "active",
		APIKeyHash: plaintext,
		Level:      "readonly",
	})
	require.True(t, errors.Is(err, errInvalidAPIKeyHash))
	var plaintextCount int
	require.NoError(t, opened.metaDB.QueryRow(
		"SELECT COUNT(*) FROM agents WHERE api_key_hash = ?",
		plaintext,
	).Scan(&plaintextCount))
	require.Zero(t, plaintextCount)

	read, err := repository.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, created.APIKeyHash, read.APIKeyHash)
	require.Equal(t, "DBA", *read.Owner)

	read.Name = "Write Agent"
	read.Owner = nil
	read.Status = "disabled"
	read.Level = "dml"
	read.ExpiresAt = nil
	updated, err := repository.Update(context.Background(), read)
	require.NoError(t, err)
	require.Equal(t, "Write Agent", updated.Name)
	require.Nil(t, updated.Owner)
	require.Equal(t, "disabled", updated.Status)
	require.Nil(t, updated.ExpiresAt)

	require.NoError(t, repository.Delete(context.Background(), created.ID))
	_, err = repository.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestDatasourceRepositoryCRUDAndEncryptedPasswordStorage(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.Datasources()
	firstPassword := "plaintext-password-one"
	datasource := model.Datasource{
		ID:            "ds_crud",
		Name:          "Primary",
		DBType:        "postgres",
		Host:          "db.internal",
		Port:          5432,
		Database:      "app",
		Username:      "gateway",
		ConnLimit:     5,
		StmtTimeoutMS: 5000,
		RowLimit:      1000,
	}

	created, err := repository.Create(context.Background(), datasource, firstPassword)
	require.NoError(t, err)
	require.NotEmpty(t, created.PasswordEnc)
	require.NotEqual(t, firstPassword, created.PasswordEnc)
	decrypted, err := repository.DecryptPassword(created.PasswordEnc)
	require.NoError(t, err)
	require.Equal(t, firstPassword, decrypted)

	var storedPassword string
	require.NoError(t, opened.metaDB.QueryRow(
		"SELECT password_enc FROM datasources WHERE id = ?",
		created.ID,
	).Scan(&storedPassword))
	require.Equal(t, created.PasswordEnc, storedPassword)
	require.NotContains(t, storedPassword, firstPassword)

	read, err := repository.Get(context.Background(), created.ID)
	require.NoError(t, err)
	read.Name = "Replica"
	read.Port = 3306
	read.DBType = "mysql"
	secondPassword := "plaintext-password-two"
	updated, err := repository.Update(context.Background(), read, secondPassword)
	require.NoError(t, err)
	require.Equal(t, "Replica", updated.Name)
	require.Equal(t, 3306, updated.Port)
	require.NotEqual(t, read.PasswordEnc, updated.PasswordEnc)
	decrypted, err = repository.DecryptPassword(updated.PasswordEnc)
	require.NoError(t, err)
	require.Equal(t, secondPassword, decrypted)

	require.NoError(t, repository.Delete(context.Background(), created.ID))
	_, err = repository.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestPolicyRepositoryCRUD(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	repository := opened.Policies()
	policy := model.Policy{
		ID:           "policy_crud",
		AgentID:      agent.ID,
		DatasourceID: datasource.ID,
		ObjectType:   "table",
		ObjectName:   "public.orders",
		Columns:      pointer("id,total"),
		RowFilter:    pointer("tenant_id = 1"),
		Action:       "allow",
	}

	created, err := repository.Create(context.Background(), policy)
	require.NoError(t, err)
	require.Equal(t, "id,total", *created.Columns)
	read, err := repository.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "allow", read.Action)

	read.Columns = nil
	read.RowFilter = nil
	read.Action = "deny"
	updated, err := repository.Update(context.Background(), read)
	require.NoError(t, err)
	require.Nil(t, updated.Columns)
	require.Nil(t, updated.RowFilter)
	require.Equal(t, "deny", updated.Action)

	require.NoError(t, repository.Delete(context.Background(), created.ID))
	_, err = repository.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestRuleRepositoryCRUD(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.Rules()
	rule := model.Rule{
		ID:          "R001",
		DBType:      "all",
		Title:       "Multiple statements",
		RiskLevel:   1,
		PatternType: "ast_match",
		Definition:  `{"multi":true}`,
		Enabled:     true,
		Builtin:     true,
	}

	created, err := repository.Create(context.Background(), rule)
	require.NoError(t, err)
	require.True(t, created.Enabled)
	read, err := repository.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, rule.Definition, read.Definition)

	read.Title = "Updated title"
	read.RiskLevel = 2
	read.Enabled = false
	updated, err := repository.Update(context.Background(), read)
	require.NoError(t, err)
	require.Equal(t, "Updated title", updated.Title)
	require.Equal(t, 2, updated.RiskLevel)
	require.False(t, updated.Enabled)

	require.NoError(t, repository.Delete(context.Background(), created.ID))
	_, err = repository.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestMaskRuleRepositoryCRUD(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.MaskRules()
	rule := model.MaskRule{
		ID:            "mask_crud",
		DatasourceID:  pointer("ds_optional"),
		TableName:     "customers",
		ColumnName:    "phone",
		SensitiveType: "phone",
		Algo:          "mask",
		Enabled:       true,
	}

	created, err := repository.Create(context.Background(), rule)
	require.NoError(t, err)
	require.Equal(t, "ds_optional", *created.DatasourceID)
	read, err := repository.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "phone", read.SensitiveType)
	require.True(t, read.Enabled)

	read.DatasourceID = nil
	read.ColumnName = "email"
	read.SensitiveType = "email"
	read.Enabled = false
	updated, err := repository.Update(context.Background(), read)
	require.NoError(t, err)
	require.Nil(t, updated.DatasourceID)
	require.Equal(t, "email", updated.ColumnName)
	require.False(t, updated.Enabled)

	require.NoError(t, repository.Delete(context.Background(), created.ID))
	_, err = repository.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestMaskRuleRepositoryRangeFieldsRoundTripAndClear(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.MaskRules()
	ctx := context.Background()
	width, zero := int64(10), int64(0)
	month := "month"

	numberRule, err := repository.Create(ctx, model.MaskRule{
		ID: "range-number", DatasourceID: pointer("ds-range"), TableName: "orders",
		ColumnName: "amount", SensitiveType: "number", Algo: "range", Enabled: true,
		RangeBucketWidth: &width, RangeBucketOffset: &zero,
	})
	require.NoError(t, err)
	require.Equal(t, int64(10), *numberRule.RangeBucketWidth)
	require.NotNil(t, numberRule.RangeBucketOffset)
	require.Zero(t, *numberRule.RangeBucketOffset)
	require.Nil(t, numberRule.RangeGranularity)

	dateRule, err := repository.Create(ctx, model.MaskRule{
		ID: "range-date", DatasourceID: pointer("ds-range"), TableName: "orders",
		ColumnName: "created_at", SensitiveType: "date", Algo: "range", Enabled: true,
		RangeGranularity: &month,
	})
	require.NoError(t, err)
	require.Nil(t, dateRule.RangeBucketWidth)
	require.Nil(t, dateRule.RangeBucketOffset)
	require.Equal(t, "month", *dateRule.RangeGranularity)

	readNumber, err := repository.Get(ctx, numberRule.ID)
	require.NoError(t, err)
	require.Equal(t, int64(10), *readNumber.RangeBucketWidth)
	require.NotNil(t, readNumber.RangeBucketOffset)
	require.Zero(t, *readNumber.RangeBucketOffset)
	listed, err := repository.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, "range-number", listed[0].ID)
	require.Zero(t, *listed[0].RangeBucketOffset)
	require.Equal(t, "range-date", listed[1].ID)
	require.Equal(t, "month", *listed[1].RangeGranularity)

	quarter := "quarter"
	readNumber.SensitiveType = "date"
	readNumber.RangeGranularity = &quarter
	switched, err := repository.Update(ctx, readNumber)
	require.NoError(t, err)
	require.Nil(t, switched.RangeBucketWidth)
	require.Nil(t, switched.RangeBucketOffset)
	require.Equal(t, "quarter", *switched.RangeGranularity)

	switched.Algo = "block"
	switched.SensitiveType = "generic"
	switched.RangeBucketWidth = &width
	switched.RangeBucketOffset = &zero
	switched.RangeGranularity = &month
	blocked, err := repository.Update(ctx, switched)
	require.NoError(t, err)
	require.Nil(t, blocked.RangeBucketWidth)
	require.Nil(t, blocked.RangeBucketOffset)
	require.Nil(t, blocked.RangeGranularity)
}

func TestMaskRuleRepositoryClassifiesNormalizedKeyConflict(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.MaskRules()
	_, err := repository.Create(context.Background(), model.MaskRule{
		ID: "first", DatasourceID: pointer(" ds-1 "), TableName: "users",
		ColumnName: " Email ", SensitiveType: "email", Algo: "mask", Enabled: true,
	})
	require.NoError(t, err)
	_, err = repository.Create(context.Background(), model.MaskRule{
		ID: "different-table", DatasourceID: pointer("ds-1"), TableName: "customers",
		ColumnName: "email", SensitiveType: "email", Algo: "mask", Enabled: false,
	})
	require.NoError(t, err)
	_, err = repository.Create(context.Background(), model.MaskRule{
		ID: "second", DatasourceID: pointer("ds-1"), TableName: "users",
		ColumnName: "email", SensitiveType: "email", Algo: "mask", Enabled: false,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMaskRuleConflict)
	require.True(t, IsMaskRuleConflict(err))
	require.ErrorContains(t, err, `scope="ds-1" schema="" table="users" column="email"`)

	third, err := repository.Create(context.Background(), model.MaskRule{
		ID: "third", DatasourceID: pointer("ds-1"), TableName: "customers",
		ColumnName: "phone", SensitiveType: "phone", Algo: "mask", Enabled: true,
	})
	require.NoError(t, err)
	third.TableName = "users"
	third.ColumnName = " EMAIL "
	_, err = repository.Update(context.Background(), third)
	require.ErrorIs(t, err, ErrMaskRuleConflict)
}

func TestMaskRuleRepositoryPersistsPhysicalScopeAndCoalescesNullScope(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.MaskRules()
	scope := pointer("ds-physical")

	first, err := repository.Create(ctx, model.MaskRule{
		ID: "physical-a", DatasourceID: scope, SchemaName: "Tenant", TableName: "customers",
		ColumnName: " Email ", SensitiveType: "email", Algo: "mask", Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "Tenant", first.SchemaName)
	require.Equal(t, "customers", first.TableName)

	_, err = repository.Create(ctx, model.MaskRule{
		ID: "physical-b", DatasourceID: scope, SchemaName: "tenant", TableName: "customers",
		ColumnName: "email", SensitiveType: "email", Algo: "mask", Enabled: true,
	})
	require.NoError(t, err, "schema comparison is exact and case-sensitive")
	_, err = repository.Create(ctx, model.MaskRule{
		ID: "physical-c", DatasourceID: scope, SchemaName: "Tenant", TableName: "accounts",
		ColumnName: "email", SensitiveType: "email", Algo: "mask", Enabled: true,
	})
	require.NoError(t, err, "different tables may use the same normalized column")

	_, err = opened.metaDB.ExecContext(ctx, `UPDATE mask_rules SET schema_name=NULL WHERE id='physical-a'`)
	require.NoError(t, err)
	first, err = repository.Get(ctx, "physical-a")
	require.NoError(t, err)
	require.Empty(t, first.SchemaName, "repository scans legacy NULL scope safely")
	first, err = repository.Update(ctx, first)
	require.NoError(t, err)
	var schemaIsNull, tableIsNull bool
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT schema_name IS NULL, table_name IS NULL FROM mask_rules WHERE id='physical-a'`).Scan(&schemaIsNull, &tableIsNull))
	require.False(t, schemaIsNull)
	require.False(t, tableIsNull)
}

func TestAuditLogRepositoryInsertAndPageOnly(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.AuditLogs()
	first, err := repository.Insert(context.Background(), model.AuditLog{
		AgentID:      pointer("ag_audit"),
		DatasourceID: pointer("ds_audit"),
		MCPTool:      pointer("query"),
		SQLRaw:       pointer("SELECT email FROM customers"),
		SQLNorm:      pointer("SELECT email FROM customers"),
		StmtType:     pointer("SELECT"),
		Decision:     "allow",
		RiskLevel:    pointer(4),
		RowsReturned: pointer(1),
		LatencyMS:    pointer(int64(5)),
		Action:       pointer("discover"),
		ActorType:    pointer("admin"),
		ActorID:      pointer(""),
		DetailsJSON:  pointer(`{"findings_count":2}`),
	})
	require.NoError(t, err)
	require.Positive(t, first.ID)
	require.False(t, first.TS.IsZero())
	require.Equal(t, "discover", *first.Action)
	require.Equal(t, "admin", *first.ActorType)
	require.NotNil(t, first.ActorID)
	require.Empty(t, *first.ActorID)
	require.Equal(t, `{"findings_count":2}`, *first.DetailsJSON)

	second, err := repository.Insert(context.Background(), model.AuditLog{
		Decision: "deny",
		ErrorMsg: pointer("blocked"),
	})
	require.NoError(t, err)
	require.Greater(t, second.ID, first.ID)
	require.Nil(t, second.Action)
	require.Nil(t, second.ActorType)
	require.Nil(t, second.ActorID)
	require.Nil(t, second.DetailsJSON)

	page, err := repository.Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 1, page.PageSize)
	require.Len(t, page.List, 1)
	require.Equal(t, second.ID, page.List[0].ID)

	repositoryType := reflect.TypeOf(repository)
	_, hasUpdate := repositoryType.MethodByName("Update")
	_, hasDelete := repositoryType.MethodByName("Delete")
	require.False(t, hasUpdate)
	require.False(t, hasDelete)
}

func TestApprovalRepositoryCRUD(t *testing.T) {
	opened := openTestStore(t)
	auditLog, err := opened.AuditLogs().Insert(context.Background(), model.AuditLog{
		Decision: "approve",
	})
	require.NoError(t, err)
	repository := opened.Approvals()
	approval := model.Approval{
		ID:      "approval_crud",
		AuditID: &auditLog.ID,
		AgentID: pointer("ag_approval"),
		SQLRaw:  pointer("UPDATE orders SET status = 'done' WHERE id = 1"),
		Reason:  pointer("complete order"),
		Status:  "pending",
	}

	created, err := repository.Create(context.Background(), approval)
	require.NoError(t, err)
	require.Equal(t, "pending", created.Status)
	read, err := repository.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, auditLog.ID, *read.AuditID)

	decidedAt := time.Now().UTC().Truncate(time.Second)
	read.Status = "approved"
	read.Approver = pointer("security-dba")
	read.DecidedAt = &decidedAt
	updated, err := repository.Update(context.Background(), read)
	require.NoError(t, err)
	require.Equal(t, "approved", updated.Status)
	require.Equal(t, "security-dba", *updated.Approver)
	require.NotNil(t, updated.DecidedAt)

	require.NoError(t, repository.Delete(context.Background(), created.ID))
	_, err = repository.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ErrNotFound))
}
