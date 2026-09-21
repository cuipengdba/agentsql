package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRecorderRecordsAllowedDecisionsWithoutChangingFields(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "approve"} {
		t.Run(decision, func(t *testing.T) {
			sink := &fakeAuditSink{}
			recorder := NewRecorder(sink)
			input := completeAuditLog(decision)
			inserted, err := recorder.Record(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, input, inserted)
			require.Equal(t, []model.AuditLog{input}, sink.logs)
		})
	}
}

func TestRecorderRejectsInvalidDecisionWithoutCallingSink(t *testing.T) {
	sink := &fakeAuditSink{}
	recorder := NewRecorder(sink)
	_, err := recorder.Record(context.Background(), model.AuditLog{Decision: "unknown"})
	require.ErrorIs(t, err, ErrInvalidDecision)
	require.Zero(t, sink.calls())
}

func TestRecorderReturnsSinkErrorUnchanged(t *testing.T) {
	expected := errors.New("SQLite disk full")
	sink := &fakeAuditSink{err: expected}
	_, err := NewRecorder(sink).Record(context.Background(), model.AuditLog{Decision: "deny"})
	require.True(t, err == expected)
}

func TestRecorderValidationAndTypedNil(t *testing.T) {
	var sink *fakeAuditSink
	rec := NewRecorder(sink)
	_, err := rec.Record(context.Background(), model.AuditLog{Decision: "allow"})
	require.Error(t, err)
	_, err = rec.Record(nil, model.AuditLog{Decision: "allow"})
	require.Error(t, err)
	var concrete *recorder
	_, err = concrete.Record(context.Background(), model.AuditLog{Decision: "allow"})
	require.Error(t, err)
}

func TestServicePagePassesFilterThrough(t *testing.T) {
	reader := &fakeAuditReader{logs: []model.AuditLog{completeAuditLog("warn")}}
	service := NewService(reader, &fakeAuditSink{})
	filter := model.AuditFilter{AgentID: stringPointer("agent-1"), Decisions: []string{"warn"}}
	page, err := service.Page(context.Background(), filter, 2, 25)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, filter, reader.lastFilter())
	require.Equal(t, 2, reader.lastPage())
	require.Equal(t, 25, reader.lastSize())
}

func TestExportJSONL(t *testing.T) {
	logs := make([]model.AuditLog, 0, 501)
	for index := 0; index < 501; index++ {
		log := completeAuditLog("error")
		log.ID = int64(index + 1)
		log.TS = log.TS.Add(time.Duration(index) * time.Second)
		if index == 0 {
			log.ErrorMsg = nil
			log.ErrorCode = nil
		}
		logs = append(logs, log)
	}
	reader := &fakeAuditReader{logs: logs}
	service := NewService(reader, &fakeAuditSink{})
	filter := model.AuditFilter{DatasourceID: stringPointer("datasource-1")}
	var output bytes.Buffer
	rows, err := service.ExportJSONL(context.Background(), filter, &output)
	require.NoError(t, err)
	require.Equal(t, 501, rows)
	require.Equal(t, []int{1, 2}, reader.pageNumbers())
	for _, size := range reader.pageSizes() {
		require.Equal(t, exportPageSize, size)
	}
	require.Equal(t, filter, reader.lastFilter())

	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	require.Len(t, lines, 501)
	for index, line := range lines {
		var decoded model.AuditLog
		require.NoError(t, json.Unmarshal([]byte(line), &decoded))
		require.Equal(t, logs[index], decoded)
		var raw map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &raw))
		if index == 0 {
			require.NotContains(t, raw, "ErrorMsg")
			require.NotContains(t, raw, "error_code")
		} else {
			require.Equal(t, "DB_OBJECT_NOT_FOUND", raw["error_code"])
		}
		timestamp, ok := raw["TS"].(string)
		require.True(t, ok)
		_, parseError := time.Parse(time.RFC3339, timestamp)
		require.NoError(t, parseError)
	}
}

func TestExportJSONLRejectsNilWriterAndLimit(t *testing.T) {
	service := NewService(&fakeAuditReader{}, &fakeAuditSink{})
	_, err := service.ExportJSONL(context.Background(), model.AuditFilter{}, nil)
	require.Error(t, err)
	var typedNil *bytes.Buffer
	_, err = service.ExportJSONL(context.Background(), model.AuditFilter{}, typedNil)
	require.Error(t, err)

	reader := &fakeAuditReader{totalOverride: exportRowLimit + 1}
	service = NewService(reader, &fakeAuditSink{})
	var output bytes.Buffer
	_, err = service.ExportJSONL(context.Background(), model.AuditFilter{}, &output)
	require.ErrorIs(t, err, ErrExportLimit)
	require.Empty(t, output.String())
}

func TestExportJSONLStopsOnCancelledContextAndFlushesErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &fakeAuditReader{logs: []model.AuditLog{completeAuditLog("allow")}}
	service := NewService(reader, &fakeAuditSink{})
	var output bytes.Buffer
	rows, err := service.ExportJSONL(ctx, model.AuditFilter{}, &output)
	require.Zero(t, rows)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, reader.calls())

	reader = &fakeAuditReader{logs: []model.AuditLog{completeAuditLog("allow")}}
	_, err = NewService(reader, &fakeAuditSink{}).ExportJSONL(
		context.Background(),
		model.AuditFilter{},
		failingWriter{},
	)
	require.Error(t, err)
}

func TestServiceRejectsTypedNilReader(t *testing.T) {
	var reader *fakeAuditReader
	service := NewService(reader, &fakeAuditSink{})
	_, err := service.Page(context.Background(), model.AuditFilter{}, 1, 10)
	require.Error(t, err)
	var output bytes.Buffer
	_, err = service.ExportJSONL(context.Background(), model.AuditFilter{}, &output)
	require.Error(t, err)
}

func TestAuditServiceExposesNoUpdateOrDelete(t *testing.T) {
	serviceType := reflect.TypeOf((*Service)(nil))
	_, hasUpdate := serviceType.MethodByName("Update")
	_, hasDelete := serviceType.MethodByName("Delete")
	require.False(t, hasUpdate)
	require.False(t, hasDelete)
}

type fakeAuditSink struct {
	mu   sync.Mutex
	logs []model.AuditLog
	err  error
}

func (sink *fakeAuditSink) Insert(
	_ context.Context,
	log model.AuditLog,
) (model.AuditLog, error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.err != nil {
		return model.AuditLog{}, sink.err
	}
	sink.logs = append(sink.logs, log)
	return log, nil
}

func (sink *fakeAuditSink) calls() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return len(sink.logs)
}

type fakeAuditReader struct {
	mu            sync.Mutex
	logs          []model.AuditLog
	totalOverride int
	filters       []model.AuditFilter
	pages         []int
	sizes         []int
}

func (reader *fakeAuditReader) FilteredPage(
	_ context.Context,
	filter model.AuditFilter,
	page int,
	size int,
) (store.AuditPage, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.filters = append(reader.filters, filter)
	reader.pages = append(reader.pages, page)
	reader.sizes = append(reader.sizes, size)
	total := len(reader.logs)
	if reader.totalOverride != 0 {
		total = reader.totalOverride
	}
	start := (page - 1) * size
	if start > len(reader.logs) {
		start = len(reader.logs)
	}
	end := start + size
	if end > len(reader.logs) {
		end = len(reader.logs)
	}
	list := append([]model.AuditLog(nil), reader.logs[start:end]...)
	return store.AuditPage{Total: int64(total), Page: page, PageSize: size, List: list}, nil
}

func (reader *fakeAuditReader) lastFilter() model.AuditFilter {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.filters[len(reader.filters)-1]
}

func (reader *fakeAuditReader) lastPage() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.pages[len(reader.pages)-1]
}

func (reader *fakeAuditReader) lastSize() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.sizes[len(reader.sizes)-1]
}

func (reader *fakeAuditReader) pageNumbers() []int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return append([]int(nil), reader.pages...)
}

func (reader *fakeAuditReader) pageSizes() []int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return append([]int(nil), reader.sizes...)
}

func (reader *fakeAuditReader) calls() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return len(reader.pages)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func completeAuditLog(decision string) model.AuditLog {
	log := model.AuditLog{
		ID:             7,
		TS:             time.Date(2026, time.September, 9, 12, 34, 56, 0, time.UTC),
		AgentID:        stringPointer("agent-1"),
		DatasourceID:   stringPointer("datasource-1"),
		SessionID:      stringPointer("session-1"),
		ConversationID: stringPointer("conversation-1"),
		MCPTool:        stringPointer("query"),
		DBType:         stringPointer("postgres"),
		SQLRaw:         stringPointer("SELECT email FROM customers"),
		SQLNorm:        stringPointer("SELECT email FROM customers"),
		StmtType:       stringPointer("SELECT"),
		Objects:        stringPointer("public.customers"),
		Decision:       decision,
		RuleHits:       stringPointer("[]"),
		RiskLevel:      intPointer(4),
		EstRows:        int64Pointer(10),
		RowsReturned:   intPointer(2),
		LatencyMS:      int64Pointer(3),
		ClientIP:       stringPointer("127.0.0.1"),
		ModelName:      stringPointer("model"),
		ErrorMsg:       stringPointer(""),
		Action:         stringPointer(ActionDiscover),
		ActorType:      stringPointer("admin"),
		ActorID:        stringPointer("root"),
		DetailsJSON:    stringPointer(`{"findings_count":2}`),
	}
	if decision == "error" {
		log.ErrorCode = stringPointer("DB_OBJECT_NOT_FOUND")
	}
	return log
}

func stringPointer(value string) *string {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}

var (
	_ Sink   = (*fakeAuditSink)(nil)
	_ Reader = (*fakeAuditReader)(nil)
)
