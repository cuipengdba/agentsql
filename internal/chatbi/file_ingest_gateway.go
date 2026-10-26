package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type importedFileSheet struct {
	SheetName    string `json:"sheetName"`
	TableName    string `json:"tableName"`
	TableComment string `json:"tableComment"`
	Rows         int    `json:"rows"`
}

type managedFileImporter interface {
	ImportFile(context.Context, Principal, Binding, fileStage, FileImportRequest, fileImportPlan) ([]importedFileSheet, error)
}

var managedFileNamePattern = regexp.MustCompile(`^agentsql_file_[0-9a-f]{32}$`)
var managedFileOperationPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ImportFile sends original staged bytes through a dedicated, dual-identity
// gateway route. The gateway reparses them and owns the database operation;
// no SQL, table name or database credential comes from this client.
func (gateway *ControlledGateway) ImportFile(ctx context.Context, p Principal, b Binding,
	stage fileStage, request FileImportRequest, plan fileImportPlan) ([]importedFileSheet, error) {
	if gateway == nil || stage.FilePath != request.FilePath || len(stage.Data) == 0 || len(stage.Data) > maxFileUpload ||
		len(plan.Sheets) == 0 || len(plan.Sheets) > 8 || b.Dialect != "pg" {
		return nil, ErrGateway
	}
	resolved, err := gateway.resolve(ctx, p, b)
	if err != nil || resolved.Schema != "agentsql_files" || resolved.Dialect != "pg" {
		return nil, ErrGateway
	}
	u, err := validateEndpoint(gateway.Endpoint, gateway.AllowHTTP)
	if err != nil || !strings.HasSuffix(u.Path, "/mcp") {
		return nil, ErrGateway
	}
	u.Path = strings.TrimSuffix(u.Path, "/mcp") + "/api/v1/integrations/sqlbot/managed-files/import"
	body, err := json.Marshal(map[string]any{"binding_id": resolved.ID, "filename": stage.Original,
		"file_data": stage.Data, "selection": request})
	if err != nil || len(body) > managedFileImportBodyLimit {
		return nil, ErrGateway
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrGateway
	}
	httpRequest.Header.Set("Authorization", "Bearer "+b.AgentKey)
	httpRequest.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+resolved.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: gateway.Transport, Timeout: 4 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, ErrGateway
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrGateway
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, ErrGateway
	}
	var envelope struct {
		Code *int `json:"code"`
		Data struct {
			OperationID string              `json:"operation_id"`
			Sheets      []importedFileSheet `json:"sheets"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 ||
		!managedFileOperationPattern.MatchString(envelope.Data.OperationID) || len(envelope.Data.Sheets) != len(plan.Sheets) {
		return nil, ErrGateway
	}
	seen := map[string]bool{}
	for index, sheet := range envelope.Data.Sheets {
		if sheet.SheetName != plan.Sheets[index].Name || sheet.Rows != len(plan.Sheets[index].Rows) ||
			sheet.TableComment != "" || !managedFileNamePattern.MatchString(sheet.TableName) || seen[sheet.TableName] {
			return nil, ErrGateway
		}
		seen[sheet.TableName] = true
	}
	return envelope.Data.Sheets, nil
}

const managedFileImportBodyLimit = 8 << 20
