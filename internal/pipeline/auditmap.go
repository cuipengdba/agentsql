package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

func mapAuditLog(
	request Request,
	agent *model.Agent,
	datasource *model.Datasource,
	ast *model.AST,
	response Response,
	executionResult *model.QueryResult,
	auditDecision string,
	operationError error,
	started time.Time,
) (model.AuditLog, error) {
	hits := response.Assessment.Hits
	if hits == nil {
		hits = []model.RuleHit{}
	}
	encodedHits, err := json.Marshal(hits)
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("marshal audit rule hits: %w", err)
	}
	log := model.AuditLog{
		AgentID:        agentIDPointer(agent),
		DatasourceID:   optionalNonEmptyString(request.DatasourceID),
		SessionID:      optionalNonEmptyString(request.SessionID),
		ConversationID: cloneStringPointer(request.ConversationID),
		MCPTool:        optionalNonEmptyString(request.MCPTool),
		SQLRaw:         stringPointer(request.SQL),
		Decision:       auditDecision,
		RuleHits:       stringPointer(string(encodedHits)),
		RiskLevel:      intPointer(int(response.Assessment.Risk)),
		LatencyMS:      int64Pointer(time.Since(started).Milliseconds()),
		ClientIP:       cloneStringPointer(request.ClientIP),
		ModelName:      cloneStringPointer(request.ModelName),
	}
	if datasource != nil {
		log.DBType = optionalNonEmptyString(datasource.DBType)
	}
	if ast != nil {
		log.SQLNorm = optionalNonEmptyString(ast.Normalized)
		log.StmtType = optionalNonEmptyString(string(ast.StmtType))
		if objects := normalizedAuditObjects(ast.Tables); objects != "" {
			log.Objects = stringPointer(objects)
		}
		if ast.Explain != nil {
			log.EstRows = int64Pointer(ast.Explain.EstScanRows)
		}
	}
	if executionResult != nil {
		log.RowsReturned = intPointer(executionResult.RowCount)
	}
	if operationError != nil {
		log.ErrorMsg = stringPointer(operationError.Error())
	}
	return log, nil
}

func normalizedAuditObjects(objects []model.ObjectRef) string {
	values := make([]string, 0, len(objects))
	for _, object := range objects {
		table := strings.TrimSpace(object.Table)
		schema := strings.TrimSpace(object.Schema)
		if table == "" {
			continue
		}
		if schema != "" {
			values = append(values, schema+"."+table)
		} else {
			values = append(values, table)
		}
	}
	return strings.Join(values, ",")
}

func agentIDPointer(agent *model.Agent) *string {
	if agent == nil {
		return nil
	}
	return optionalNonEmptyString(agent.ID)
}

func optionalNonEmptyString(value string) *string {
	if value == "" {
		return nil
	}
	return stringPointer(value)
}

func stringPointer(value string) *string {
	copyValue := value
	return &copyValue
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}

func intPointer(value int) *int {
	copyValue := value
	return &copyValue
}

func int64Pointer(value int64) *int64 {
	copyValue := value
	return &copyValue
}
