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
	require.NoError(t, opened.db.QueryRow(
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
	require.NoError(t, opened.db.QueryRow(
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
	require.NoError(t, opened.db.QueryRow(
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
	}

	created, err := repository.Create(context.Background(), rule)
	require.NoError(t, err)
	require.Equal(t, "ds_optional", *created.DatasourceID)
	read, err := repository.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "phone", read.SensitiveType)

	read.DatasourceID = nil
	read.ColumnName = "email"
	read.SensitiveType = "email"
	updated, err := repository.Update(context.Background(), read)
	require.NoError(t, err)
	require.Nil(t, updated.DatasourceID)
	require.Equal(t, "email", updated.ColumnName)

	require.NoError(t, repository.Delete(context.Background(), created.ID))
	_, err = repository.Get(context.Background(), created.ID)
	require.True(t, errors.Is(err, ErrNotFound))
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
	})
	require.NoError(t, err)
	require.Positive(t, first.ID)
	require.False(t, first.TS.IsZero())

	second, err := repository.Insert(context.Background(), model.AuditLog{
		Decision: "deny",
		ErrorMsg: pointer("blocked"),
	})
	require.NoError(t, err)
	require.Greater(t, second.ID, first.ID)

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
