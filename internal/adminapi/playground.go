package adminapi

import (
	"net/http"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
)

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
