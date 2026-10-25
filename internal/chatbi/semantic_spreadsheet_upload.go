package chatbi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func readSemanticUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSemanticUpload+65536)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, errSemanticWorkbook
	}
	var data []byte
	seen := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil || seen || part.FormName() != "file" || !strings.HasSuffix(strings.ToLower(part.FileName()), ".xlsx") {
			return nil, errSemanticWorkbook
		}
		seen = true
		data, err = io.ReadAll(io.LimitReader(part, maxSemanticUpload+1))
		_ = part.Close()
		if err != nil || len(data) > maxSemanticUpload {
			return nil, errSemanticWorkbook
		}
	}
	if !seen {
		return nil, errSemanticWorkbook
	}
	return data, nil
}

func (service *Service) semanticBindingNames(ctx context.Context, p Principal) (map[string]ID, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return nil, err
	}
	raw, err := service.Store.list(ctx, "binding", string(p.Workspace), string(p.User.ID))
	if err != nil {
		return nil, err
	}
	result := map[string]ID{}
	for _, item := range raw {
		var binding Binding
		if json.Unmarshal(item, &binding) != nil || binding.User != p.User.ID || binding.Workspace != p.Workspace || !binding.Datasource.Valid() || strings.TrimSpace(binding.Name) == "" {
			return nil, ErrUnavailable
		}
		key := strings.ToLower(strings.TrimSpace(binding.Name))
		if _, duplicate := result[key]; duplicate {
			return nil, ErrConflict
		}
		result[key] = binding.Datasource
	}
	return result, nil
}

func splitSemanticNames(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			result = append(result, name)
		}
	}
	return result
}

type semanticUploadReport struct {
	SuccessCount   int              `json:"success_count"`
	FailedCount    int              `json:"failed_count"`
	DuplicateCount int              `json:"duplicate_count"`
	OriginalCount  int              `json:"original_count"`
	ErrorExcelFile *string          `json:"error_excel_filename"`
	FailedRecords  []map[string]any `json:"failed_records,omitempty"`
}

func (h *Handler) uploadSemantics(w http.ResponseWriter, r *http.Request, p Principal, token, kind string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	data, err := readSemanticUpload(w, r)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid or oversized XLSX upload")
		return
	}
	columns := 6
	if kind == "sql-example" {
		columns = 4
	}
	rows, err := readSemanticWorkbook(data, columns)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid or unsupported XLSX workbook")
		return
	}
	bindings, err := h.service.semanticBindingNames(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	known := map[string]bool{}
	if kind == "terminology" {
		items, err := h.service.Terminologies(r.Context(), p)
		if err != nil {
			failure(w, err)
			return
		}
		for _, item := range items {
			known[strings.ToLower(strings.TrimSpace(item.Word))] = true
		}
	} else {
		items, err := h.service.Examples(r.Context(), p)
		if err != nil {
			failure(w, err)
			return
		}
		for _, item := range items {
			known[strings.ToLower(strings.TrimSpace(item.Question))] = true
		}
	}
	report := semanticUploadReport{FailedRecords: []map[string]any{}}
	for rowIndex, row := range rows {
		blank := true
		for _, value := range row {
			if strings.TrimSpace(value) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		report.OriginalCount++
		key := strings.ToLower(strings.TrimSpace(row[0]))
		if key != "" && known[key] {
			report.DuplicateCount++
			continue
		}
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		invalid := false
		if kind == "terminology" {
			all := strings.ToLower(strings.TrimSpace(row[4]))
			term := Terminology{Word: strings.TrimSpace(row[0]), Description: strings.TrimSpace(row[2]), OtherWords: splitSemanticNames(row[1]), Enabled: true}
			switch all {
			case "y", "yes", "true":
			case "n", "no", "false":
				term.SpecificDS = true
				for _, name := range splitSemanticNames(row[3]) {
					id := bindings[strings.ToLower(name)]
					number, err := strconv.ParseInt(string(id), 10, 64)
					if err != nil || number <= 0 {
						invalid = true
						break
					}
					term.DatasourceIDs = append(term.DatasourceIDs, number)
				}
			default:
				invalid = true
			}
			if strings.TrimSpace(row[5]) != "" || term.Description == "" || !validTerm(term) {
				invalid = true
			}
			if !invalid {
				_, err = h.service.SaveTerminology(r.Context(), p, term)
			}
		} else {
			id := bindings[strings.ToLower(strings.TrimSpace(row[2]))]
			number, numberErr := strconv.ParseInt(string(id), 10, 64)
			workspace, workspaceErr := strconv.ParseInt(string(p.Workspace), 10, 64)
			example := SQLExample{Workspace: workspace, Datasource: number, Question: strings.TrimSpace(row[0]), Description: strings.TrimSpace(row[1]), Enabled: true}
			if numberErr != nil || workspaceErr != nil || strings.TrimSpace(row[3]) != "" || !validExample(example, p.Workspace) {
				invalid = true
			}
			if !invalid {
				_, err = h.service.SaveExample(r.Context(), p, example)
			}
		}
		if invalid {
			report.FailedCount++
			report.FailedRecords = append(report.FailedRecords, map[string]any{"row": rowIndex + 2, "reason": "invalid or unsupported metadata"})
			continue
		}
		if err != nil {
			// Each successful row is independently committed with its own audit;
			// a failed row never escapes as a success or un-audited object.
			failure(w, err)
			return
		}
		report.SuccessCount++
		known[key] = true
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, report)
}

func (h *Handler) terminologyUpload(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	h.uploadSemantics(w, r, p, token, "terminology")
}

func (h *Handler) exampleUpload(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	h.uploadSemantics(w, r, p, token, "sql-example")
}
