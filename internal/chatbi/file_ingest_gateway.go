package chatbi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type importedFileSheet struct {
	SheetName    string `json:"sheetName"`
	TableName    string `json:"tableName"`
	TableComment string `json:"tableComment"`
	Rows         int    `json:"rows"`
}

type managedFileImportResult struct {
	OperationID    string
	Sheets         []importedFileSheet
	ChildBindingID string
	ChildName      string
	ConnectorToken string
}

type managedFileImporter interface {
	ImportFile(context.Context, Principal, Binding, fileStage, FileImportRequest, fileImportPlan) (managedFileImportResult, error)
}

type managedFileDeleter interface {
	DeleteFile(context.Context, Principal, Binding, string) error
}

var managedFileNamePattern = regexp.MustCompile(`^agentsql_file_[0-9a-f]{32}$`)
var managedFileOperationPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var managedFileChildNamePattern = regexp.MustCompile(`^agentsql-import-[0-9a-f]{32}$`)

// ImportFile sends original staged bytes through a dedicated, dual-identity
// gateway route. The gateway reparses them and owns the database operation;
// no SQL, table name or database credential comes from this client.
func (gateway *ControlledGateway) ImportFile(ctx context.Context, p Principal, b Binding,
	stage fileStage, request FileImportRequest, plan fileImportPlan) (managedFileImportResult, error) {
	if gateway == nil || stage.FilePath != request.FilePath || len(stage.Data) == 0 || len(stage.Data) > maxFileUpload ||
		len(plan.Sheets) == 0 || len(plan.Sheets) > 8 || b.Dialect != "pg" {
		return managedFileImportResult{}, ErrGateway
	}
	resolved, err := gateway.resolve(ctx, p, b)
	if err != nil || resolved.Schema != "agentsql_files" || resolved.Dialect != "pg" {
		return managedFileImportResult{}, ErrGateway
	}
	u, err := validateEndpoint(gateway.Endpoint, gateway.AllowHTTP)
	if err != nil || !strings.HasSuffix(u.Path, "/mcp") {
		return managedFileImportResult{}, ErrGateway
	}
	u.Path = strings.TrimSuffix(u.Path, "/mcp") + "/api/v1/integrations/sqlbot/managed-files/import"
	body, err := json.Marshal(map[string]any{"binding_id": resolved.ID, "filename": stage.Original,
		"file_data": stage.Data, "selection": request})
	if err != nil || len(body) > managedFileImportBodyLimit {
		return managedFileImportResult{}, ErrGateway
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return managedFileImportResult{}, ErrGateway
	}
	httpRequest.Header.Set("Authorization", "Bearer "+b.AgentKey)
	httpRequest.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+resolved.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: gateway.Transport, Timeout: 4 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(httpRequest)
	if err != nil {
		return managedFileImportResult{}, ErrGateway
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return managedFileImportResult{}, ErrGateway
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return managedFileImportResult{}, ErrGateway
	}
	var envelope struct {
		Code *int `json:"code"`
		Data struct {
			OperationID    string              `json:"operation_id"`
			Sheets         []importedFileSheet `json:"sheets"`
			BindingID      string              `json:"binding_id"`
			DatasourceName string              `json:"datasource_name"`
			ConnectorToken string              `json:"connector_token"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return managedFileImportResult{}, ErrGateway
	}
	decodedToken, decodeErr := base64.RawURLEncoding.DecodeString(envelope.Data.ConnectorToken)
	childID, childErr := uuid.Parse(envelope.Data.BindingID)
	if envelope.Code == nil || *envelope.Code != 0 ||
		!managedFileOperationPattern.MatchString(envelope.Data.OperationID) || len(envelope.Data.Sheets) != len(plan.Sheets) ||
		childErr != nil || childID.String() != envelope.Data.BindingID || envelope.Data.BindingID == resolved.ID ||
		!managedFileChildNamePattern.MatchString(envelope.Data.DatasourceName) || decodeErr != nil || len(decodedToken) != 32 {
		return managedFileImportResult{}, ErrGateway
	}
	seen := map[string]bool{}
	for index, sheet := range envelope.Data.Sheets {
		if sheet.SheetName != plan.Sheets[index].Name || sheet.Rows != len(plan.Sheets[index].Rows) ||
			sheet.TableComment != "" || !managedFileNamePattern.MatchString(sheet.TableName) || seen[sheet.TableName] {
			return managedFileImportResult{}, ErrGateway
		}
		seen[sheet.TableName] = true
	}
	return managedFileImportResult{OperationID: envelope.Data.OperationID, Sheets: envelope.Data.Sheets,
		ChildBindingID: envelope.Data.BindingID, ChildName: envelope.Data.DatasourceName,
		ConnectorToken: envelope.Data.ConnectorToken}, nil
}

// DeleteFile sends only a server-issued operation ID. The gateway performs
// owner, binding, policy and physical cleanup checks independently.
func (gateway *ControlledGateway) DeleteFile(ctx context.Context, p Principal, b Binding, operationID string) error {
	if gateway == nil || !managedFileOperationPattern.MatchString(operationID) || b.Dialect != "pg" {
		return ErrGateway
	}
	resolved, err := gateway.resolve(ctx, p, b)
	if err != nil || resolved.Schema != "agentsql_files" || resolved.Dialect != "pg" {
		return ErrGateway
	}
	u, err := validateEndpoint(gateway.Endpoint, gateway.AllowHTTP)
	if err != nil || !strings.HasSuffix(u.Path, "/mcp") {
		return ErrGateway
	}
	u.Path = strings.TrimSuffix(u.Path, "/mcp") + "/api/v1/integrations/sqlbot/managed-files/delete"
	body, err := json.Marshal(map[string]string{"binding_id": resolved.ID, "operation_id": operationID})
	if err != nil {
		return ErrGateway
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return ErrGateway
	}
	httpRequest.Header.Set("Authorization", "Bearer "+b.AgentKey)
	httpRequest.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+resolved.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: gateway.Transport, Timeout: 45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(httpRequest)
	if err != nil {
		return ErrGateway
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrGateway
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return ErrGateway
	}
	var envelope struct {
		Code *int `json:"code"`
		Data struct {
			OperationID string `json:"operation_id"`
			Status      string `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 ||
		envelope.Data.OperationID != operationID || envelope.Data.Status != "deleted" {
		return ErrGateway
	}
	return nil
}

const managedFileImportBodyLimit = 8 << 20
