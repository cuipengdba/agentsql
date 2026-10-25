package chatbi

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

const maxSemanticUpload = 4 << 20

type workbookStrings struct {
	Items []struct {
		Text string `xml:"t"`
		Runs []struct {
			Text string `xml:"t"`
		} `xml:"r"`
	} `xml:"si"`
}

type workbookSheet struct {
	Rows []struct {
		Number int `xml:"r,attr"`
		Cells  []struct {
			Reference string  `xml:"r,attr"`
			Type      string  `xml:"t,attr"`
			Value     string  `xml:"v"`
			Formula   *string `xml:"f"`
			Inline    struct {
				Text string `xml:"t"`
				Runs []struct {
					Text string `xml:"t"`
				} `xml:"r"`
			} `xml:"is"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
}

// readSemanticWorkbook accepts the simple cell types produced by SQLBot's
// XLSX exports and this application's templates. It never evaluates formulas,
// macros, external links, or arbitrary package paths.
func readSemanticWorkbook(data []byte, columns int) ([][]string, error) {
	if len(data) < 4 || len(data) > maxSemanticUpload || columns < 1 || columns > 16 {
		return nil, errSemanticWorkbook
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > 64 {
		return nil, errSemanticWorkbook
	}
	parts := map[string]*zip.File{}
	var total uint64
	for _, file := range archive.File {
		name := file.Name
		if path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(strings.ToLower(name), "externallink") || strings.Contains(strings.ToLower(name), "vbaproject") || strings.Contains(strings.ToLower(name), "embedding") || file.UncompressedSize64 > 8<<20 {
			return nil, errSemanticWorkbook
		}
		total += file.UncompressedSize64
		if total > 16<<20 || parts[name] != nil {
			return nil, errSemanticWorkbook
		}
		parts[name] = file
	}
	readPart := func(name string) ([]byte, error) {
		file := parts[name]
		if file == nil {
			return nil, errSemanticWorkbook
		}
		reader, err := file.Open()
		if err != nil {
			return nil, errSemanticWorkbook
		}
		defer reader.Close()
		body, err := io.ReadAll(io.LimitReader(reader, 8<<20+1))
		if err != nil || len(body) > 8<<20 || uint64(len(body)) != file.UncompressedSize64 {
			return nil, errSemanticWorkbook
		}
		return body, nil
	}
	shared := []string{}
	if parts["xl/sharedStrings.xml"] != nil {
		body, err := readPart("xl/sharedStrings.xml")
		if err != nil {
			return nil, err
		}
		var stringsFile workbookStrings
		if xml.Unmarshal(body, &stringsFile) != nil || len(stringsFile.Items) > 10001*16 {
			return nil, errSemanticWorkbook
		}
		for _, item := range stringsFile.Items {
			value := item.Text
			for _, run := range item.Runs {
				value += run.Text
			}
			shared = append(shared, value)
		}
	}
	sheets := []string{}
	for name := range parts {
		if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
			index := strings.TrimSuffix(strings.TrimPrefix(name, "xl/worksheets/sheet"), ".xml")
			number, err := strconv.Atoi(index)
			if err != nil || number < 1 || number > 8 || name != "xl/worksheets/sheet"+strconv.Itoa(number)+".xml" {
				return nil, errSemanticWorkbook
			}
			sheets = append(sheets, name)
		}
	}
	if len(sheets) == 0 || len(sheets) > 8 || parts["xl/workbook.xml"] == nil {
		return nil, errSemanticWorkbook
	}
	sort.Strings(sheets)
	rows := make([][]string, 0)
	for _, name := range sheets {
		body, err := readPart(name)
		if err != nil {
			return nil, err
		}
		var sheet workbookSheet
		if xml.Unmarshal(body, &sheet) != nil || len(sheet.Rows) == 0 || len(sheet.Rows) > 1001 {
			return nil, errSemanticWorkbook
		}
		previousRow := 0
		for rowIndex, source := range sheet.Rows {
			if source.Number <= previousRow || source.Number > 1001 || len(source.Cells) > 16 || rowIndex == 0 && source.Number != 1 {
				return nil, errSemanticWorkbook
			}
			previousRow = source.Number
			if rowIndex == 0 && len(source.Cells) < columns {
				return nil, errSemanticWorkbook
			}
			row := make([]string, columns)
			seen := make([]bool, columns)
			for _, cell := range source.Cells {
				column, err := workbookColumn(cell.Reference, source.Number)
				if err != nil || column >= columns || seen[column] || cell.Formula != nil {
					return nil, errSemanticWorkbook
				}
				seen[column] = true
				value := cell.Value
				switch cell.Type {
				case "s":
					index, err := strconv.Atoi(value)
					if err != nil || index < 0 || index >= len(shared) {
						return nil, errSemanticWorkbook
					}
					value = shared[index]
				case "inlineStr":
					value = cell.Inline.Text
					for _, run := range cell.Inline.Runs {
						value += run.Text
					}
				case "", "n":
				case "str", "e", "b":
					return nil, errSemanticWorkbook
				default:
					return nil, errSemanticWorkbook
				}
				if len(value) > 65536 || strings.ContainsRune(value, '\x00') {
					return nil, errSemanticWorkbook
				}
				row[column] = value
			}
			if rowIndex == 0 {
				// The first row of every sheet is its column header.
				continue
			}
			rows = append(rows, row)
			if len(rows) > 1000 {
				return nil, errSemanticWorkbook
			}
		}
	}
	return rows, nil
}

func workbookColumn(reference string, row int) (int, error) {
	index := 0
	for index < len(reference) && reference[index] >= 'A' && reference[index] <= 'Z' {
		index++
	}
	if index == 0 || reference[index:] != strconv.Itoa(row) {
		return 0, errSemanticWorkbook
	}
	column := 0
	for _, letter := range reference[:index] {
		column = column*26 + int(letter-'A'+1)
		if column > 16 {
			return 0, errSemanticWorkbook
		}
	}
	return column - 1, nil
}
