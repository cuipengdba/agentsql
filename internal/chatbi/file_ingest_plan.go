package chatbi

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
)

type FileImportRequest struct {
	FilePath string `json:"filePath"`
	Sheets   []struct {
		Name   string      `json:"sheetName"`
		Fields []FileField `json:"fields"`
	} `json:"sheets"`
}

type fileImportPlan struct {
	Sheets []fileImportSheet
}

type fileImportSheet struct {
	Name   string
	Fields []FileField
	Rows   [][]any
}

// BuildManagedFileSheets independently reparses the original bytes at the
// gateway boundary. The caller may assert a file path and field types, but
// cannot provide physical table names, SQL or trusted converted rows.
func BuildManagedFileSheets(filename string, data []byte, request FileImportRequest) ([]executor.ManagedFileSheet, []string, error) {
	stage := fileStage{FilePath: request.FilePath, Original: filename, Data: data, Expires: time.Now().UTC().Add(time.Minute)}
	plan, err := planFileImport(stage, request, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	sheets := make([]executor.ManagedFileSheet, len(plan.Sheets))
	names := make([]string, len(plan.Sheets))
	for index, sheet := range plan.Sheets {
		names[index] = sheet.Name
		columns := make([]executor.ManagedFileColumn, len(sheet.Fields))
		for columnIndex, field := range sheet.Fields {
			columns[columnIndex] = executor.ManagedFileColumn{Name: field.Name, Type: field.Type}
		}
		sheets[index] = executor.ManagedFileSheet{Columns: columns, Rows: sheet.Rows}
	}
	return sheets, names, nil
}

var decimalImportValue = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]{1,3})?$`)

// planFileImport validates the client's selected columns and types against
// the encrypted staged bytes. It is inert: no SQL, DB handle or grant is
// returned. A future managed provisioner must consume this only after a
// fresh identity check and an independently audited gateway transaction.
func planFileImport(stage fileStage, request FileImportRequest, now time.Time) (fileImportPlan, error) {
	if request.FilePath == "" || request.FilePath != stage.FilePath || strings.ContainsAny(request.FilePath, "/\\\x00") ||
		stage.Expires.IsZero() || !stage.Expires.After(now) || len(request.Sheets) == 0 || len(request.Sheets) > 8 {
		return fileImportPlan{}, errFileImport
	}
	parsed, err := parseFileImport(stage.Original, stage.Data)
	if err != nil {
		return fileImportPlan{}, err
	}
	available := make(map[string]FileSheet, len(parsed))
	for _, sheet := range parsed {
		available[sheet.Name] = sheet
	}
	plan := fileImportPlan{Sheets: make([]fileImportSheet, 0, len(request.Sheets))}
	used := map[string]bool{}
	for _, selected := range request.Sheets {
		sheet, ok := available[selected.Name]
		if !ok || used[selected.Name] || len(selected.Fields) != len(sheet.Fields) {
			return fileImportPlan{}, errFileImport
		}
		used[selected.Name] = true
		part := fileImportSheet{Name: sheet.Name, Fields: append([]FileField(nil), selected.Fields...), Rows: make([][]any, 0, sheet.Count)}
		for index, field := range selected.Fields {
			if field.Name != sheet.Fields[index].Name || !validImportFieldType(field.Type) {
				return fileImportPlan{}, errFileImport
			}
		}
		for _, row := range sheet.Rows {
			values := make([]any, len(row))
			for index, raw := range row {
				value, err := convertFileValue(selected.Fields[index].Type, raw)
				if err != nil {
					return fileImportPlan{}, errFileImport
				}
				values[index] = value
			}
			part.Rows = append(part.Rows, values)
		}
		plan.Sheets = append(plan.Sheets, part)
	}
	return plan, nil
}

func validImportFieldType(value string) bool {
	return value == "int" || value == "float" || value == "datetime" || value == "string"
}

func convertFileValue(kind, raw string) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	switch kind {
	case "string":
		return raw, nil
	case "int":
		value := strings.TrimSpace(raw)
		if len(value) > 128 || !decimalImportValue.MatchString(value) {
			return nil, errFileImport
		}
		rational, ok := new(big.Rat).SetString(value)
		if !ok {
			return nil, errFileImport
		}
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(rational.Num(), rational.Denom(), remainder)
		twiceRemainder := new(big.Int).Abs(remainder)
		twiceRemainder.Mul(twiceRemainder, big.NewInt(2))
		if twiceRemainder.Cmp(rational.Denom()) >= 0 {
			if rational.Sign() < 0 {
				quotient.Sub(quotient, big.NewInt(1))
			} else {
				quotient.Add(quotient, big.NewInt(1))
			}
		}
		if !quotient.IsInt64() {
			return nil, errFileImport
		}
		return quotient.Int64(), nil
	case "float":
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, errFileImport
		}
		return value, nil
	case "datetime":
		value := strings.TrimSpace(raw)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
			if instant, err := time.ParseInLocation(layout, value, time.UTC); err == nil && instant.Year() >= 1 && instant.Year() <= 9999 {
				return instant.UTC(), nil
			}
		}
		return nil, errFileImport
	default:
		return nil, errFileImport
	}
}
