package chatbi

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSemanticWorkbookReadRoundTripAndFormulaRefusal(t *testing.T) {
	data, err := semanticWorkbook([][]string{{"term", "description"}, {"GMV", "synthetic sum"}, {"Sales", "a,b"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readSemanticWorkbook(data, 2)
	if err != nil || len(rows) != 2 || rows[0][0] != "GMV" || rows[1][1] != "a,b" {
		t.Fatalf("safe workbook did not round-trip: %v %v", rows, err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var modified bytes.Buffer
	writer := zip.NewWriter(&modified)
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if file.Name == "xl/worksheets/sheet1.xml" {
			marker := []byte(`<is><t xml:space="preserve">GMV`)
			changed := bytes.Replace(body, marker, append([]byte(`<f>1+1</f>`), marker...), 1)
			if bytes.Equal(changed, body) {
				t.Fatal("synthetic formula mutation did not find the target cell")
			}
			body = changed
		}
		part, err := writer.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readSemanticWorkbook(modified.Bytes(), 2); err == nil {
		t.Fatal("formula-bearing upload accepted")
	}
	if _, err := readSemanticWorkbook(data, 3); err == nil {
		t.Fatal("missing required header accepted")
	}
}

// Optional local pinned SQLBot fixture. CI does not depend on external files.
func TestPinnedSQLBotWorkbookTemplateCanBeRead(t *testing.T) {
	directory := os.Getenv("AGENTSQL_SQLBOT_TEMPLATE_DIR")
	if directory == "" {
		t.Skip("pinned SQLBot templates are not configured")
	}
	for kind, columns := range map[string]int{"terminology": 6, "data-training": 4} {
		data, err := os.ReadFile(filepath.Join(directory, "sqlbot-"+kind+"-template.private.xlsx"))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := readSemanticWorkbook(data, columns)
		if err != nil || len(rows) == 0 || strings.TrimSpace(rows[0][0]) == "" {
			t.Fatalf("pinned %s template rejected: %v %v", kind, rows, err)
		}
	}
}
