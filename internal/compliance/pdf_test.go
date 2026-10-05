package compliance

import (
	"bytes"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRenderAuditPDFUnicodeAndMultiplePages(t *testing.T) {
	actor, action, sqlText := "审计员", "query", "SELECT 姓名 FROM 客户"
	logs := make([]model.AuditLog, 80)
	for index := range logs {
		logs[index] = model.AuditLog{ID: int64(index + 1), TS: time.Date(2026, 10, 5, 1, index%60, 0, 0, time.UTC),
			Decision: "allow", ActorID: &actor, Action: &action, SQLNorm: &sqlText}
	}
	report, err := RenderAuditPDF(logs, PDFOptions{GeneratedAt: time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC), Filters: map[string]string{"datasource": "生产库"}})
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(report, []byte("%PDF-1.7")))
	require.Contains(t, string(report), "/Type /Pages")
	require.Contains(t, string(report), "/Encoding /UniGB-UCS2-H")
	require.Greater(t, bytes.Count(report, []byte("/Type /Page ")), 1)
	require.True(t, bytes.HasSuffix(report, []byte("%%EOF\n")))
}

func TestRenderAuditPDFEmpty(t *testing.T) {
	report, err := RenderAuditPDF(nil, PDFOptions{GeneratedAt: time.Unix(1, 0)})
	require.NoError(t, err)
	require.NotEmpty(t, report)
	require.Contains(t, string(report), "xref")
}

func TestPDFType0TextEncoding(t *testing.T) {
	require.Equal(t, "4E2D6587", pdfUTF16Hex("中文"))
	require.Equal(t, "003F", pdfUTF16Hex("😀"))
}
