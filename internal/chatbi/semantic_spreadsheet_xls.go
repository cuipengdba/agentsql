package chatbi

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"time"

	xls "github.com/b0r1ssh/go-xls"
)

// readLegacySemanticWorkbook reads BIFF cells as inert values under the same
// size and row bounds as semantic XLSX imports. It intentionally does not use
// fileSheetFromRows: SQLBot's semantic templates have long display headers,
// while managed-file column names have a much shorter database-name limit.
func readLegacySemanticWorkbook(data []byte, columns int) (result [][]string, err error) {
	defer func() {
		if recover() != nil {
			result, err = nil, errSemanticWorkbook
		}
	}()
	if len(data) < 8 || len(data) > maxSemanticUpload || columns < 1 || columns > 16 ||
		!bytes.Equal(data[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return nil, errSemanticWorkbook
	}
	workbook, err := xls.OpenReader(&boundedXLSReader{data: data})
	if err != nil || workbook == nil {
		return nil, errSemanticWorkbook
	}
	names := workbook.SheetNames()
	if len(names) == 0 || len(names) > 8 {
		return nil, errSemanticWorkbook
	}
	result = make([][]string, 0)
	seenNames := make(map[string]bool, len(names))
	decodedBytes := 0
	for _, name := range names {
		if seenNames[name] {
			return nil, errSemanticWorkbook
		}
		seenNames[name] = true
		previous := -1
		for row := range workbook.ReadRows(name) {
			if row.Error != nil || row.Index <= previous || row.Index > 1000 || row.LastCol >= 16 {
				return nil, errSemanticWorkbook
			}
			if previous == -1 && (row.Index != 0 || row.FirstCol != 0 || int(row.LastCol)+1 < columns) {
				return nil, errSemanticWorkbook
			}
			previous = row.Index
			values := make([]string, columns)
			seen := make([]bool, 16)
			for _, cell := range row.Cells {
				if cell.Index < 0 || cell.Index >= 16 || seen[cell.Index] {
					return nil, errSemanticWorkbook
				}
				seen[cell.Index] = true
				var value string
				switch typed := cell.Value.(type) {
				case nil:
				case string:
					value = typed
				case float64:
					if math.IsInf(typed, 0) || math.IsNaN(typed) {
						return nil, errSemanticWorkbook
					}
					value = strconv.FormatFloat(typed, 'f', -1, 64)
				case bool:
					value = strconv.FormatBool(typed)
				case time.Time:
					value = typed.Format("2006-01-02 15:04:05")
				default:
					return nil, errSemanticWorkbook
				}
				decodedBytes += len(value)
				if len(value) > 65536 || decodedBytes > 16<<20 || !validFileText(value) {
					return nil, errSemanticWorkbook
				}
				if cell.Index < columns {
					values[cell.Index] = value
				}
			}
			if row.Index != 0 {
				result = append(result, values)
				if len(result) > 1000 {
					return nil, errSemanticWorkbook
				}
			}
		}
		if previous == -1 {
			return nil, errSemanticWorkbook
		}
	}
	return result, nil
}

func semanticUploadExtension(filename string) string {
	name := strings.ToLower(filename)
	if strings.HasSuffix(name, ".xlsx") {
		return ".xlsx"
	}
	if strings.HasSuffix(name, ".xls") {
		return ".xls"
	}
	return ""
}
