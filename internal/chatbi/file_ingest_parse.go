package chatbi

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxFileUpload = 4 << 20
	maxFileRows   = 1000
	maxFileFields = 32
)

var errFileImport = errors.New("file data exceeds supported import bounds")

type FileField struct {
	Name string `json:"fieldName"`
	Type string `json:"fieldType"`
}

type FileSheet struct {
	Name    string           `json:"sheetName"`
	Fields  []FileField      `json:"fields"`
	Preview []map[string]any `json:"data"`
	Count   int              `json:"rows"`
	Rows    [][]string       `json:"-"`
}

// parseFileImport is the bounded, inert first stage of managed file ingestion.
// It does not create a database object or grant any gateway permission.
func parseFileImport(filename string, data []byte) ([]FileSheet, error) {
	if len(data) == 0 || len(data) > maxFileUpload || len(filename) == 0 || len(filename) > 128 ||
		strings.ContainsAny(filename, "/\\\x00") {
		return nil, errFileImport
	}
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".csv"):
		rows, err := parseCSVRows(data)
		if err != nil {
			return nil, err
		}
		sheet, err := fileSheetFromRows("Sheet1", rows)
		if err != nil {
			return nil, err
		}
		return []FileSheet{sheet}, nil
	case strings.HasSuffix(strings.ToLower(filename), ".xlsx"):
		return parseXLSXSheets(data)
	default:
		return nil, errFileImport
	}
}

func parseCSVRows(data []byte) ([][]string, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errFileImport
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	rows := make([][]string, 0, 32)
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(row) == 0 || len(row) > maxFileFields || len(rows) > maxFileRows {
			return nil, errFileImport
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, errFileImport
	}
	return rows, nil
}

func fileSheetFromRows(name string, rows [][]string) (FileSheet, error) {
	if name == "" || len(name) > 64 || strings.TrimSpace(name) != name || !validFileText(name) || len(rows) == 0 || len(rows) > maxFileRows+1 ||
		len(rows[0]) == 0 || len(rows[0]) > maxFileFields {
		return FileSheet{}, errFileImport
	}
	headers := rows[0]
	seen := make(map[string]bool, len(headers))
	for _, header := range headers {
		if header == "" || strings.TrimSpace(header) != header || len([]byte(header)) > 63 || !validFileText(header) || seen[strings.ToLower(header)] {
			return FileSheet{}, errFileImport
		}
		seen[strings.ToLower(header)] = true
	}
	result := FileSheet{Name: name, Count: len(rows) - 1, Rows: make([][]string, 0, len(rows)-1)}
	for _, row := range rows[1:] {
		if len(row) != len(headers) {
			return FileSheet{}, errFileImport
		}
		for _, value := range row {
			if len(value) > 65536 || !validFileText(value) {
				return FileSheet{}, errFileImport
			}
		}
		result.Rows = append(result.Rows, append([]string(nil), row...))
	}
	for index, header := range headers {
		kind := inferFileField(result.Rows, index)
		result.Fields = append(result.Fields, FileField{Name: header, Type: kind})
	}
	for _, row := range result.Rows {
		if len(result.Preview) == 10 {
			break
		}
		item := make(map[string]any, len(headers))
		for index, header := range headers {
			item[header] = filePreviewValue(result.Fields[index].Type, row[index])
		}
		result.Preview = append(result.Preview, item)
	}
	if result.Preview == nil {
		result.Preview = []map[string]any{}
	}
	return result, nil
}

func validFileText(value string) bool {
	return utf8.ValidString(value) && strings.IndexFunc(value, func(r rune) bool {
		return r == 0 || unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) < 0
}

func inferFileField(rows [][]string, index int) string {
	integer, floating, nonempty := true, true, false
	for _, row := range rows {
		value := strings.TrimSpace(row[index])
		if value == "" {
			continue
		}
		nonempty = true
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			integer = false
		}
		if n, err := strconv.ParseFloat(value, 64); err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			floating = false
		}
	}
	if nonempty && integer {
		return "int"
	}
	if nonempty && floating {
		return "float"
	}
	return "string"
}

func filePreviewValue(kind, value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	switch kind {
	case "int":
		n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n
	case "float":
		n, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return n
	default:
		return value
	}
}

type fileWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		RID  string `xml:"id,attr"`
	} `xml:"sheets>sheet"`
}

type fileRelationships struct {
	Items []struct {
		ID         string `xml:"Id,attr"`
		Type       string `xml:"Type,attr"`
		Target     string `xml:"Target,attr"`
		TargetMode string `xml:"TargetMode,attr"`
	} `xml:"Relationship"`
}

func parseXLSXSheets(data []byte) ([]FileSheet, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > 128 {
		return nil, errFileImport
	}
	parts := make(map[string]*zip.File, len(archive.File))
	var total uint64
	for _, file := range archive.File {
		name, lower := file.Name, strings.ToLower(file.Name)
		if path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") ||
			strings.Contains(lower, "externallink") || strings.Contains(lower, "vbaproject") ||
			strings.Contains(lower, "embedding") || strings.HasPrefix(lower, "customxml/") ||
			file.UncompressedSize64 > 8<<20 || parts[name] != nil {
			return nil, errFileImport
		}
		total += file.UncompressedSize64
		if total > 16<<20 {
			return nil, errFileImport
		}
		parts[name] = file
	}
	read := func(name string) ([]byte, error) {
		file := parts[name]
		if file == nil {
			return nil, errFileImport
		}
		reader, err := file.Open()
		if err != nil {
			return nil, errFileImport
		}
		defer reader.Close()
		body, err := io.ReadAll(io.LimitReader(reader, 8<<20+1))
		if err != nil || len(body) > 8<<20 || uint64(len(body)) != file.UncompressedSize64 {
			return nil, errFileImport
		}
		return body, nil
	}
	workbookXML, err := read("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	relationsXML, err := read("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	var book fileWorkbook
	var links fileRelationships
	if xml.Unmarshal(workbookXML, &book) != nil || xml.Unmarshal(relationsXML, &links) != nil || len(book.Sheets) == 0 || len(book.Sheets) > 8 {
		return nil, errFileImport
	}
	paths := map[string]string{}
	for _, relation := range links.Items {
		if !strings.HasSuffix(relation.Type, "/worksheet") {
			continue
		}
		if relation.TargetMode != "" && relation.TargetMode != "Internal" {
			return nil, errFileImport
		}
		target, err := workbookSheetTarget(relation.Target)
		if err != nil {
			return nil, errFileImport
		}
		if relation.ID == "" || !strings.HasPrefix(target, "xl/worksheets/") || paths[relation.ID] != "" {
			return nil, errFileImport
		}
		paths[relation.ID] = target
	}
	shared := []string{}
	if parts["xl/sharedStrings.xml"] != nil {
		body, err := read("xl/sharedStrings.xml")
		if err != nil {
			return nil, err
		}
		var stringsFile workbookStrings
		if xml.Unmarshal(body, &stringsFile) != nil || len(stringsFile.Items) > (maxFileRows+1)*maxFileFields*8 {
			return nil, errFileImport
		}
		for _, item := range stringsFile.Items {
			value := item.Text
			for _, run := range item.Runs {
				value += run.Text
			}
			if len(value) > 65536 || !validFileText(value) {
				return nil, errFileImport
			}
			shared = append(shared, value)
		}
	}
	sheets := make([]FileSheet, 0, len(book.Sheets))
	seenNames := map[string]bool{}
	for _, item := range book.Sheets {
		filename := paths[item.RID]
		if filename == "" || seenNames[strings.ToLower(item.Name)] {
			return nil, errFileImport
		}
		seenNames[strings.ToLower(item.Name)] = true
		body, err := read(filename)
		if err != nil {
			return nil, err
		}
		rows, err := parseFileSheetXML(body, shared)
		if err != nil {
			return nil, err
		}
		sheet, err := fileSheetFromRows(item.Name, rows)
		if err != nil {
			return nil, err
		}
		sheets = append(sheets, sheet)
	}
	return sheets, nil
}

// OOXML relationships may use a path relative to xl/workbook.xml or an
// absolute package path. Accept both without allowing navigation segments,
// alternate separators, URLs, or a target outside the worksheet directory.
func workbookSheetTarget(raw string) (string, error) {
	if raw == "" || strings.ContainsAny(raw, "\\\x00?#") || strings.HasPrefix(raw, "//") {
		return "", errFileImport
	}
	absolute := strings.HasPrefix(raw, "/")
	if absolute {
		raw = strings.TrimPrefix(raw, "/")
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errFileImport
		}
	}
	if !absolute {
		raw = path.Join("xl", raw)
	}
	if !strings.HasPrefix(raw, "xl/worksheets/") || path.Clean(raw) != raw {
		return "", errFileImport
	}
	return raw, nil
}

func parseFileSheetXML(body []byte, shared []string) ([][]string, error) {
	var sheet workbookSheet
	if xml.Unmarshal(body, &sheet) != nil || len(sheet.Rows) == 0 || len(sheet.Rows) > maxFileRows+1 {
		return nil, errFileImport
	}
	rows := make([][]string, 0, len(sheet.Rows))
	previous := 0
	for _, source := range sheet.Rows {
		if source.Number <= previous || source.Number > maxFileRows+1 || len(source.Cells) > maxFileFields {
			return nil, errFileImport
		}
		for previous+1 < source.Number {
			rows = append(rows, nil)
			previous++
		}
		previous = source.Number
		row := make([]string, maxFileFields)
		seen := map[int]bool{}
		last := -1
		for _, cell := range source.Cells {
			column, err := fileCellColumn(cell.Reference, source.Number)
			if err != nil || seen[column] || cell.Formula != nil {
				return nil, errFileImport
			}
			seen[column] = true
			value := cell.Value
			switch cell.Type {
			case "s":
				index, err := strconv.Atoi(value)
				if err != nil || index < 0 || index >= len(shared) {
					return nil, errFileImport
				}
				value = shared[index]
			case "inlineStr":
				value = cell.Inline.Text
				for _, run := range cell.Inline.Runs {
					value += run.Text
				}
			case "", "n":
			case "str", "e", "b":
				return nil, errFileImport
			default:
				return nil, errFileImport
			}
			if len(value) > 65536 || !validFileText(value) {
				return nil, errFileImport
			}
			row[column] = value
			if column > last {
				last = column
			}
		}
		rows = append(rows, row[:last+1])
	}
	width := len(rows[0])
	if width == 0 {
		return nil, errFileImport
	}
	for index := 1; index < len(rows); index++ {
		if len(rows[index]) > width {
			return nil, errFileImport
		}
		padded := make([]string, width)
		copy(padded, rows[index])
		rows[index] = padded
	}
	return rows, nil
}

func fileCellColumn(reference string, row int) (int, error) {
	index := 0
	for index < len(reference) && reference[index] >= 'A' && reference[index] <= 'Z' {
		index++
	}
	if index == 0 || reference[index:] != strconv.Itoa(row) {
		return 0, errFileImport
	}
	column := 0
	for _, letter := range reference[:index] {
		column = column*26 + int(letter-'A'+1)
		if column > maxFileFields {
			return 0, errFileImport
		}
	}
	return column - 1, nil
}
