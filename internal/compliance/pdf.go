// Package compliance renders bounded, self-contained compliance artifacts.
// It intentionally has no browser, native-library, or network dependency.
package compliance

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	pdfLinesPerPage  = 51
	pdfMaxFieldRunes = 240
)

// PDFOptions describes the request metadata printed in an audit PDF. Filter
// values must already be safe to disclose; callers must not pass secrets or a
// free-form keyword value.
type PDFOptions struct {
	GeneratedAt time.Time
	Filters     map[string]string
}

// RenderAuditPDF renders every selected event into a compact, multi-page A4
// report. The standard CJK Type0 font mapping keeps the implementation pure Go
// and allows Chinese audit values without invoking a browser or external tool.
func RenderAuditPDF(logs []model.AuditLog, options PDFOptions) ([]byte, error) {
	generatedAt := options.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	lines := []string{
		"AgentSQL Audit Compliance Report",
		"Generated at (UTC): " + generatedAt.Format(time.RFC3339),
		"Events: " + strconv.Itoa(len(logs)),
		"Integrity notice: this PDF is an application report, not WORM evidence or an external timestamp.",
		"",
		"Applied filters",
	}
	filterNames := make([]string, 0, len(options.Filters))
	for name := range options.Filters {
		filterNames = append(filterNames, name)
	}
	sort.Strings(filterNames)
	if len(filterNames) == 0 {
		lines = append(lines, "  none")
	}
	for _, name := range filterNames {
		lines = append(lines, "  "+cleanPDFText(name, 64)+": "+cleanPDFText(options.Filters[name], 160))
	}

	decisionCounts := make(map[string]int)
	actionCounts := make(map[string]int)
	for _, log := range logs {
		decisionCounts[emptyLabel(log.Decision)]++
		actionCounts[emptyLabel(pointerValue(log.Action))]++
	}
	lines = append(lines, "", "Decision distribution: "+formatCounts(decisionCounts))
	lines = append(lines, "Action distribution: "+formatCounts(actionCounts), "", "Event details")
	if len(logs) == 0 {
		lines = append(lines, "  No matching events.")
	}
	for _, log := range logs {
		actor := pointerValue(log.ActorID)
		if actor == "" {
			actor = pointerValue(log.AgentID)
		}
		first := fmt.Sprintf("#%d  %s  decision=%s  action=%s  actor=%s  datasource=%s",
			log.ID, log.TS.UTC().Format(time.RFC3339Nano), emptyLabel(log.Decision),
			emptyLabel(pointerValue(log.Action)), emptyLabel(actor), emptyLabel(pointerValue(log.DatasourceID)))
		lines = append(lines, first)
		second := "  statement=" + emptyLabel(pointerValue(log.StmtType)) +
			"  objects=" + emptyLabel(pointerValue(log.Objects)) +
			"  error_code=" + emptyLabel(pointerValue(log.ErrorCode))
		lines = append(lines, second)
		if sql := pointerValue(log.SQLNorm); sql != "" {
			lines = append(lines, "  sql_norm="+cleanPDFText(sql, pdfMaxFieldRunes))
		} else if sql := pointerValue(log.SQLRaw); sql != "" {
			lines = append(lines, "  sql_raw="+cleanPDFText(sql, pdfMaxFieldRunes))
		}
		if message := pointerValue(log.ErrorMsg); message != "" {
			lines = append(lines, "  error="+cleanPDFText(message, pdfMaxFieldRunes))
		}
		lines = append(lines, "")
	}

	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		// STSong is a monospaced CID font at the PDF level. Keeping lines to
		// 56 runes prevents both ASCII and CJK content from crossing the A4
		// content box even when a viewer uses conservative glyph widths.
		wrapped = append(wrapped, wrapPDFLine(line, 56)...)
	}
	if len(wrapped) == 0 {
		wrapped = []string{"AgentSQL Audit Compliance Report"}
	}
	pages := make([][]string, 0, (len(wrapped)+pdfLinesPerPage-1)/pdfLinesPerPage)
	for len(wrapped) > 0 {
		count := min(len(wrapped), pdfLinesPerPage)
		pages = append(pages, append([]string(nil), wrapped[:count]...))
		wrapped = wrapped[count:]
	}
	return renderPDFPages(pages, generatedAt)
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func emptyLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "<none>"
	}
	return cleanPDFText(value, pdfMaxFieldRunes)
}

func formatCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", cleanPDFText(key, 64), counts[key]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func cleanPDFText(value string, maxRunes int) string {
	value = strings.Map(func(character rune) rune {
		switch character {
		case '\r', '\n', '\t':
			return ' '
		}
		if character < 0x20 || character == 0x7f || character > utf8.MaxRune {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-3]) + "..."
	}
	return value
}

func wrapPDFLine(value string, width int) []string {
	if value == "" {
		return []string{""}
	}
	runes := []rune(cleanPDFText(value, 4096))
	result := make([]string, 0, (len(runes)+width-1)/width)
	for len(runes) > width {
		cut := width
		for index := width; index > width/2; index-- {
			if runes[index-1] == ' ' {
				cut = index
				break
			}
		}
		result = append(result, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	result = append(result, strings.TrimSpace(string(runes)))
	return result
}

func renderPDFPages(pages [][]string, generatedAt time.Time) ([]byte, error) {
	pageCount := len(pages)
	// IDs 1..5 are catalog/pages/fonts, each page uses two IDs, and the
	// final ID is the document info dictionary. objectCount is /Size and is
	// therefore one greater than the highest object ID.
	objectCount := 7 + pageCount*2
	objects := make([][]byte, objectCount)
	objects[1] = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	pageReferences := make([]string, 0, pageCount)
	for page := range pages {
		pageReferences = append(pageReferences, fmt.Sprintf("%d 0 R", 6+page*2))
	}
	objects[2] = []byte(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", pageCount, strings.Join(pageReferences, " ")))
	objects[3] = []byte("<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [4 0 R] >>")
	objects[4] = []byte("<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> /FontDescriptor 5 0 R /DW 1000 >>")
	objects[5] = []byte("<< /Type /FontDescriptor /FontName /STSong-Light /Flags 4 /FontBBox [-25 -254 1000 880] /ItalicAngle 0 /Ascent 752 /Descent -271 /CapHeight 737 /StemV 58 >>")
	for pageIndex, pageLines := range pages {
		pageObject := 6 + pageIndex*2
		contentObject := pageObject + 1
		objects[pageObject] = []byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentObject))
		var stream bytes.Buffer
		for lineIndex, line := range pageLines {
			fontSize := 8
			if pageIndex == 0 && lineIndex == 0 {
				fontSize = 13
			}
			y := 806 - lineIndex*15
			fmt.Fprintf(&stream, "BT /F1 %d Tf 44 %d Td <%s> Tj ET\n", fontSize, y, pdfUTF16Hex(line))
		}
		footer := fmt.Sprintf("Page %d of %d | generated %s", pageIndex+1, pageCount, generatedAt.Format(time.RFC3339))
		fmt.Fprintf(&stream, "BT /F1 8 Tf 44 32 Td <%s> Tj ET\n", pdfUTF16Hex(footer))
		content := stream.Bytes()
		objects[contentObject] = append([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n", len(content))), content...)
		objects[contentObject] = append(objects[contentObject], []byte("endstream")...)
	}

	infoObject := objectCount - 1
	objects[infoObject] = []byte(fmt.Sprintf("<< /Title (AgentSQL Audit Compliance Report) /Creator (AgentSQL) /CreationDate (D:%sZ) >>", generatedAt.Format("20060102150405")))
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, objectCount)
	for objectID := 1; objectID < objectCount; objectID++ {
		offsets[objectID] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n", objectID)
		output.Write(objects[objectID])
		output.WriteString("\nendobj\n")
	}
	xrefOffset := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n", objectCount)
	output.WriteString("0000000000 65535 f \n")
	for objectID := 1; objectID < objectCount; objectID++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[objectID])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", objectCount, infoObject, xrefOffset)
	return output.Bytes(), nil
}

func pdfUTF16Hex(value string) string {
	// UniGB-UCS2-H reads these bytes as character codes. A UTF-16 BOM is
	// appropriate for PDF metadata strings, but would become an extra glyph in
	// a Type0 font text-showing string. UCS-2 cannot represent non-BMP runes.
	runes := []rune(value)
	encoded := make([]byte, len(runes)*2)
	for index, character := range runes {
		if character > 0xffff || character >= 0xd800 && character <= 0xdfff {
			character = '?'
		}
		encoded[index*2] = byte(character >> 8)
		encoded[index*2+1] = byte(character)
	}
	return strings.ToUpper(hex.EncodeToString(encoded))
}
