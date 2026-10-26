package chatbi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxSemanticWorkbook = 8 << 20

var errSemanticWorkbook = errors.New("semantic workbook exceeds supported bounds")

// semanticWorkbook writes a deliberately small, formula-free XLSX. Every cell
// is an OOXML inline string, including SQL text and strings beginning with '='.
func semanticWorkbook(rows [][]string) ([]byte, error) {
	if len(rows) == 0 || len(rows) > 10001 || len(rows[0]) == 0 || len(rows[0]) > 256 {
		return nil, errSemanticWorkbook
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
	}
	for _, part := range parts {
		writer, err := archive.Create(part.name)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write([]byte(part.body)); err != nil {
			return nil, err
		}
	}
	writer, err := archive.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)); err != nil {
		return nil, err
	}
	plainBytes := 0
	for rowIndex, row := range rows {
		if len(row) != len(rows[0]) {
			return nil, errSemanticWorkbook
		}
		if _, err := fmt.Fprintf(writer, `<row r="%d">`, rowIndex+1); err != nil {
			return nil, err
		}
		for columnIndex, value := range row {
			plainBytes += len(value)
			if plainBytes > maxSemanticWorkbook || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 32767 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) >= 0 {
				return nil, errSemanticWorkbook
			}
			address := spreadsheetColumn(columnIndex) + strconv.Itoa(rowIndex+1)
			if _, err := fmt.Fprintf(writer, `<c r="%s" t="inlineStr"><is><t xml:space="preserve">`, address); err != nil {
				return nil, err
			}
			if err := xml.EscapeText(writer, []byte(value)); err != nil {
				return nil, err
			}
			if _, err := writer.Write([]byte(`</t></is></c>`)); err != nil {
				return nil, err
			}
		}
		if _, err := writer.Write([]byte(`</row>`)); err != nil {
			return nil, err
		}
	}
	if _, err := writer.Write([]byte(`</sheetData></worksheet>`)); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if output.Len() > maxSemanticWorkbook {
		return nil, errSemanticWorkbook
	}
	return output.Bytes(), nil
}

func spreadsheetColumn(index int) string {
	result := ""
	for index >= 0 {
		result = string(rune('A'+index%26)) + result
		index = index/26 - 1
	}
	return result
}

func semanticHeaders(kind string, language string, template bool) []string {
	english := strings.HasPrefix(strings.ToLower(language), "en")
	if kind == "terminology" {
		if english {
			if template {
				return []string{"Terminology Name (Required)", `Synonyms (Multiple supported, separated by ",")`, "Terminology Description (Required)", `Effective Data Sources (Multiple supported, separated by ",")`, "All Data Sources (Y: Apply to all data sources, N: Apply to specified data sources)", "Advanced Application"}
			}
			return []string{"Terminology Name", "Synonyms", "Terminology Description", "Effective Data Sources", "All Data Sources", "Advanced Application"}
		}
		if template {
			return []string{"术语名称（必填）", `同义词（支持多个，用","分割）`, "术语描述（必填）", `生效数据源（支持多个，用","分割）`, "所有数据源（Y：应用到全部数据源，N：应用到指定数据源）", "高级应用"}
		}
		return []string{"术语名称", "同义词", "术语描述", "生效数据源", "所有数据源", "高级应用"}
	}
	if english {
		if template {
			return []string{"Problem Description (Required)", "Sample SQL (Required)", "Effective Data Sources", "Advanced Application"}
		}
		return []string{"Problem Description", "Sample SQL", "Effective Data Sources", "Advanced Application"}
	}
	if template {
		return []string{"问题描述（必填）", "示例 SQL（必填）", "生效数据源", "高级应用"}
	}
	return []string{"问题描述", "示例 SQL", "生效数据源", "高级应用"}
}

func (h *Handler) sendSemanticWorkbook(w http.ResponseWriter, r *http.Request, p Principal, token, kind string, rows [][]string, audit bool) {
	data, err := semanticWorkbook(rows)
	if err != nil {
		failure(w, ErrUnavailable)
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
	if audit {
		if err := h.service.auditSemanticExport(r.Context(), p, kind, len(rows)-1); err != nil {
			failure(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="agentsql-`+kind+`.xlsx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (service *Service) auditSemanticExport(ctx context.Context, p Principal, kind string, count int) error {
	id, err := newID()
	if err != nil {
		return err
	}
	key := objectKey{"management-audit", "system", "system", string(id)}
	return service.Store.put(ctx, key, map[string]any{"action": kind + ".export", "workspace": p.Workspace, "user": p.User.ID, "rows": count, "time": time.Now().UTC()}, 0)
}

func (h *Handler) terminologyTemplate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	rows := [][]string{semanticHeaders("terminology", p.User.Language, true)}
	if strings.HasPrefix(strings.ToLower(p.User.Language), "en") {
		rows = append(rows, []string{"Term1", "Synonym1, Synonym2", "Term1 Description", "Datasource1, Datasource2", "N", ""}, []string{"Term2", "Synonym3", "Term2 Description", "", "Y", ""})
	} else {
		rows = append(rows, []string{"术语1", "同义词1, 同义词2", "术语1描述", "生效数据源1, 生效数据源2", "N", ""}, []string{"术语2", "同义词3", "术语2描述", "", "Y", ""})
	}
	h.sendSemanticWorkbook(w, r, p, token, "terminology-template", rows, false)
}

func (h *Handler) exampleTemplate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	rows := [][]string{semanticHeaders("sql-example", p.User.Language, true)}
	if strings.HasPrefix(strings.ToLower(p.User.Language), "en") {
		rows = append(rows, []string{"Query all IDs in the TEST table", "SELECT id FROM TEST", "Effective Datasource 1", ""})
	} else {
		rows = append(rows, []string{"查询TEST表内所有ID", "SELECT id FROM TEST", "生效数据源1", ""})
	}
	h.sendSemanticWorkbook(w, r, p, token, "sql-example-template", rows, false)
}

func (h *Handler) terminologyExport(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	terms, err := h.service.Terminologies(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("word")))
	rows := [][]string{semanticHeaders("terminology", p.User.Language, false)}
	for _, term := range terms {
		if filter != "" && !strings.Contains(strings.ToLower(term.Word), filter) {
			continue
		}
		all := "Y"
		if term.SpecificDS {
			all = "N"
		}
		rows = append(rows, []string{term.Word, strings.Join(term.OtherWords, ", "), term.Description, strings.Join(term.DatasourceNames, ", "), all, ""})
	}
	h.sendSemanticWorkbook(w, r, p, token, "terminology", rows, true)
}

func (h *Handler) exampleExport(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	examples, err := h.service.Examples(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("question")))
	rows := [][]string{semanticHeaders("sql-example", p.User.Language, false)}
	for _, example := range examples {
		if filter == "" || strings.Contains(strings.ToLower(example.Question), filter) {
			rows = append(rows, []string{example.Question, example.Description, example.DatasourceName, ""})
		}
	}
	h.sendSemanticWorkbook(w, r, p, token, "sql-example", rows, true)
}
