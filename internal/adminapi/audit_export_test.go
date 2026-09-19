package adminapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

var auditCSVExpectedHeader = []string{
	"时间", "审计ID", "决策", "风险等级", "Agent ID", "数据源", "数据库类型", "会话ID", "对话ID", "MCP工具",
	"语句类型", "命中对象", "命中规则", "原始SQL", "归一化SQL", "预估行数", "返回行数", "耗时毫秒", "客户端IP", "模型",
	"动作", "执行者类型", "执行者ID", "错误信息", "详情JSON",
}

func insertAdminAuditLog(t *testing.T, fixture *adminFixture, log model.AuditLog) model.AuditLog {
	t.Helper()
	if log.Decision == "" {
		log.Decision = "allow"
	}
	inserted, err := fixture.store.AuditLogs().Insert(context.Background(), log)
	require.NoError(t, err)
	return inserted
}

func requestAdminAuditExport(t *testing.T, fixture *adminFixture, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", token)
	}
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	return recorder
}

func readAuditCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	require.True(t, bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}))
	records, err := csv.NewReader(bytes.NewReader(body[3:])).ReadAll()
	require.NoError(t, err)
	return records
}

func TestAdminAuditExportJSONLBackwardCompatibility(t *testing.T) {
	fixture := newAdminFixture(t)
	insertAdminAuditLog(t, fixture, model.AuditLog{
		AgentID: stringPointerAdmin("jsonl-agent"), SQLRaw: stringPointerAdmin("SELECT 1"), Decision: "allow",
	})

	defaultResponse := requestAdminAuditExport(t, fixture, "/api/v1/audit/export", fixture.adminToken)
	explicitResponse := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=jsonl", fixture.adminToken)
	for _, response := range []*httptest.ResponseRecorder{defaultResponse, explicitResponse} {
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, "application/x-ndjson", response.Header().Get("Content-Type"))
		require.Equal(t, "attachment; filename=agentsql-audit.jsonl", response.Header().Get("Content-Disposition"))
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		require.False(t, bytes.HasPrefix(response.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
	}
	require.Equal(t, defaultResponse.Body.Bytes(), explicitResponse.Body.Bytes())

	decoder := json.NewDecoder(bytes.NewReader(defaultResponse.Body.Bytes()))
	decoded := 0
	for {
		var row map[string]json.RawMessage
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Contains(t, row, "agent_id")
		require.Contains(t, row, "sql_raw")
		require.NotContains(t, row, "AgentID")
		decoded++
	}
	require.Equal(t, 1, decoded)
}

func TestAdminAuditExportCSVContract(t *testing.T) {
	fixture := newAdminFixture(t)
	insertAdminAuditLog(t, fixture, model.AuditLog{Decision: "allow"})
	response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=csv", fixture.adminToken)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "text/csv; charset=utf-8", response.Header().Get("Content-Type"))
	require.Equal(t, "attachment; filename=agentsql-audit.csv", response.Header().Get("Content-Disposition"))
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	records := readAuditCSV(t, response.Body.Bytes())
	require.Len(t, records, 2)
	require.Equal(t, auditCSVExpectedHeader, records[0])
	require.Len(t, records[0], 25)
}

func TestAdminAuditExportCSVFormatsNilAndNumbers(t *testing.T) {
	fixture := newAdminFixture(t)
	empty := ""
	inserted := insertAdminAuditLog(t, fixture, model.AuditLog{
		AgentID: &empty, Decision: "allow", RiskLevel: intPointerAdmin(-3), EstRows: int64PointerAdmin(-4),
		RowsReturned: intPointerAdmin(5), LatencyMS: int64PointerAdmin(-6),
	})
	response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=csv", fixture.adminToken)
	require.Equal(t, http.StatusOK, response.Code)
	row := readAuditCSV(t, response.Body.Bytes())[1]

	require.Equal(t, "", row[4])
	require.Equal(t, "", row[5])
	require.Equal(t, strconv.FormatInt(inserted.ID, 10), row[1])
	require.Equal(t, "-3", row[3])
	require.Equal(t, "-4", row[15])
	require.Equal(t, "5", row[16])
	require.Equal(t, "-6", row[17])
	for _, index := range []int{1, 3, 15, 16, 17} {
		require.False(t, strings.HasPrefix(row[index], "'"))
	}
	parsed, err := time.Parse(time.RFC3339Nano, row[0])
	require.NoError(t, err)
	require.Equal(t, time.UTC, parsed.Location())
}

func TestAdminAuditExportCSVEscapesSpecialCharacters(t *testing.T) {
	fixture := newAdminFixture(t)
	original := "SELECT \"a,b\"\nline2\rline3\r\nline4"
	insertAdminAuditLog(t, fixture, model.AuditLog{AgentID: stringPointerAdmin("escape-agent"), SQLRaw: &original})
	response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=csv&agent_id=escape-agent", fixture.adminToken)
	require.Equal(t, http.StatusOK, response.Code)
	records := readAuditCSV(t, response.Body.Bytes())
	// encoding/csv.Reader normalizes CRLF inside quoted fields to LF. The raw
	// byte assertion below separately verifies that Writer preserved the CRLF.
	require.Equal(t, strings.ReplaceAll(original, "\r\n", "\n"), records[1][13])
	require.Contains(t, string(response.Body.Bytes()), `"SELECT ""a,b""`)
	require.Contains(t, response.Body.String(), "\rline3\r\nline4\"")
}

func TestSanitizeAuditCSVText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "equals", input: "=cmd", want: "'=cmd"},
		{name: "plus", input: "+cmd", want: "'+cmd"},
		{name: "minus", input: "-cmd", want: "'-cmd"},
		{name: "at", input: "@cmd", want: "'@cmd"},
		{name: "tab", input: "\tcmd", want: "'\tcmd"},
		{name: "carriage return", input: "\rcmd", want: "'\rcmd"},
		{name: "line feed", input: "\ncmd", want: "'\ncmd"},
		{name: "leading ascii spaces", input: "  =cmd", want: "'  =cmd"},
		{name: "leading ideographic space", input: "　＠cmd", want: "'　＠cmd"},
		{name: "fullwidth equals", input: "＝cmd", want: "'＝cmd"},
		{name: "fullwidth plus", input: "＋cmd", want: "'＋cmd"},
		{name: "fullwidth minus", input: "－cmd", want: "'－cmd"},
		{name: "fullwidth at", input: "＠cmd", want: "'＠cmd"},
		{name: "percent", input: "%cmd", want: "%cmd"},
		{name: "safe", input: "safe text", want: "safe text"},
		{name: "empty", input: "", want: ""},
		{name: "newline after safe text", input: "普通文本\n=cmd", want: "普通文本\n=cmd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, sanitizeAuditCSVText(test.input))
		})
	}
}

func TestAdminAuditExportCSVNeutralizesEveryTextColumn(t *testing.T) {
	fixture := newAdminFixture(t)
	dangerous := "=cmd"
	insertAdminAuditLog(t, fixture, model.AuditLog{
		AgentID: &dangerous, DatasourceID: &dangerous, SessionID: &dangerous, ConversationID: &dangerous,
		MCPTool: &dangerous, DBType: &dangerous, SQLRaw: &dangerous, SQLNorm: &dangerous, StmtType: &dangerous,
		Objects: &dangerous, RuleHits: &dangerous, Decision: "allow", ClientIP: &dangerous, ModelName: &dangerous,
		ErrorMsg: &dangerous, Action: &dangerous, ActorType: &dangerous, ActorID: &dangerous, DetailsJSON: &dangerous,
	})
	response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=csv&agent_id="+url.QueryEscape(dangerous), fixture.adminToken)
	require.Equal(t, http.StatusOK, response.Code)
	row := readAuditCSV(t, response.Body.Bytes())[1]
	for _, index := range []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 18, 19, 20, 21, 22, 23, 24} {
		require.Equalf(t, "'=cmd", row[index], "column %d was not neutralized", index)
	}
	require.Equal(t, "'=cmd", auditCSVRow(auditView{Decision: dangerous})[2])
}

func TestAdminAuditExportCSVAppliesFilters(t *testing.T) {
	fixture := newAdminFixture(t)
	insertAdminAuditLog(t, fixture, model.AuditLog{AgentID: stringPointerAdmin("other"), Decision: "deny", Objects: stringPointerAdmin("public.target")})
	first := insertAdminAuditLog(t, fixture, model.AuditLog{AgentID: stringPointerAdmin("target"), Decision: "deny", Objects: stringPointerAdmin("public.target")})
	insertAdminAuditLog(t, fixture, model.AuditLog{AgentID: stringPointerAdmin("target"), Decision: "allow", Objects: stringPointerAdmin("public.target")})
	second := insertAdminAuditLog(t, fixture, model.AuditLog{AgentID: stringPointerAdmin("target"), Decision: "deny", Objects: stringPointerAdmin("public.target.child")})

	path := "/api/v1/audit/export?format=csv&agent_id=target&decisions=deny&object=public.target"
	response := requestAdminAuditExport(t, fixture, path, fixture.adminToken)
	require.Equal(t, http.StatusOK, response.Code)
	records := readAuditCSV(t, response.Body.Bytes())
	require.Len(t, records, 3)
	require.Equal(t, strconv.FormatInt(second.ID, 10), records[1][1])
	require.Equal(t, strconv.FormatInt(first.ID, 10), records[2][1])
	for _, row := range records[1:] {
		require.Equal(t, "target", row[4])
		require.Equal(t, "deny", row[2])
		require.Contains(t, row[11], "public.target")
	}
}

func TestAdminAuditExportCSVEmptyResultHasHeader(t *testing.T) {
	fixture := newAdminFixture(t)
	response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=csv&agent_id=missing", fixture.adminToken)
	require.Equal(t, http.StatusOK, response.Code)
	records := readAuditCSV(t, response.Body.Bytes())
	require.Equal(t, [][]string{auditCSVExpectedHeader}, records)
}

func TestAdminAuditExportRejectsUnsupportedFormat(t *testing.T) {
	fixture := newAdminFixture(t)
	for _, format := range []string{"pdf", "CSV"} {
		t.Run(format, func(t *testing.T) {
			response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format="+format, fixture.adminToken)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Contains(t, response.Body.String(), "format must be csv or jsonl")
			require.Empty(t, response.Header().Get("Content-Disposition"))
			require.False(t, bytes.HasPrefix(response.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
			var envelope map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		})
	}
}

func TestAdminAuditExportRejectsMoreThan10000Rows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires 10001 real SQLite audit inserts")
	}
	fixture := newAdminFixture(t)
	for index := 0; index < auditExportRowLimit+1; index++ {
		insertAdminAuditLog(t, fixture, model.AuditLog{AgentID: stringPointerAdmin("limit-agent")})
	}
	response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=csv&agent_id=limit-agent", fixture.adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, response.Code)
	require.Contains(t, response.Body.String(), errAuditExportLimit.Error())
	require.Empty(t, response.Header().Get("Content-Disposition"))
	require.NotEqual(t, "text/csv; charset=utf-8", response.Header().Get("Content-Type"))
	require.False(t, bytes.HasPrefix(response.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
}

func TestAdminAuditExportCSVRequiresAdminAuthentication(t *testing.T) {
	fixture := newAdminFixture(t)
	response := requestAdminAuditExport(t, fixture, "/api/v1/audit/export?format=csv", "")
	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Empty(t, response.Header().Get("Content-Disposition"))
	require.False(t, bytes.HasPrefix(response.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
}
