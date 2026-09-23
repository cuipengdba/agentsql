package adminapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

const maxJSONBodyBytes = 1 << 20

type apiResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

type pageResponse struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	List     any   `json:"list"`
}

type agentView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Owner     *string    `json:"owner,omitempty"`
	Status    string     `json:"status"`
	Level     string     `json:"level"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	APIKey    string     `json:"api_key,omitempty"`
}

type agentCreateInput struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Owner     *string    `json:"owner,omitempty"`
	Status    string     `json:"status,omitempty"`
	Level     string     `json:"level"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type nullableTime struct {
	Present bool
	Value   *time.Time
}

func (value *nullableTime) UnmarshalJSON(data []byte) error {
	value.Present = true
	value.Value = nil
	if string(data) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return err
	}
	value.Value = &parsed
	return nil
}

type agentUpdateInput struct {
	Name      *string      `json:"name,omitempty"`
	Owner     *string      `json:"owner,omitempty"`
	Status    *string      `json:"status,omitempty"`
	Level     *string      `json:"level,omitempty"`
	ExpiresAt nullableTime `json:"expires_at,omitempty"`
}

type datasourceView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DBType      string `json:"db_type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Database    string `json:"database"`
	Username    string `json:"username"`
	ConnLimit   int    `json:"conn_limit"`
	StmtTimeout int    `json:"stmt_timeout_ms"`
	RowLimit    int    `json:"row_limit"`
	HasPassword bool   `json:"has_password"`
}

type datasourceInput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DBType        string `json:"db_type"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Database      string `json:"database"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	ConnLimit     int    `json:"conn_limit"`
	StmtTimeoutMS int    `json:"stmt_timeout_ms"`
	RowLimit      int    `json:"row_limit"`
}

type discoveryTableInput struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

type discoveryInput struct {
	Tables     []discoveryTableInput `json:"tables"`
	Sampling   *bool                 `json:"sampling,omitempty"`
	SampleRows *int                  `json:"sample_rows,omitempty"`
	Categories []string              `json:"categories,omitempty"`
}

type discoveryApplyItemInput struct {
	Schema        string               `json:"schema"`
	Table         string               `json:"table"`
	Column        string               `json:"column"`
	Category      string               `json:"category"`
	SensitiveType string               `json:"sensitive_type"`
	Algo          string               `json:"algo"`
	Range         *discoveryApplyRange `json:"range,omitempty"`
}

type discoveryApplyRange struct {
	BucketWidth  *int64 `json:"bucket_width,omitempty"`
	BucketOffset *int64 `json:"bucket_offset,omitempty"`
	Granularity  string `json:"granularity,omitempty"`
}

type discoveryApplyInput struct {
	Items []discoveryApplyItemInput `json:"items"`
}

type discoveryApplyItemView struct {
	Schema        string               `json:"schema,omitempty"`
	Table         string               `json:"table,omitempty"`
	Column        string               `json:"column"`
	Category      string               `json:"category,omitempty"`
	SensitiveType string               `json:"sensitive_type"`
	Algo          string               `json:"algo"`
	Range         *discoveryApplyRange `json:"range,omitempty"`
	RuleID        string               `json:"rule_id,omitempty"`
}

type discoveryApplyCounts struct {
	Requested       int `json:"requested"`
	Created         int `json:"created"`
	Existing        int `json:"existing"`
	CoveredByGlobal int `json:"covered_by_global"`
	Conflicts       int `json:"conflicts"`
	Ambiguous       int `json:"ambiguous"`
}

type discoveryApplyResponse struct {
	Created         []discoveryApplyItemView `json:"created"`
	Existing        []discoveryApplyItemView `json:"existing"`
	CoveredByGlobal []discoveryApplyItemView `json:"covered_by_global"`
	Conflicts       []discoveryApplyItemView `json:"conflicts"`
	Ambiguous       []discoveryApplyItemView `json:"ambiguous"`
	Counts          discoveryApplyCounts     `json:"counts"`
}

type policyInput struct {
	ID           string  `json:"id"`
	AgentID      string  `json:"agent_id"`
	DatasourceID string  `json:"datasource_id"`
	ObjectType   string  `json:"object_type"`
	ObjectName   string  `json:"object_name"`
	Columns      *string `json:"columns,omitempty"`
	RowFilter    *string `json:"row_filter,omitempty"`
	Action       string  `json:"action"`
}

type ruleInput struct {
	ID          string `json:"id,omitempty"`
	DBType      string `json:"db_type,omitempty"`
	Title       string `json:"title,omitempty"`
	RiskLevel   int    `json:"risk_level,omitempty"`
	PatternType string `json:"pattern_type,omitempty"`
	Definition  string `json:"definition,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Builtin     *bool  `json:"builtin,omitempty"`
}

type maskRuleInput struct {
	ID                string  `json:"id"`
	DatasourceID      *string `json:"datasource_id,omitempty"`
	SchemaName        string  `json:"schema_name"`
	TableName         string  `json:"table_name"`
	ColumnName        string  `json:"column_name"`
	SensitiveType     string  `json:"sensitive_type"`
	Algo              string  `json:"algo"`
	Enabled           *bool   `json:"enabled,omitempty"`
	RangeBucketWidth  *int64  `json:"range_bucket_width,omitempty"`
	RangeBucketOffset *int64  `json:"range_bucket_offset,omitempty"`
	RangeGranularity  *string `json:"range_granularity,omitempty"`
}

type decideInput struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment,omitempty"`
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	if request == nil || request.Body == nil {
		return errors.New("request body is required")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errTrailingJSON
		}
		return err
	}
	return nil
}

var errTrailingJSON = errors.New("trailing JSON value")

func parsePage(request *http.Request) (page, size int, err error) {
	page, size = 1, 20
	values := request.URL.Query()
	if text := values.Get("page"); text != "" {
		page, err = strconv.Atoi(text)
		if err != nil || page < 1 {
			return 0, 0, errInvalidPage
		}
	}
	if text := values.Get("page_size"); text != "" {
		size, err = strconv.Atoi(text)
		if err != nil || size < 1 || size > 100 {
			return 0, 0, errInvalidPageSize
		}
	}
	return page, size, nil
}

var (
	errInvalidPage     = &apiValidationError{"page must be at least 1"}
	errInvalidPageSize = &apiValidationError{"page_size must be between 1 and 100"}
)

type apiValidationError struct{ message string }

func (err *apiValidationError) Error() string { return err.message }

func paginate[T any](items []T, page, size int) pageResponse {
	start := (page - 1) * size
	if start > len(items) {
		start = len(items)
	}
	end := start + size
	if end > len(items) {
		end = len(items)
	}
	list := append([]T(nil), items[start:end]...)
	if list == nil {
		list = make([]T, 0)
	}
	return pageResponse{Total: int64(len(items)), Page: page, PageSize: size, List: list}
}

func agentToView(agent model.Agent, plaintext string) agentView {
	return agentView{ID: agent.ID, Name: agent.Name, Owner: agent.Owner, Status: agent.Status,
		Level: agent.Level, ExpiresAt: agent.ExpiresAt, CreatedAt: agent.CreatedAt, UpdatedAt: agent.UpdatedAt,
		APIKey: plaintext}
}

func datasourceToView(datasource model.Datasource) datasourceView {
	return datasourceView{ID: datasource.ID, Name: datasource.Name, DBType: datasource.DBType,
		Host: datasource.Host, Port: datasource.Port, Database: datasource.Database, Username: datasource.Username,
		ConnLimit: datasource.ConnLimit, StmtTimeout: datasource.StmtTimeoutMS, RowLimit: datasource.RowLimit,
		HasPassword: datasource.PasswordEnc != ""}
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// auditView is the snake_case projection of model.AuditLog for the console and
// for JSONL export; the store layer's PascalCase struct is never serialized.
type auditView struct {
	ID             int64     `json:"id"`
	TS             time.Time `json:"ts"`
	AgentID        *string   `json:"agent_id,omitempty"`
	DatasourceID   *string   `json:"datasource_id,omitempty"`
	SessionID      *string   `json:"session_id,omitempty"`
	ConversationID *string   `json:"conversation_id,omitempty"`
	MCPTool        *string   `json:"mcp_tool,omitempty"`
	DBType         *string   `json:"db_type,omitempty"`
	SQLRaw         *string   `json:"sql_raw,omitempty"`
	SQLNorm        *string   `json:"sql_norm,omitempty"`
	StmtType       *string   `json:"stmt_type,omitempty"`
	Objects        *string   `json:"objects,omitempty"`
	Decision       string    `json:"decision"`
	RuleHits       *string   `json:"rule_hits,omitempty"`
	RiskLevel      *int      `json:"risk_level,omitempty"`
	EstRows        *int64    `json:"est_rows,omitempty"`
	RowsReturned   *int      `json:"rows_returned,omitempty"`
	LatencyMS      *int64    `json:"latency_ms,omitempty"`
	ClientIP       *string   `json:"client_ip,omitempty"`
	ModelName      *string   `json:"model_name,omitempty"`
	ErrorMsg       *string   `json:"error_msg,omitempty"`
	ErrorCode      *string   `json:"error_code,omitempty"`
	Action         *string   `json:"action,omitempty"`
	ActorType      *string   `json:"actor_type,omitempty"`
	ActorID        *string   `json:"actor_id,omitempty"`
	DetailsJSON    *string   `json:"details_json,omitempty"`
}

func auditToView(log model.AuditLog) auditView {
	return auditView{
		ID: log.ID, TS: log.TS, AgentID: log.AgentID, DatasourceID: log.DatasourceID,
		SessionID: log.SessionID, ConversationID: log.ConversationID, MCPTool: log.MCPTool,
		DBType: log.DBType, SQLRaw: log.SQLRaw, SQLNorm: log.SQLNorm, StmtType: log.StmtType,
		Objects: log.Objects, Decision: log.Decision, RuleHits: log.RuleHits, RiskLevel: log.RiskLevel,
		EstRows: log.EstRows, RowsReturned: log.RowsReturned, LatencyMS: log.LatencyMS,
		ClientIP: log.ClientIP, ModelName: log.ModelName, ErrorMsg: log.ErrorMsg, ErrorCode: log.ErrorCode,
		Action: log.Action, ActorType: log.ActorType, ActorID: log.ActorID, DetailsJSON: log.DetailsJSON,
	}
}

func auditToViews(logs []model.AuditLog) []auditView {
	views := make([]auditView, 0, len(logs))
	for _, log := range logs {
		views = append(views, auditToView(log))
	}
	return views
}

// auditStreamView is the explicit safe allow-list for realtime events.
type auditStreamView struct {
	ID           int64     `json:"id"`
	TS           time.Time `json:"ts"`
	AgentID      *string   `json:"agent_id,omitempty"`
	DatasourceID *string   `json:"datasource_id,omitempty"`
	MCPTool      *string   `json:"mcp_tool,omitempty"`
	DBType       *string   `json:"db_type,omitempty"`
	StmtType     *string   `json:"stmt_type,omitempty"`
	Objects      *string   `json:"objects,omitempty"`
	Decision     string    `json:"decision"`
	RuleHits     *string   `json:"rule_hits,omitempty"`
	RiskLevel    *int      `json:"risk_level,omitempty"`
	EstRows      *int64    `json:"est_rows,omitempty"`
	RowsReturned *int      `json:"rows_returned,omitempty"`
	LatencyMS    *int64    `json:"latency_ms,omitempty"`
	ModelName    *string   `json:"model_name,omitempty"`
	ErrorCode    *string   `json:"error_code,omitempty"`
	Action       *string   `json:"action,omitempty"`
	ActorType    *string   `json:"actor_type,omitempty"`
	ActorID      *string   `json:"actor_id,omitempty"`
	DetailsJSON  *string   `json:"details_json,omitempty"`
}

func auditToStreamView(log model.AuditLog) auditStreamView {
	return auditStreamView{
		ID: log.ID, TS: log.TS, AgentID: log.AgentID, DatasourceID: log.DatasourceID,
		MCPTool: log.MCPTool, DBType: log.DBType, StmtType: log.StmtType, Objects: log.Objects,
		Decision: log.Decision, RuleHits: log.RuleHits, RiskLevel: log.RiskLevel,
		EstRows: log.EstRows, RowsReturned: log.RowsReturned, LatencyMS: log.LatencyMS,
		ModelName: log.ModelName, ErrorCode: log.ErrorCode,
		Action: log.Action, ActorType: log.ActorType, ActorID: log.ActorID, DetailsJSON: log.DetailsJSON,
	}
}

type approvalView struct {
	ID        string     `json:"id"`
	AuditID   *int64     `json:"audit_id,omitempty"`
	AgentID   *string    `json:"agent_id,omitempty"`
	SQLRaw    *string    `json:"sql_raw,omitempty"`
	Reason    *string    `json:"reason,omitempty"`
	Status    string     `json:"status"`
	Approver  *string    `json:"approver,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func approvalToView(approval model.Approval) approvalView {
	return approvalView{
		ID: approval.ID, AuditID: approval.AuditID, AgentID: approval.AgentID, SQLRaw: approval.SQLRaw,
		Reason: approval.Reason, Status: approval.Status, Approver: approval.Approver, DecidedAt: approval.DecidedAt,
		CreatedAt: approval.CreatedAt, UpdatedAt: approval.UpdatedAt,
	}
}

func approvalToViews(items []model.Approval) []approvalView {
	views := make([]approvalView, 0, len(items))
	for _, item := range items {
		views = append(views, approvalToView(item))
	}
	return views
}

type policyView struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	DatasourceID string    `json:"datasource_id"`
	ObjectType   string    `json:"object_type"`
	ObjectName   string    `json:"object_name"`
	Columns      *string   `json:"columns,omitempty"`
	RowFilter    *string   `json:"row_filter,omitempty"`
	Action       string    `json:"action"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func policyToView(policy model.Policy) policyView {
	return policyView{
		ID: policy.ID, AgentID: policy.AgentID, DatasourceID: policy.DatasourceID, ObjectType: policy.ObjectType,
		ObjectName: policy.ObjectName, Columns: policy.Columns, RowFilter: policy.RowFilter, Action: policy.Action,
		CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
	}
}

func policyToViews(items []model.Policy) []policyView {
	views := make([]policyView, 0, len(items))
	for _, item := range items {
		views = append(views, policyToView(item))
	}
	return views
}

type ruleView struct {
	ID          string    `json:"id"`
	DBType      string    `json:"db_type"`
	Title       string    `json:"title"`
	RiskLevel   int       `json:"risk_level"`
	PatternType string    `json:"pattern_type"`
	Definition  string    `json:"definition"`
	Enabled     bool      `json:"enabled"`
	Builtin     bool      `json:"builtin"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func ruleToView(rule model.Rule) ruleView {
	return ruleView{
		ID: rule.ID, DBType: rule.DBType, Title: rule.Title, RiskLevel: rule.RiskLevel, PatternType: rule.PatternType,
		Definition: rule.Definition, Enabled: rule.Enabled, Builtin: rule.Builtin, CreatedAt: rule.CreatedAt,
		UpdatedAt: rule.UpdatedAt,
	}
}

func ruleToViews(items []model.Rule) []ruleView {
	views := make([]ruleView, 0, len(items))
	for _, item := range items {
		views = append(views, ruleToView(item))
	}
	return views
}

type maskRuleView struct {
	ID                string    `json:"id"`
	DatasourceID      *string   `json:"datasource_id,omitempty"`
	SchemaName        string    `json:"schema_name"`
	TableName         string    `json:"table_name"`
	ColumnName        string    `json:"column_name"`
	SensitiveType     string    `json:"sensitive_type"`
	Algo              string    `json:"algo"`
	Enabled           bool      `json:"enabled"`
	RangeBucketWidth  *int64    `json:"range_bucket_width,omitempty"`
	RangeBucketOffset *int64    `json:"range_bucket_offset,omitempty"`
	RangeGranularity  *string   `json:"range_granularity,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func maskRuleToView(rule model.MaskRule) maskRuleView {
	return maskRuleView{
		ID: rule.ID, DatasourceID: rule.DatasourceID, SchemaName: rule.SchemaName, TableName: rule.TableName, ColumnName: rule.ColumnName,
		SensitiveType: rule.SensitiveType, Algo: rule.Algo, Enabled: rule.Enabled,
		RangeBucketWidth: rule.RangeBucketWidth, RangeBucketOffset: rule.RangeBucketOffset,
		RangeGranularity: rule.RangeGranularity,
		CreatedAt:        rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
	}
}

func maskRuleToViews(items []model.MaskRule) []maskRuleView {
	views := make([]maskRuleView, 0, len(items))
	for _, item := range items {
		views = append(views, maskRuleToView(item))
	}
	return views
}
