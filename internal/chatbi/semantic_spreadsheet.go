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
	if len(rows) == 0 || len(rows) > 10001 || len(rows[0]) == 0 || len(rows[0]) > 16 {
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

func semanticHeaders(kind string, language string) []string {
	english := strings.HasPrefix(strings.ToLower(language), "en")
	if kind == "terminology" {
		if english {
			return []string{"Term", "Synonyms", "Description", "Effective data sources", "All data sources", "Advanced application"}
		}
		return []string{"术语名称", "同义词", "术语描述", "生效数据源", "全部数据源", "高级应用"}
	}
	if english {
		return []string{"Question", "Sample SQL", "Effective data source", "Advanced application"}
	}
	return []string{"问题描述", "样例 SQL", "生效数据源", "高级应用"}
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
	rows := [][]string{semanticHeaders("terminology", p.User.Language), {"GMV", "成交额", "订单金额总和", "", "Y", ""}}
	h.sendSemanticWorkbook(w, r, p, token, "terminology-template", rows, false)
}

func (h *Handler) exampleTemplate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	rows := [][]string{semanticHeaders("sql-example", p.User.Language), {"查询销售额", "SELECT amount FROM sales", "销售数据源", ""}}
	h.sendSemanticWorkbook(w, r, p, token, "sql-example-template", rows, false)
}

func (h *Handler) terminologyExport(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	terms, err := h.service.Terminologies(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("word")))
	rows := [][]string{semanticHeaders("terminology", p.User.Language)}
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
	rows := [][]string{semanticHeaders("sql-example", p.User.Language)}
	for _, example := range examples {
		if filter == "" || strings.Contains(strings.ToLower(example.Question), filter) {
			rows = append(rows, []string{example.Question, example.Description, example.DatasourceName, ""})
		}
	}
	h.sendSemanticWorkbook(w, r, p, token, "sql-example", rows, true)
}
