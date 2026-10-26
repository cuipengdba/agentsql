package chatbi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func newSQLBotDatasourceID() (ID, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrUnavailable
	}
	value := binary.BigEndian.Uint64(raw[:]) & ((uint64(1) << 53) - 1)
	if value == 0 {
		return newSQLBotDatasourceID()
	}
	return ID(strconv.FormatUint(value, 10)), nil
}

type excelDatasourceInput struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Type          string `json:"type"`
	Configuration string `json:"configuration"`
	Tables        []struct {
		Name    string `json:"table_name"`
		Comment string `json:"table_comment"`
	} `json:"tables"`
}

// addExcelDatasource accepts the pinned SQLBot client's datasource/add shape
// only after a matching, one-use import receipt has been stored locally. The
// browser's AES configuration carries no authority over SQL, tables or DB
// credentials; the distinct child binding came from the gateway.
func (h *Handler) addExcelDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input excelDatasourceInput
	if !readBody(w, r, &input) {
		return
	}
	h.createExcelDatasource(w, r, p, token, input)
}

// Claim an import from AgentSQL's own UI. The browser provides only a lookup
// token and display metadata; the tables and legacy wire configuration are
// derived from the server-owned receipt and checked again by the common path.
func (h *Handler) claimNativeFileDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var request struct {
		FilePath    string `json:"filePath"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !readBody(w, r, &request) {
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if request.FilePath == "" || strings.ContainsAny(request.FilePath, "/\\\x00") || len(request.FilePath) > 128 {
		failure(w, ErrNotFound)
		return
	}
	var receipt managedFileReceipt
	_, err := h.service.Store.get(r.Context(), managedFileReceiptKey(p, request.FilePath), &receipt)
	if err != nil || receipt.ClaimedDatasource != "" || receipt.Filename != request.FilePath {
		failure(w, ErrNotFound)
		return
	}
	configuration, err := encodeSQLBotExcelConfig(receipt.Filename, receipt.Sheets)
	if err != nil {
		failure(w, err)
		return
	}
	input := excelDatasourceInput{Name: request.Name, Description: request.Description,
		Type: "excel", Configuration: configuration}
	for _, sheet := range receipt.Sheets {
		input.Tables = append(input.Tables, struct {
			Name    string `json:"table_name"`
			Comment string `json:"table_comment"`
		}{Name: sheet.TableName})
	}
	h.createExcelDatasource(w, r, p, token, input)
}

func (h *Handler) createExcelDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string, input excelDatasourceInput) {
	if input.Type != "excel" || input.Name == "" || strings.TrimSpace(input.Name) != input.Name ||
		len(input.Name) > 50 || len(input.Description) > 512 || strings.ContainsAny(input.Name, "/\\\r\n\x00") {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid Excel datasource")
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	existing, err := h.service.Store.list(r.Context(), "binding", string(p.Workspace), string(p.User.ID))
	if err != nil {
		failure(w, err)
		return
	}
	for _, raw := range existing {
		var item Binding
		if json.Unmarshal(raw, &item) != nil || item.User != p.User.ID || item.Workspace != p.Workspace {
			failure(w, ErrUnavailable)
			return
		}
		if strings.EqualFold(item.Name, input.Name) {
			failure(w, ErrConflict)
			return
		}
	}
	filename, configuredSheets, err := decodeSQLBotExcelConfig(input.Configuration)
	if err != nil {
		failure(w, err)
		return
	}
	var receipt managedFileReceipt
	revision, err := h.service.Store.get(r.Context(), managedFileReceiptKey(p, filename), &receipt)
	if err != nil || receipt.ClaimedDatasource != "" || receipt.Filename != filename ||
		!managedFileOperationPattern.MatchString(receipt.OperationID) ||
		!managedFileChildNamePattern.MatchString(receipt.DatasourceName) || receipt.BindingID == "" ||
		!reflect.DeepEqual(configuredSheets, receipt.Sheets) || len(input.Tables) != len(receipt.Sheets) {
		failure(w, ErrNotFound)
		return
	}
	selected := map[string]bool{}
	for _, table := range input.Tables {
		if !managedFileNamePattern.MatchString(table.Name) || table.Comment != "" || selected[table.Name] {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid Excel tables")
			return
		}
		selected[table.Name] = true
	}
	for _, sheet := range receipt.Sheets {
		if !selected[sheet.TableName] {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid Excel tables")
			return
		}
	}
	base, err := h.service.binding(r.Context(), p, h.service.ManagedFileDatasource)
	if err != nil || base.Dialect != "pg" || base.BindingID == receipt.BindingID {
		failure(w, ErrGateway)
		return
	}
	identifier, err := newSQLBotDatasourceID()
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	child := Binding{Datasource: identifier, Workspace: p.Workspace, User: p.User.ID,
		Name: input.Name, ResolveName: receipt.DatasourceName, Type: "excel", Description: input.Description,
		Configuration: input.Configuration, ManagedFileID: receipt.OperationID,
		Dialect: "pg", BindingID: receipt.BindingID, ConnectorToken: receipt.ConnectorToken, AgentKey: base.AgentKey,
		Created: time.Now().UTC()}
	columns, err := h.service.Gateway.Schema(r.Context(), p, child)
	if err != nil {
		failure(w, err)
		return
	}
	visible := map[string]bool{}
	for _, column := range columns {
		if column.Schema != "agentsql_files" || column.Table == "" || !selected[column.Table] {
			failure(w, ErrGateway)
			return
		}
		visible[column.Table] = true
	}
	if len(visible) != len(receipt.Sheets) {
		failure(w, ErrGateway)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	current, err := h.service.binding(r.Context(), p, h.service.ManagedFileDatasource)
	if err != nil || current != base {
		failure(w, ErrGateway)
		return
	}
	if err := h.service.Store.claimManagedFileReceipt(r.Context(), p, filename, revision, receipt, child); err != nil {
		failure(w, err)
		return
	}
	payload, err := legacyExcelDatasource(child)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, payload)
}

func legacyExcelDatasource(binding Binding) (map[string]any, error) {
	if binding.Type != "excel" || !binding.Datasource.Valid() || !binding.Workspace.Valid() || !binding.User.Valid() {
		return nil, ErrUnavailable
	}
	numericID, err := strconv.ParseInt(string(binding.Datasource), 10, 64)
	if err != nil || numericID < 1 || numericID > (1<<53)-1 {
		return nil, ErrUnavailable
	}
	workspaceID, err := strconv.ParseInt(string(binding.Workspace), 10, 64)
	if err != nil {
		return nil, ErrUnavailable
	}
	userID, err := strconv.ParseInt(string(binding.User), 10, 64)
	if err != nil {
		return nil, ErrUnavailable
	}
	_, sheets, err := decodeSQLBotExcelConfig(binding.Configuration)
	if err != nil {
		return nil, ErrUnavailable
	}
	return map[string]any{"id": numericID, "name": binding.Name, "description": binding.Description,
		"type": "excel", "type_name": "Excel/CSV", "configuration": binding.Configuration,
		"oid": workspaceID, "create_by": userID, "create_time": binding.Created,
		"status": "Success", "num": strconv.Itoa(len(sheets)) + "/" + strconv.Itoa(len(sheets)),
		"recommended_config": 1, "embedding": nil, "table_relation": nil}, nil
}

func (h *Handler) getExcelDatasource(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, err := h.service.binding(r.Context(), p, id)
	if err != nil || binding.Type != "excel" {
		failure(w, ErrNotFound)
		return
	}
	payload, err := legacyExcelDatasource(binding)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, payload)
}

func (h *Handler) deleteExcelDatasource(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, err := h.service.binding(r.Context(), p, id)
	if err != nil || binding.Type != "excel" || binding.Name != r.PathValue("name") ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		failure(w, ErrNotFound)
		return
	}
	deleter, ok := h.service.Gateway.(managedFileDeleter)
	if !ok {
		failure(w, ErrGateway)
		return
	}
	if err := deleter.DeleteFile(r.Context(), p, binding, binding.ManagedFileID); err != nil {
		failure(w, err)
		return
	}
	// Physical deletion has already closed query visibility. Finish the local
	// alias removal even if the browser disconnects at this point.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	defer cancel()
	if err := h.service.Store.retireManagedFileDatasource(cleanup, p, binding); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"message": "Datasource with ID " + string(id) + " deleted successfully."})
}
