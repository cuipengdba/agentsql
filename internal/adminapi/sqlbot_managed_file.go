package adminapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

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
	// SQLBot calls importToDb before datasource/add. Issue a distinct binding
	// for this import now, so later Excel datasources cannot discover or query
	// each other's tables even when they share the same physical reader role.
	seed, err := managedfile.NewOperationID()
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file identity unavailable")
		return
	}
	tenantCtx, err := store.WithTenant(request.Context(), principal.TenantID)
	if err != nil {
		handler.fail(writer, http.StatusForbidden, "managed file identity unavailable")
		return
	}
	child, connectorToken, err := handler.deps.Runtime.Store.SQLBotBindings().Create(tenantCtx, store.SQLBotBinding{
		WorkspaceID: binding.WorkspaceID, DatasourceName: "agentsql-import-" + hex.EncodeToString(seed[:]),
		SQLBotUserID: binding.SQLBotUserID, AgentSQLUserID: binding.AgentSQLUserID,
		AgentID: binding.AgentID, DatasourceID: binding.DatasourceID, SchemaName: "agentsql_files",
		AgentKeyEnv: binding.AgentKeyEnv, ConnectorTokenEnv: binding.ConnectorTokenEnv, Enabled: true,
	}, principal.UserID)
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "managed file identity unavailable")
		return
	}
	cleanupChild := func(operationID string) {
		// A failed publication cannot leave a live, reusable child connector.
		// An ambiguous publish commit may already be visible; fence and delete
		// that operation before revoking its binding. Keep the binding if the
		// physical delete fails, so the owner can retry.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(tenantCtx), 30*time.Second)
		defer cancel()
		if operationID != "" {
			item, getErr := handler.deps.Runtime.Store.ManagedFileImports().Get(recovery, operationID)
			if getErr != nil {
				return
			}
			if item.Status == "published" {
				if handler.deps.Runtime.DeleteSQLBotFile(recovery, principal, child, operationID, handler.deps.ManagedFiles) != nil {
					return
				}
			}
		}
		_ = handler.deps.Runtime.Store.SQLBotBindings().Delete(recovery, child.ID, child.Revision, principal.UserID)
	}
	digest := sha256.Sum256(input.FileData)
	staged, tables, err := handler.deps.Runtime.StageSQLBotFile(request.Context(), principal, child,
		hex.EncodeToString(digest[:]), int64(len(input.FileData)), sheets, handler.deps.ManagedFiles)
	if err != nil {
		cleanupChild("")
		handler.fail(writer, http.StatusServiceUnavailable, "managed file staging failed")
		return
	}
	published, err := handler.deps.Runtime.PublishSQLBotFile(request.Context(), principal, child, staged, tables, sheets, handler.deps.ManagedFiles)
	if err != nil {
		cleanupChild(staged.ID)
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
