package chatbi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// One encrypted staged file per workspace/user bounds storage while the
// managed PostgreSQL provisioning and policy-enrollment path is developed.
// filePath is a lookup token, never a filesystem path or an authorization.
type fileStage struct {
	FilePath string    `json:"file_path"`
	Original string    `json:"original"`
	Data     []byte    `json:"data"`
	Expires  time.Time `json:"expires"`
}

// The encrypted receipt is the bridge between SQLBot's importToDb response
// and its later datasource/add call. A browser never supplies the operation ID.
type managedFileReceipt struct {
	OperationID       string              `json:"operation_id"`
	BindingID         string              `json:"binding_id"`
	DatasourceName    string              `json:"datasource_name"`
	ConnectorToken    string              `json:"connector_token"`
	Filename          string              `json:"filename"`
	FileSHA256        string              `json:"file_sha256"`
	SelectionSHA256   string              `json:"selection_sha256"`
	Sheets            []importedFileSheet `json:"sheets"`
	Created           time.Time           `json:"created"`
	ClaimedDatasource ID                  `json:"claimed_datasource,omitempty"`
}

func managedFileReceiptKey(p Principal, filePath string) objectKey {
	return objectKey{"managed-file-receipt", string(p.Workspace), string(p.User.ID), filePath}
}

func fileSelectionDigest(request FileImportRequest) (string, error) {
	encoded, err := json.Marshal(request.Sheets)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func fileStageKey(p Principal) objectKey {
	return objectKey{"file-stage", string(p.Workspace), string(p.User.ID), "latest"}
}

func readFileUpload(w http.ResponseWriter, r *http.Request) (string, []byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFileUpload+65536)
	reader, err := r.MultipartReader()
	if err != nil {
		return "", nil, errFileImport
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		return "", nil, errFileImport
	}
	filename := part.FileName()
	data, err := io.ReadAll(io.LimitReader(part, maxFileUpload+1))
	_ = part.Close()
	if err != nil || len(data) > maxFileUpload {
		return "", nil, errFileImport
	}
	if part, err = reader.NextPart(); err != io.EOF || part != nil {
		return "", nil, errFileImport
	}
	return filename, data, nil
}

func (h *Handler) parseExcel(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	filename, data, err := readFileUpload(w, r)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid or oversized file upload")
		return
	}
	sheets, err := parseFileImport(filename, data)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid or unsupported CSV/XLSX file")
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
	id, err := newID()
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	extension := ".csv"
	if strings.HasSuffix(strings.ToLower(filename), ".xlsx") {
		extension = ".xlsx"
	}
	stage := fileStage{FilePath: "agentsql-file-" + string(id) + extension, Original: filename,
		Data: data, Expires: time.Now().UTC().Add(time.Hour)}
	if err := h.service.saveFileStage(r.Context(), p, stage, sheets); err != nil {
		failure(w, err)
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
	okay(w, map[string]any{"filePath": stage.FilePath, "data": sheets})
}

func (h *Handler) importToDb(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if h.service.ManagedFileDatasource == "" {
		writeJSON(w, http.StatusServiceUnavailable, "Managed file import is not configured")
		return
	}
	var request FileImportRequest
	if !readBody(w, r, &request) {
		return
	}
	stage, plan, err := h.service.prepareFileImportWithStage(r.Context(), p, request)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	binding, err := h.service.binding(r.Context(), p, h.service.ManagedFileDatasource)
	if err != nil || binding.Dialect != "pg" {
		failure(w, ErrGateway)
		return
	}
	selectionHash, err := fileSelectionDigest(request)
	if err != nil {
		failure(w, errFileImport)
		return
	}
	fileHash := sha256.Sum256(stage.Data)
	var prior managedFileReceipt
	_, err = h.service.Store.get(r.Context(), managedFileReceiptKey(p, stage.FilePath), &prior)
	if err == nil {
		if prior.ClaimedDatasource != "" || prior.Filename != stage.FilePath ||
			prior.FileSHA256 != hex.EncodeToString(fileHash[:]) || prior.SelectionSHA256 != selectionHash ||
			!managedFileOperationPattern.MatchString(prior.OperationID) ||
			!managedFileChildNamePattern.MatchString(prior.DatasourceName) || prior.BindingID == "" ||
			len(prior.Sheets) != len(plan.Sheets) {
			failure(w, ErrConflict)
			return
		}
		child := binding
		child.Type, child.Name, child.ResolveName = "excel", prior.DatasourceName, prior.DatasourceName
		child.BindingID, child.ConnectorToken = prior.BindingID, prior.ConnectorToken
		columns, schemaErr := h.service.schema(r.Context(), p, child)
		if schemaErr != nil {
			failure(w, ErrGateway)
			return
		}
		visible := map[string]bool{}
		for _, column := range columns {
			if column.Schema != "agentsql_files" || column.Table == "" {
				failure(w, ErrGateway)
				return
			}
			visible[column.Table] = true
		}
		if len(visible) != len(prior.Sheets) {
			failure(w, ErrGateway)
			return
		}
		for index, sheet := range prior.Sheets {
			if sheet.SheetName != plan.Sheets[index].Name || sheet.Rows != len(plan.Sheets[index].Rows) ||
				sheet.TableComment != "" || !managedFileNamePattern.MatchString(sheet.TableName) || !visible[sheet.TableName] {
				failure(w, ErrGateway)
				return
			}
		}
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		if err := h.service.semanticAdmin(r.Context(), p); err != nil {
			failure(w, err)
			return
		}
		current, currentErr := h.service.binding(r.Context(), p, h.service.ManagedFileDatasource)
		if currentErr != nil || current != binding {
			failure(w, ErrGateway)
			return
		}
		okay(w, map[string]any{"filename": stage.FilePath, "sheets": prior.Sheets})
		return
	}
	if !errors.Is(err, ErrNotFound) {
		failure(w, err)
		return
	}
	importer, ok := h.service.Gateway.(managedFileImporter)
	if !ok {
		failure(w, ErrGateway)
		return
	}
	result, err := importer.ImportFile(r.Context(), p, binding, stage, request, plan)
	if err != nil {
		failure(w, err)
		return
	}
	undo := func(cause error) error {
		deleter, ok := h.service.Gateway.(managedFileDeleter)
		if !ok {
			return cause
		}
		child := binding
		child.BindingID = result.ChildBindingID
		child.Name = result.ChildName
		child.ConnectorToken = result.ConnectorToken
		recovery, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
		defer cancel()
		return errors.Join(cause, deleter.DeleteFile(recovery, p, child, result.OperationID))
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, undo(err))
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, undo(err))
		return
	}
	current, err := h.service.binding(r.Context(), p, h.service.ManagedFileDatasource)
	if err != nil || current != binding {
		failure(w, undo(ErrGateway))
		return
	}
	if !managedFileOperationPattern.MatchString(result.OperationID) || len(result.Sheets) != len(plan.Sheets) ||
		result.ChildBindingID == "" || !managedFileChildNamePattern.MatchString(result.ChildName) || result.ConnectorToken == "" {
		failure(w, undo(ErrGateway))
		return
	}
	receipt := managedFileReceipt{OperationID: result.OperationID, BindingID: result.ChildBindingID,
		DatasourceName: result.ChildName, ConnectorToken: result.ConnectorToken,
		Filename: stage.FilePath, FileSHA256: hex.EncodeToString(fileHash[:]), SelectionSHA256: selectionHash,
		Sheets: result.Sheets, Created: time.Now().UTC()}
	if err := h.service.Store.writeSemanticWithDetails(r.Context(), managedFileReceiptKey(p, stage.FilePath), receipt, 0,
		"file-import.published", map[string]any{"operation_id": result.OperationID, "sheets": len(result.Sheets)}); err != nil {
		failure(w, undo(err))
		return
	}
	okay(w, map[string]any{"filename": stage.FilePath, "sheets": result.Sheets})
}

func (service *Service) saveFileStage(ctx context.Context, p Principal, stage fileStage, sheets []FileSheet) error {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return err
	}
	if stage.FilePath == "" || len(stage.Data) == 0 || len(stage.Data) > maxFileUpload || stage.Expires.Before(time.Now().UTC()) || len(sheets) == 0 || len(sheets) > 8 {
		return errFileImport
	}
	rows := 0
	for _, sheet := range sheets {
		rows += sheet.Count
	}
	key := fileStageKey(p)
	var previous fileStage
	revision, err := service.Store.get(ctx, key, &previous)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	digest := sha256.Sum256(stage.Data)
	return service.Store.writeSemanticWithDetails(ctx, key, stage, revision, "file-stage.parse",
		map[string]any{"file_path": stage.FilePath, "file_sha256": hex.EncodeToString(digest[:]),
			"size_bytes": len(stage.Data), "sheets": len(sheets), "rows": rows})
}

// prepareFileImport reads only the current user's encrypted stage. It neither
// provisions a table nor confers an execution capability.
func (service *Service) prepareFileImport(ctx context.Context, p Principal, request FileImportRequest) (fileImportPlan, error) {
	_, plan, err := service.prepareFileImportWithStage(ctx, p, request)
	return plan, err
}

func (service *Service) prepareFileImportWithStage(ctx context.Context, p Principal, request FileImportRequest) (fileStage, fileImportPlan, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return fileStage{}, fileImportPlan{}, err
	}
	var stage fileStage
	if _, err := service.Store.get(ctx, fileStageKey(p), &stage); err != nil {
		return fileStage{}, fileImportPlan{}, err
	}
	plan, err := planFileImport(stage, request, time.Now().UTC())
	if err != nil {
		return fileStage{}, fileImportPlan{}, err
	}
	if err := service.semanticAdmin(ctx, p); err != nil {
		return fileStage{}, fileImportPlan{}, err
	}
	return stage, plan, nil
}
