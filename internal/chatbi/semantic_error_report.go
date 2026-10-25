package chatbi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Only the latest error workbook is retained per workspace/user. This bounds
// storage even when a client repeatedly uploads invalid workbooks. The file
// name is a lookup token, never a filesystem path or an authority by itself.
type semanticErrorReport struct {
	Filename string    `json:"filename"`
	Workbook []byte    `json:"workbook"`
	Kind     string    `json:"kind"`
	Rows     int       `json:"rows"`
	Expires  time.Time `json:"expires"`
}

func semanticErrorKey(p Principal) objectKey {
	return objectKey{"semantic-error", string(p.Workspace), string(p.User.ID), "latest"}
}

func (service *Service) saveSemanticErrorReport(ctx context.Context, p Principal, kind string, failedRows [][]string) (string, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return "", err
	}
	if kind != "terminology" && kind != "sql-example" || len(failedRows) == 0 || len(failedRows) > 1000 {
		return "", ErrInvalidSemantic
	}
	rows := make([][]string, 0, len(failedRows)+1)
	rows = append(rows, append(semanticHeaders(kind, p.User.Language, false), "Error"))
	rows = append(rows, failedRows...)
	workbook, err := semanticWorkbook(rows)
	if err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", ErrUnavailable
	}
	filename := "agentsql-" + kind + "-" + string(id) + "_error.xlsx"
	report := semanticErrorReport{Filename: filename, Workbook: workbook, Kind: kind, Rows: len(failedRows), Expires: time.Now().UTC().Add(7 * 24 * time.Hour)}
	key := semanticErrorKey(p)
	var prior semanticErrorReport
	revision, err := service.Store.get(ctx, key, &prior)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if err := service.Store.writeSemantic(ctx, key, report, revision, "semantic-error.write"); err != nil {
		return "", err
	}
	return filename, nil
}

func (h *Handler) downloadSemanticError(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		File string `json:"file"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if input.File == "" || len(input.File) > 128 || strings.ContainsAny(input.File, "/\\\x00") || !strings.HasSuffix(input.File, "_error.xlsx") {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid error workbook name")
		return
	}
	var report semanticErrorReport
	if _, err := h.service.Store.get(r.Context(), semanticErrorKey(p), &report); err != nil {
		failure(w, err)
		return
	}
	if report.Filename != input.File || report.Expires.Before(time.Now().UTC()) || len(report.Workbook) < 4 || len(report.Workbook) > maxSemanticWorkbook || report.Rows < 1 || report.Rows > 1000 {
		failure(w, ErrNotFound)
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
	if err := h.service.auditSemanticErrorDownload(r.Context(), p, report.Filename, report.Rows); err != nil {
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
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+report.Filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(report.Workbook)
}

func (service *Service) auditSemanticErrorDownload(ctx context.Context, p Principal, filename string, rows int) error {
	id, err := newID()
	if err != nil {
		return ErrUnavailable
	}
	key := objectKey{"management-audit", "system", "system", string(id)}
	return service.Store.put(ctx, key, map[string]any{"action": "semantic-error.download", "workspace": p.Workspace, "user": p.User.ID, "file": filename, "rows": rows, "time": time.Now().UTC()}, 0)
}
