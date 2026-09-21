package adminapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
)

// DemoRunner is the only live-pipeline capability exposed to the demo HTTP
// endpoint. Production startup injects it only after demo credentials pass
// fail-fast validation.
type DemoRunner interface {
	ProcessDemo(context.Context, pipeline.Request) (pipeline.Response, error)
}

type playgroundAssessInput struct {
	SQL        string `json:"sql"`
	DBType     string `json:"db_type"`
	AgentLevel string `json:"agent_level,omitempty"`
}

type playgroundHitView struct {
	RuleID     string `json:"RuleID"`
	Risk       int    `json:"Risk"`
	Decision   string `json:"Decision"`
	Message    string `json:"Message"`
	Suggestion string `json:"Suggestion"`
}

type playgroundAssessView struct {
	Decision     string              `json:"Decision"`
	Risk         int                 `json:"Risk"`
	StmtType     string              `json:"StmtType"`
	Hits         []playgroundHitView `json:"Hits"`
	EstScanRows  int64               `json:"EstScanRows"`
	Reason       string              `json:"Reason"`
	Suggestion   string              `json:"Suggestion"`
	Normalized   string              `json:"Normalized"`
	Objects      []model.ObjectRef   `json:"Objects"`
	StageLatency map[string]int64    `json:"StageLatency"`
	ParseError   string              `json:"ParseError"`
	StaticOnly   bool                `json:"StaticOnly"`
	DBType       string              `json:"DBType"`
	AgentLevel   string              `json:"AgentLevel"`
	SQL          string              `json:"SQL"`
}

type playgroundRunInput struct {
	SQL          string `json:"sql"`
	DatasourceID string `json:"datasource_id"`
	AgentProfile string `json:"agent_profile"`
}

type playgroundRunAssessmentView struct {
	Decision     string                    `json:"decision"`
	Risk         int                       `json:"risk"`
	StmtType     string                    `json:"stmt_type"`
	Hits         []playgroundRunHitView    `json:"hits"`
	EstScanRows  int64                     `json:"est_scan_rows"`
	Reason       string                    `json:"reason"`
	Suggestion   string                    `json:"suggestion"`
	Normalized   string                    `json:"normalized"`
	Objects      []playgroundRunObjectView `json:"objects"`
	StageLatency map[string]int64          `json:"stage_latency"`
}

type playgroundRunHitView struct {
	RuleID     string `json:"rule_id"`
	Risk       int    `json:"risk"`
	Decision   string `json:"decision"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

type playgroundRunObjectView struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Alias  string `json:"alias"`
}

type playgroundResultView struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	RowCount  int        `json:"row_count"`
	Truncated bool       `json:"truncated"`
	LatencyMS int64      `json:"latency_ms"`
}

type playgroundRedactView struct {
	TouchedColumns map[int]string `json:"touched_columns"`
	MaskedCells    int            `json:"masked_cells"`
}

type playgroundRunView struct {
	Decision     string                      `json:"decision"`
	ErrorCode    string                      `json:"error_code,omitempty"`
	ErrorStage   string                      `json:"error_stage,omitempty"`
	ErrorMessage string                      `json:"error_message,omitempty"`
	Suggestion   string                      `json:"suggestion,omitempty"`
	Assessment   playgroundRunAssessmentView `json:"assessment"`
	Result       playgroundResultView        `json:"result"`
	Redact       playgroundRedactView        `json:"redact"`
	AuditID      int64                       `json:"audit_id"`
	DatasourceID string                      `json:"datasource_id"`
	AgentProfile string                      `json:"agent_profile"`
}

func (handler *Handler) playgroundAssess(writer http.ResponseWriter, request *http.Request) {
	var input playgroundAssessInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid request body")
		return
	}
	input.SQL = strings.TrimSpace(input.SQL)
	if input.SQL == "" {
		handler.fail(writer, http.StatusUnprocessableEntity, "sql is required")
		return
	}
	if input.DBType != "postgres" && input.DBType != "mysql" {
		handler.fail(writer, http.StatusUnprocessableEntity, "db_type must be postgres or mysql")
		return
	}
	if input.AgentLevel == "" {
		input.AgentLevel = "readonly"
	}
	if !validAgentLevel(input.AgentLevel) {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid agent level")
		return
	}

	assessment, parseError, err := pipeline.StaticAssess(pipeline.StaticAssessInput{
		SQL: input.SQL, Dialect: model.DBDialect(input.DBType), AgentLevel: input.AgentLevel,
	})
	if err != nil {
		handler.internal(writer, err)
		return
	}
	view := playgroundView(assessment, input, parseError)
	handler.ok(writer, view)
}

func (handler *Handler) playgroundRun(writer http.ResponseWriter, request *http.Request) {
	var input playgroundRunInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	input.SQL = strings.TrimSpace(input.SQL)
	if input.SQL == "" {
		handler.fail(writer, http.StatusUnprocessableEntity, "sql is required")
		return
	}
	if input.AgentProfile != "ro" && input.AgentProfile != "dml" {
		handler.fail(writer, http.StatusUnprocessableEntity, "agent_profile must be ro or dml")
		return
	}
	if !handler.deps.Config.Demo.AllowsDatasource(input.DatasourceID) {
		handler.fail(writer, http.StatusForbidden, "datasource is not allowed for demo")
		return
	}
	apiKey, ok := handler.deps.demoKeys.forProfile(input.AgentProfile)
	if !ok {
		handler.fail(writer, http.StatusInternalServerError, "internal error")
		return
	}
	response, err := handler.deps.demoRunner.ProcessDemo(request.Context(), pipeline.Request{
		APIKey:       apiKey,
		DatasourceID: input.DatasourceID,
		SQL:          input.SQL,
		MCPTool:      "query",
	})
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, playgroundRunResponseView(response, input))
}

func playgroundRunResponseView(response pipeline.Response, input playgroundRunInput) playgroundRunView {
	hits := make([]playgroundRunHitView, 0, len(response.Assessment.Hits))
	for _, hit := range response.Assessment.Hits {
		hits = append(hits, playgroundRunHitView{
			RuleID: hit.RuleID, Risk: int(hit.Risk), Decision: string(hit.Decision),
			Message: hit.Message, Suggestion: hit.Suggestion,
		})
	}
	objects := make([]playgroundRunObjectView, 0, len(response.Assessment.Objects))
	for _, object := range response.Assessment.Objects {
		objects = append(objects, playgroundRunObjectView{
			Schema: object.Schema, Table: object.Table, Alias: object.Alias,
		})
	}
	latency := map[string]int64{
		pipeline.StageAuth: 0, pipeline.StageLoad: 0, pipeline.StageParse: 0,
		pipeline.StageGuardStatic: 0, pipeline.StageGuardDynamic: 0,
		pipeline.StageExecute: 0, pipeline.StageRedact: 0, pipeline.StageAudit: 0,
	}
	for stage, value := range response.Assessment.StageLatency {
		latency[stage] = value
	}
	result := playgroundResultView{Columns: []string{}, Rows: [][]string{}}
	if response.Result != nil {
		result = playgroundResultView{
			Columns:  append([]string{}, response.Result.Columns...),
			Rows:     clonePlaygroundRows(response.Result.Rows),
			RowCount: response.Result.RowCount, Truncated: response.Result.Truncated,
			LatencyMS: response.Result.LatencyMS,
		}
	}
	touchedColumns := make(map[int]string, len(response.Redact.TouchedColumns))
	for index, sensitiveType := range response.Redact.TouchedColumns {
		touchedColumns[index] = string(sensitiveType)
	}
	view := playgroundRunView{
		Decision: string(response.Decision), ErrorCode: response.ErrorCode, ErrorStage: response.ErrorStage,
		ErrorMessage: response.ErrorMessage,
		Assessment: playgroundRunAssessmentView{
			Decision: string(response.Assessment.Decision), Risk: int(response.Assessment.Risk),
			StmtType: string(response.Assessment.StmtType), Hits: hits,
			EstScanRows: response.Assessment.EstScanRows, Reason: response.Assessment.Reason,
			Suggestion: response.Assessment.Suggestion, Normalized: response.Assessment.Normalized,
			Objects: objects, StageLatency: latency,
		},
		Result:  result,
		Redact:  playgroundRedactView{TouchedColumns: touchedColumns, MaskedCells: response.Redact.MaskedCells},
		AuditID: response.AuditID, DatasourceID: input.DatasourceID, AgentProfile: input.AgentProfile,
	}
	if response.Decision == model.DecisionError {
		view.Suggestion = response.Suggestion
	}
	return view
}

func clonePlaygroundRows(rows [][]string) [][]string {
	cloned := make([][]string, 0, len(rows))
	for _, row := range rows {
		cloned = append(cloned, append([]string{}, row...))
	}
	return cloned
}

func playgroundView(
	assessment model.Assessment,
	input playgroundAssessInput,
	parseError string,
) playgroundAssessView {
	hits := make([]playgroundHitView, 0, len(assessment.Hits))
	for _, hit := range assessment.Hits {
		hits = append(hits, playgroundHitView{
			RuleID: hit.RuleID, Risk: int(hit.Risk), Decision: string(hit.Decision),
			Message: hit.Message, Suggestion: hit.Suggestion,
		})
	}
	objects := append([]model.ObjectRef{}, assessment.Objects...)
	latency := make(map[string]int64, len(assessment.StageLatency))
	for stage, value := range assessment.StageLatency {
		latency[stage] = value
	}
	decision := string(assessment.Decision)
	if parseError != "" {
		decision = "error"
	}
	return playgroundAssessView{
		Decision: decision, Risk: int(assessment.Risk), StmtType: string(assessment.StmtType),
		Hits: hits, EstScanRows: assessment.EstScanRows, Reason: assessment.Reason,
		Suggestion: assessment.Suggestion, Normalized: assessment.Normalized, Objects: objects,
		StageLatency: latency, ParseError: parseError, StaticOnly: true, DBType: input.DBType,
		AgentLevel: input.AgentLevel, SQL: input.SQL,
	}
}
