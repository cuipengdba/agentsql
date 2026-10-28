package chatbi

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type managedRemarkSheet struct {
	name string
	rows [][]string
}

// This independently written XLSX encoder emits only inline text cells. The
// original client expects one table-list sheet and one sheet per visible table.
func managedRemarkWorkbook(sheets []managedRemarkSheet) ([]byte, error) {
	if len(sheets) < 2 || len(sheets) > 9 {
		return nil, errSemanticWorkbook
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	writePart := func(name string, body []byte) error {
		writer, err := archive.Create(name)
		if err != nil {
			return err
		}
		_, err = writer.Write(body)
		return err
	}
	var types, workbook, relationships strings.Builder
	types.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	workbook.WriteString(`<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	relationships.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for index, sheet := range sheets {
		if sheet.name == "" || len(sheet.rows) == 0 || len(sheet.rows) > 257 ||
			len(sheet.rows[0]) < 2 || len(sheet.rows[0]) > 3 {
			return nil, errSemanticWorkbook
		}
		part := fmt.Sprintf("xl/worksheets/sheet%d.xml", index+1)
		fmt.Fprintf(&types, `<Override PartName="/%s" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, part)
		fmt.Fprintf(&workbook, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, sheet.name, index+1, index+1)
		fmt.Fprintf(&relationships, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, index+1, index+1)
	}
	types.WriteString(`</Types>`)
	workbook.WriteString(`</sheets></workbook>`)
	relationships.WriteString(`</Relationships>`)
	if err := writePart("[Content_Types].xml", []byte(types.String())); err != nil {
		return nil, err
	}
	if err := writePart("_rels/.rels", []byte(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`)); err != nil {
		return nil, err
	}
	if err := writePart("xl/workbook.xml", []byte(workbook.String())); err != nil {
		return nil, err
	}
	if err := writePart("xl/_rels/workbook.xml.rels", []byte(relationships.String())); err != nil {
		return nil, err
	}
	for index, sheet := range sheets {
		var data bytes.Buffer
		data.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
		for rowIndex, row := range sheet.rows {
			if len(row) != len(sheet.rows[0]) {
				return nil, errSemanticWorkbook
			}
			fmt.Fprintf(&data, `<row r="%d">`, rowIndex+1)
			for columnIndex, value := range row {
				if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 32767 ||
					strings.IndexFunc(value, unicode.IsControl) >= 0 {
					return nil, errSemanticWorkbook
				}
				fmt.Fprintf(&data, `<c r="%s%d" t="inlineStr"><is><t xml:space="preserve">`, spreadsheetColumn(columnIndex), rowIndex+1)
				if err := xml.EscapeText(&data, []byte(value)); err != nil {
					return nil, err
				}
				data.WriteString(`</t></is></c>`)
			}
			data.WriteString(`</row>`)
		}
		data.WriteString(`</sheetData></worksheet>`)
		if data.Len() > maxSemanticWorkbook || writePart(fmt.Sprintf("xl/worksheets/sheet%d.xml", index+1), data.Bytes()) != nil {
			return nil, errSemanticWorkbook
		}
	}
	if err := archive.Close(); err != nil || output.Len() > maxSemanticWorkbook {
		return nil, errSemanticWorkbook
	}
	return output.Bytes(), nil
}

func (h *Handler) exportManagedFileRemarks(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	datasource := ID(r.PathValue("id"))
	if datasource == "0" {
		sheets := []managedRemarkSheet{
			{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"},
				{"Sheet0", "user", "示例用户表备注"}, {"Sheet1", "course", "示例课程表备注"}}},
			{name: "Sheet0", rows: [][]string{{"字段名", "字段备注"},
				{"id", "示例用户 ID"}, {"name", "示例姓名"}}},
			{name: "Sheet1", rows: [][]string{{"字段名", "字段备注"},
				{"id", "示例课程 ID"}, {"user_id", "示例用户 ID"}}},
		}
		data, err := managedRemarkWorkbook(sheets)
		if err != nil {
			failure(w, ErrUnavailable)
			return
		}
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		if err := h.service.auditSemanticExport(r.Context(), p, "datasource.remark-template", 0); err != nil {
			failure(w, err)
			return
		}
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="agentsql-remark-template.xlsx"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}
	if !datasource.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, datasource)
	if err != nil || binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		failure(w, ErrNotFound)
		return
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(datasource, columns)
	if !valid || len(tables) > 8 {
		failure(w, ErrGateway)
		return
	}
	if len(tables) == 0 {
		writeJSON(w, http.StatusBadRequest, "No tables")
		return
	}
	_, importedSheets, err := decodeSQLBotExcelConfig(binding.Configuration)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	imported := make(map[string]bool, len(importedSheets))
	for _, sheet := range importedSheets {
		imported[sheet.TableName] = true
	}
	tableComments, err := managedFileTableComments(binding)
	if err != nil {
		failure(w, err)
		return
	}
	fieldComments, err := managedFileFieldComments(binding)
	if err != nil {
		failure(w, err)
		return
	}
	sheets := []managedRemarkSheet{{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"}}}}
	for index, table := range tables {
		if table.schema != "agentsql_files" || !managedFileNamePattern.MatchString(table.name) ||
			!imported[table.name] || len(table.columns) > 256 {
			failure(w, ErrGateway)
			return
		}
		sheetName := fmt.Sprintf("Sheet%d", index)
		sheets[0].rows = append(sheets[0].rows, []string{sheetName, table.name, tableComments[table.name]})
		fieldRows := [][]string{{"字段名", "字段备注"}}
		for _, column := range table.columns {
			fieldRows = append(fieldRows, []string{column.Name, fieldComments[managedFileFieldCommentKey(table.name, column.Name)]})
		}
		sheets = append(sheets, managedRemarkSheet{name: sheetName, rows: fieldRows})
	}
	data, err := managedRemarkWorkbook(sheets)
	if err != nil {
		failure(w, ErrUnavailable)
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
	if err := h.service.auditSemanticExport(r.Context(), p, "datasource.remark", len(tables)); err != nil {
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
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="agentsql-datasource-`+string(datasource)+`-remarks.xlsx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
