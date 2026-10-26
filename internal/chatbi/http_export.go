package chatbi

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxCSVExportBytes = 2 << 20

func csvCell(value string) string {
	trimmed := strings.TrimLeftFunc(value, unicode.IsSpace)
	first, _ := utf8.DecodeRuneInString(trimmed)
	if strings.ContainsRune("=+-@", first) {
		return "'" + value
	}
	return value
}

func encodeCSVExport(data QueryData) ([]byte, error) {
	if len(data.Fields) == 0 || len(data.Fields) > 256 || len(data.Data) > 1000 || data.AuditID <= 0 || data.Truncated {
		return nil, ErrUnavailable
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	writer.UseCRLF = true
	fields := make([]string, len(data.Fields))
	seen := map[string]bool{}
	for index, field := range data.Fields {
		if field == "" || len(field) > 256 || seen[field] {
			return nil, ErrUnavailable
		}
		seen[field] = true
		fields[index] = csvCell(field)
	}
	if writer.Write(fields) != nil {
		return nil, ErrUnavailable
	}
	for _, item := range data.Data {
		row := make([]string, len(data.Fields))
		for index, field := range data.Fields {
			value, exists := item[field]
			if !exists {
				return nil, ErrUnavailable
			}
			if value != nil {
				plain, ok := value.(string)
				if !ok || len(plain) > 65536 {
					return nil, ErrUnavailable
				}
				row[index] = csvCell(plain)
			}
		}
		if writer.Write(row) != nil || buffer.Len() > maxCSVExportBytes {
			return nil, ErrUnavailable
		}
	}
	writer.Flush()
	if writer.Error() != nil || buffer.Len() > maxCSVExportBytes {
		return nil, ErrUnavailable
	}
	return buffer.Bytes(), nil
}

func encodeWorkbookExport(data QueryData) ([]byte, error) {
	if len(data.Fields) == 0 || len(data.Fields) > 256 || len(data.Data) > 1000 || data.AuditID <= 0 || data.Truncated {
		return nil, ErrUnavailable
	}
	rows := make([][]string, 0, len(data.Data)+1)
	header := make([]string, len(data.Fields))
	seen := map[string]bool{}
	for index, field := range data.Fields {
		if field == "" || len(field) > 256 || seen[field] {
			return nil, ErrUnavailable
		}
		seen[field] = true
		header[index] = field
	}
	rows = append(rows, header)
	for _, item := range data.Data {
		row := make([]string, len(data.Fields))
		for index, field := range data.Fields {
			value, exists := item[field]
			if !exists {
				return nil, ErrUnavailable
			}
			if value != nil {
				plain, ok := value.(string)
				if !ok || len(plain) > 65536 {
					return nil, ErrUnavailable
				}
				row[index] = plain
			}
		}
		rows = append(rows, row)
	}
	encoded, err := semanticWorkbook(rows) // Inline strings cannot become spreadsheet formulas.
	if err != nil {
		return nil, ErrUnavailable
	}
	return encoded, nil
}

// Export runs the same current-policy query used by historical reads. The
// candidate is assembled in memory and an audit receipt commits before bytes
// are sent. A revoked or replaced local datasource binding fails closed.
func (h *Handler) exportCSV(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	h.exportRecord(w, r, p, token, false)
}

func (h *Handler) exportSQLBotWorkbook(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	h.exportRecord(w, r, p, token, true)
}

func (h *Handler) exportRecord(w http.ResponseWriter, r *http.Request, p Principal, token string, workbook bool) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid record ID")
		return
	}
	metadata, err := h.service.Record(r.Context(), p, id, false)
	if err != nil {
		failure(w, err)
		return
	}
	if workbook {
		chatID := ID(r.PathValue("chatID"))
		if !chatID.Valid() || metadata.Chat != chatID {
			failure(w, ErrNotFound)
			return
		}
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, metadata.Datasource)
	if err != nil {
		failure(w, err)
		return
	}
	record, err := h.service.Record(r.Context(), p, id, true)
	if err != nil {
		failure(w, err)
		return
	}
	if record.Data == nil || !record.Finish || record.Error != "" {
		failure(w, ErrNotFound)
		return
	}
	var encoded []byte
	if workbook {
		encoded, err = encodeWorkbookExport(*record.Data)
	} else {
		encoded, err = encodeCSVExport(*record.Data)
	}
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	receiptID, err := newID()
	if err != nil {
		failure(w, err)
		return
	}
	receipt := map[string]any{"record": id, "datasource": record.Datasource, "audit_id": record.Data.AuditID,
		"rows": len(record.Data.Data), "time": time.Now().UTC()}
	format := "csv"
	if workbook {
		format = "xlsx"
	}
	if err := h.service.Store.writeSemanticWithDetails(r.Context(), objectKey{"export-receipt", string(p.Workspace), string(p.User.ID), string(receiptID)},
		receipt, 0, "chatbi.record.export."+format, map[string]any{"record": id, "audit_id": record.Data.AuditID, "rows": len(record.Data.Data)}); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	if workbook {
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	} else {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=agentsql-record-"+string(id)+".csv")
	}
	w.Header().Set("X-AgentSQL-Audit-ID", strconv.FormatInt(record.Data.AuditID, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}
