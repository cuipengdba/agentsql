package adminapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/chatbi"
	"github.com/cuipengdba/agentsql/internal/managedfile"
	"github.com/cuipengdba/agentsql/internal/store"
)

const managedFileImportBodyBytes = 8 << 20

// This exact route is called by the separately authenticated ChatBI server.
// It replays parsing from the uploaded bytes instead of trusting rows in the
// request, then keeps both the physical operation and policy publication on
// the gateway side of the credential boundary.
func (handler *Handler) sqlbotManagedFileImport(writer http.ResponseWriter, request *http.Request) {
	keys := request.Header.Values("Authorization")
	humans := request.Header.Values("X-AgentSQL-User-Authorization")
	if len(keys) != 1 || len(humans) != 1 {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	agentKey, keyOK := adminBearerToken(keys[0])
	humanToken, humanOK := adminBearerToken(humans[0])
	if !keyOK || !humanOK {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	agent, err := auth.NewAuthenticator(handler.deps.Runtime.Store.Agents()).Authenticate(request.Context(), agentKey)
	if err != nil || agent.Level != "readonly" || agent.TaskGrantID != "" {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if handler.rbac == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file import unavailable")
		return
	}
	verifier := NewSQLBotDelegationVerifier(handler.tokenKey, handler.deps.Runtime.Store, handler.rbac)
	principal, binding, err := verifier.VerifySQLBot(request.Context(), humanToken, agent.ID)
	if err != nil || principal.TenantID != agent.TenantID || binding.SchemaName != "agentsql_files" {
		handler.fail(writer, http.StatusForbidden, "managed file identity unavailable")
		return
	}
	if handler.deps.ManagedFiles == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file import unavailable")
		return
	}
	if binding.DatasourceID != handler.deps.ManagedFiles.ReadDatasourceID() {
		handler.fail(writer, http.StatusForbidden, "managed file identity unavailable")
		return
	}
	var input struct {
		BindingID string                   `json:"binding_id"`
		Filename  string                   `json:"filename"`
		FileData  []byte                   `json:"file_data"`
		Selection chatbi.FileImportRequest `json:"selection"`
	}
	request.Body = http.MaxBytesReader(writer, request.Body, managedFileImportBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.BindingID != binding.ID ||
		len(input.FileData) == 0 || len(input.FileData) > 4<<20 || input.Filename == "" ||
		strings.ContainsAny(input.Filename, "/\\\x00") {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid managed file request")
		return
	}
	sheets, names, err := chatbi.BuildManagedFileSheets(input.Filename, input.FileData, input.Selection)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid managed file contents")
		return
	}
	// SQLBot calls importToDb before datasource/add. Derive one child identity
	// from the authenticated staged-file ID so a lost response can be replayed.
	name, connectorToken, err := handler.deps.Runtime.DeriveSQLBotImportIdentity(binding, principal, input.Selection.FilePath)
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file identity unavailable")
		return
	}
	tenantCtx, err := store.WithTenant(request.Context(), principal.TenantID)
	if err != nil {
		handler.fail(writer, http.StatusForbidden, "managed file identity unavailable")
		return
	}
	child, err := handler.deps.Runtime.Store.SQLBotBindings().CreateDerived(tenantCtx, store.SQLBotBinding{
		WorkspaceID: binding.WorkspaceID, DatasourceName: name,
		SQLBotUserID: binding.SQLBotUserID, AgentSQLUserID: binding.AgentSQLUserID,
		AgentID: binding.AgentID, DatasourceID: binding.DatasourceID, SchemaName: "agentsql_files",
		ParentBindingID: binding.ID, ParentRevision: binding.Revision,
		AgentKeyEnv: binding.AgentKeyEnv, ConnectorTokenEnv: binding.ConnectorTokenEnv, Enabled: true,
	}, principal.UserID, connectorToken)
	if err != nil {
		child, err = handler.deps.Runtime.Store.SQLBotBindings().ResolveConnector(tenantCtx, connectorToken)
		if err != nil || child.DatasourceName != name || child.ParentBindingID != binding.ID ||
			child.ParentRevision != binding.Revision || child.TenantID != binding.TenantID ||
			child.WorkspaceID != binding.WorkspaceID || child.SQLBotUserID != binding.SQLBotUserID ||
			child.AgentSQLUserID != binding.AgentSQLUserID || child.AgentID != binding.AgentID ||
			child.DatasourceID != binding.DatasourceID || child.SchemaName != "agentsql_files" ||
			child.Revision != 1 || !child.Enabled {
			handler.fail(writer, http.StatusServiceUnavailable, "managed file identity unavailable")
			return
		}
	}
	digest := sha256.Sum256(input.FileData)
	selectionBytes, err := json.Marshal(sheets)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid managed file contents")
		return
	}
	selectionDigest := sha256.Sum256(selectionBytes)
	journal := handler.deps.Runtime.Store.ManagedFileImports()
	previous, err := journal.GetByRequestKey(tenantCtx, child.ID)
	if err == nil {
		if previous.Status != "published" || previous.FileSHA256 != hex.EncodeToString(digest[:]) ||
			previous.SelectionSHA256 != hex.EncodeToString(selectionDigest[:]) || previous.RequestKey != child.ID ||
			previous.BindingID != child.ID || previous.BindingRevision != child.Revision ||
			previous.TenantID != principal.TenantID || previous.WorkspaceID != child.WorkspaceID ||
			previous.OwnerUserID != principal.UserID || previous.AgentID != child.AgentID ||
			previous.DatasourceID != child.DatasourceID || previous.FileBytes != int64(len(input.FileData)) ||
			previous.SheetCount != len(sheets) {
			handler.fail(writer, http.StatusConflict, "managed file request already used")
			return
		}
		storedTables, tableErr := journal.ListTables(tenantCtx, previous)
		operation, idErr := managedfile.ParseOperationID(previous.ID)
		if tableErr != nil || idErr != nil || len(storedTables) != len(sheets) {
			handler.fail(writer, http.StatusServiceUnavailable, "managed file replay unavailable")
			return
		}
		result := make([]map[string]any, len(storedTables))
		for index, table := range storedTables {
			expected, nameErr := managedfile.TableName(operation, index)
			allowed, ownerErr := journal.PublishedTableOwner(tenantCtx, child.ID, child.DatasourceID, child.AgentID, principal.UserID, table.Name)
			if nameErr != nil || table.Name != expected || table.Rows != len(sheets[index].Rows) || ownerErr != nil || !allowed {
				handler.fail(writer, http.StatusServiceUnavailable, "managed file replay unavailable")
				return
			}
			result[index] = map[string]any{"sheetName": names[index], "tableName": table.Name, "tableComment": "", "rows": table.Rows}
		}
		if err := handler.deps.Runtime.Store.SQLBotBindings().RecordImportReplay(tenantCtx, child, principal.UserID); err != nil {
			handler.fail(writer, http.StatusServiceUnavailable, "managed file replay unavailable")
			return
		}
		handler.ok(writer, map[string]any{"operation_id": previous.ID, "sheets": result,
			"binding_id": child.ID, "datasource_name": child.DatasourceName, "connector_token": connectorToken})
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file request unavailable")
		return
	}
	staged, tables, err := handler.deps.Runtime.StageSQLBotFile(request.Context(), principal, child,
		hex.EncodeToString(digest[:]), int64(len(input.FileData)), sheets, handler.deps.ManagedFiles)
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file staging failed")
		return
	}
	published, err := handler.deps.Runtime.PublishSQLBotFile(request.Context(), principal, child, staged, tables, sheets, handler.deps.ManagedFiles)
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file publication failed")
		return
	}
	result := make([]map[string]any, len(tables))
	for index, table := range tables {
		result[index] = map[string]any{"sheetName": names[index], "tableName": table.Name, "tableComment": "", "rows": table.Rows}
	}
	handler.ok(writer, map[string]any{"operation_id": published.ID, "sheets": result,
		"binding_id": child.ID, "datasource_name": child.DatasourceName, "connector_token": connectorToken})
}

// A client may request deletion only by the server-issued operation ID. The
// gateway proves the same Agent Key and short-lived delegated user identity
// used for import; the control store checks exact owner and binding revision.
func (handler *Handler) sqlbotManagedFileDelete(writer http.ResponseWriter, request *http.Request) {
	keys := request.Header.Values("Authorization")
	humans := request.Header.Values("X-AgentSQL-User-Authorization")
	if len(keys) != 1 || len(humans) != 1 {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	agentKey, keyOK := adminBearerToken(keys[0])
	humanToken, humanOK := adminBearerToken(humans[0])
	if !keyOK || !humanOK {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	agent, err := auth.NewAuthenticator(handler.deps.Runtime.Store.Agents()).Authenticate(request.Context(), agentKey)
	if err != nil || agent.Level != "readonly" || agent.TaskGrantID != "" {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if handler.rbac == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file delete unavailable")
		return
	}
	verifier := NewSQLBotDelegationVerifier(handler.tokenKey, handler.deps.Runtime.Store, handler.rbac)
	principal, binding, err := verifier.VerifySQLBot(request.Context(), humanToken, agent.ID)
	if err != nil || principal.TenantID != agent.TenantID || binding.SchemaName != "agentsql_files" {
		handler.fail(writer, http.StatusForbidden, "managed file identity unavailable")
		return
	}
	if handler.deps.ManagedFiles == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file delete unavailable")
		return
	}
	if binding.DatasourceID != handler.deps.ManagedFiles.ReadDatasourceID() {
		handler.fail(writer, http.StatusForbidden, "managed file identity unavailable")
		return
	}
	var input struct {
		BindingID   string `json:"binding_id"`
		OperationID string `json:"operation_id"`
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 2048)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.BindingID != binding.ID {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid managed file delete")
		return
	}
	if _, err := managedfile.ParseOperationID(input.OperationID); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid managed file delete")
		return
	}
	if err := handler.deps.Runtime.DeleteSQLBotFile(request.Context(), principal, binding, input.OperationID, handler.deps.ManagedFiles); err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file delete failed")
		return
	}
	handler.ok(writer, map[string]any{"operation_id": input.OperationID, "status": "deleted"})
}
