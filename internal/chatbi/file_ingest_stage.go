package chatbi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
