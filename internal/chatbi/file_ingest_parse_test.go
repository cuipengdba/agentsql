package chatbi

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileImportParserXLS(t *testing.T) {
	data, err := os.ReadFile("testdata/file-import-sales.xls")
	require.NoError(t, err)
	sheets, err := parseFileImport("sales.xls", data)
	require.NoError(t, err)
	require.Len(t, sheets, 1)
	require.Equal(t, "Sales", sheets[0].Name)
	require.Equal(t, 2, sheets[0].Count)
	require.Equal(t, []FileField{{Name: "region", Type: "string"}, {Name: "amount", Type: "int"}}, sheets[0].Fields)
	require.Equal(t, int64(10), sheets[0].Preview[0]["amount"])
	require.Equal(t, "west", sheets[0].Preview[1]["region"])
	unicodeData, err := os.ReadFile("testdata/file-import-unicode.xls")
	require.NoError(t, err)
	unicodeSheets, err := parseFileImport("销售.xls", unicodeData)
	require.NoError(t, err)
	require.Equal(t, "销售", unicodeSheets[0].Name)
	require.Equal(t, "华东", unicodeSheets[0].Preview[0]["区域"])
	require.Equal(t, int64(20), unicodeSheets[0].Preview[1]["金额"])

	for _, invalid := range [][]byte{data[:8], data[:len(data)/2], []byte("region,amount\neast,10\n")} {
		_, err := parseFileImport("sales.xls", invalid)
		require.ErrorIs(t, err, errFileImport)
	}
	// A cyclic OLE directory sector must terminate with an error, not pin a
	// request worker while replaying the same four directory entries forever.
	cyclic := bytes.Clone(data)
	directorySector := binary.LittleEndian.Uint32(cyclic[48:52])
	fatSector := binary.LittleEndian.Uint32(cyclic[76:80])
	fatEntry := 512 + int(fatSector)*512 + int(directorySector)*4
	require.LessOrEqual(t, fatEntry+4, len(cyclic))
	binary.LittleEndian.PutUint32(cyclic[fatEntry:fatEntry+4], directorySector)
	_, err = parseFileImport("cycle.xls", cyclic)
	require.ErrorIs(t, err, errFileImport)
	_, err = parseFileImport("sales.csv", data)
	require.ErrorIs(t, err, errFileImport)
}

func TestFileImportParserCSVAndXLSX(t *testing.T) {
	csvData := []byte("region,amount\neast,10\nwest,20\n")
	sheets, err := parseFileImport("sales.csv", csvData)
	require.NoError(t, err)
	require.Len(t, sheets, 1)
	require.Equal(t, "Sheet1", sheets[0].Name)
	require.Equal(t, 2, sheets[0].Count)
	require.Equal(t, []FileField{{Name: "region", Type: "string"}, {Name: "amount", Type: "int"}}, sheets[0].Fields)
	require.Equal(t, int64(10), sheets[0].Preview[0]["amount"])

	workbook, err := semanticWorkbook([][]string{{"region", "amount"}, {"east", "10"}, {"west", "20"}})
	require.NoError(t, err)
	sheets, err = parseFileImport("sales.xlsx", workbook)
	require.NoError(t, err)
	require.Len(t, sheets, 1)
	require.Equal(t, "Sheet1", sheets[0].Name)
	require.Equal(t, 2, sheets[0].Count)
	require.Equal(t, []FileField{{Name: "region", Type: "string"}, {Name: "amount", Type: "int"}}, sheets[0].Fields)
	require.Equal(t, int64(20), sheets[0].Preview[1]["amount"])
	absoluteTarget := rewriteFileWorkbook(t, workbook, func(name, body string) string {
		if name == "xl/_rels/workbook.xml.rels" {
			return strings.Replace(body, `Target="worksheets/sheet1.xml"`, `Target="/xl/worksheets/sheet1.xml"`, 1)
		}
		return body
	})
	sheets, err = parseFileImport("absolute.xlsx", absoluteTarget)
	require.NoError(t, err, "common XLSX writers use package-absolute worksheet targets")
	require.Equal(t, int64(20), sheets[0].Preview[1]["amount"])
	sparse := rewriteFileWorkbook(t, workbook, func(name, body string) string {
		if name == "xl/worksheets/sheet1.xml" {
			const cell = `<c r="B2" t="inlineStr"><is><t xml:space="preserve">10</t></is></c>`
			require.Contains(t, body, cell)
			return strings.Replace(body, cell, "", 1)
		}
		return body
	})
	sheets, err = parseFileImport("sparse.xlsx", sparse)
	require.NoError(t, err)
	require.Nil(t, sheets[0].Preview[0]["amount"])
	require.Equal(t, int64(20), sheets[0].Preview[1]["amount"])
}

func TestFileImportParserRejectsUnsafeOrAmbiguousData(t *testing.T) {
	for name, input := range map[string][]byte{
		"duplicate header": []byte("region,region\neast,west\n"),
		"uneven row":       []byte("region,amount\neast\n"),
		"nul":              []byte("region,amount\neast,\x0010\n"),
		"invalid utf8":     []byte{'r', 'e', 'g', 'i', 'o', 'n', '\n', 0xff},
		"too many rows":    []byte("region\n" + strings.Repeat("east\n", maxFileRows+1)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseFileImport("sales.csv", input)
			require.ErrorIs(t, err, errFileImport)
		})
	}
	_, err := parseFileImport("../sales.csv", []byte("region\neast\n"))
	require.ErrorIs(t, err, errFileImport)
	_, err = parseFileImport("sales.xls", []byte("legacy"))
	require.ErrorIs(t, err, errFileImport)
	_, err = parseFileImport("sales.csv", bytes.Repeat([]byte("a"), maxFileUpload+1))
	require.ErrorIs(t, err, errFileImport)
	workbook, err := semanticWorkbook([][]string{{"region", "amount"}, {"east", "10"}})
	require.NoError(t, err)
	formula := rewriteFileWorkbook(t, workbook, func(name, body string) string {
		if name == "xl/worksheets/sheet1.xml" {
			const cell = `<c r="B2" t="inlineStr"><is><t xml:space="preserve">10</t></is></c>`
			require.Contains(t, body, cell)
			return strings.Replace(body, cell, `<c r="B2"><f>1+1</f><v>2</v></c>`, 1)
		}
		return body
	})
	_, err = parseFileImport("formula.xlsx", formula)
	require.ErrorIs(t, err, errFileImport)
	bomb := rewriteFileWorkbook(t, workbook, func(name, body string) string {
		if name == "xl/worksheets/sheet1.xml" {
			return body + strings.Repeat(" ", (8<<20)+1)
		}
		return body
	})
	require.Less(t, len(bomb), maxFileUpload, "compressed input must fit the upload limit")
	_, err = parseFileImport("compressed-bomb.xlsx", bomb)
	require.ErrorIs(t, err, errFileImport)
	for _, target := range []string{"../worksheets/sheet1.xml", "/xl/worksheets/../worksheets/sheet1.xml", "//xl/worksheets/sheet1.xml", "https://example.invalid/sheet1.xml"} {
		t.Run(target, func(t *testing.T) {
			changed := rewriteFileWorkbook(t, workbook, func(name, body string) string {
				if name == "xl/_rels/workbook.xml.rels" {
					return strings.Replace(body, `Target="worksheets/sheet1.xml"`, `Target="`+target+`"`, 1)
				}
				return body
			})
			_, err := parseFileImport("unsafe.xlsx", changed)
			require.ErrorIs(t, err, errFileImport)
		})
	}
	externalMode := rewriteFileWorkbook(t, workbook, func(name, body string) string {
		if name == "xl/_rels/workbook.xml.rels" {
			return strings.Replace(body, `Target="worksheets/sheet1.xml"`, `Target="worksheets/sheet1.xml" TargetMode="External"`, 1)
		}
		return body
	})
	_, err = parseFileImport("external.xlsx", externalMode)
	require.ErrorIs(t, err, errFileImport)
}

func rewriteFileWorkbook(t *testing.T, original []byte, rewrite func(name, body string) string) []byte {
	t.Helper()
	source, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	require.NoError(t, err)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range source.File {
		reader, err := file.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		part, err := writer.Create(file.Name)
		require.NoError(t, err)
		_, err = part.Write([]byte(rewrite(file.Name, string(body))))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}
