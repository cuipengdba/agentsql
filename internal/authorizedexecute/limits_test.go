package authorizedexecute

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func requireReason(t *testing.T, err error, reason Reason) {
	t.Helper()
	var authorizationError *AuthError
	require.ErrorAs(t, err, &authorizationError)
	require.Equal(t, reason, authorizationError.Reason)
}

func TestPreParseLimitsFailBeforeParser(t *testing.T) {
	limits := DefaultLimits
	limits.RawSQLBytes, limits.Tokens, limits.LexicalDepth = 8, 2, 1
	requireReason(t, ValidatePreParse("SELECT 123", nil, limits), ReasonRequestTooLarge)
	limits.RawSQLBytes = DefaultLimits.RawSQLBytes
	requireReason(t, ValidatePreParse("a b c", nil, limits), ReasonTokenLimit)
	requireReason(t, ValidatePreParse("((x))", nil, limits), ReasonNestingLimit)
	require.NoError(t, ValidatePreParse("'((' -- ))\n", nil, limits))
	limits.Tokens = DefaultLimits.Tokens
	limits.LexicalDepth = DefaultLimits.LexicalDepth
	limits.QueryBlocks = 1
	requireReason(t, ValidatePreParse("SELECT 1 WHERE EXISTS (SELECT 2)", nil, limits), ReasonQueryBlockLimit)
}

func TestBudgetSharesCheckedWork(t *testing.T) {
	limits := DefaultLimits
	limits.WorkUnits = 3
	budget := NewBudget(limits)
	require.NoError(t, budget.ChargeWork(2))
	requireReason(t, budget.ChargeWork(2), ReasonWorkLimit)
	limits.DependencyEdges = 1
	budget = NewBudget(limits)
	requireReason(t, budget.ChargeAST(&model.AST{ProjectionLineages: []model.ProjectionLineage{{Arms: []model.LineageArm{{Dependencies: []model.ColumnDependency{{}, {}}}}}}}), ReasonDependencyLimit)
}

func TestResultCapsNeverTruncate(t *testing.T) {
	limits := DefaultLimits
	limits.RawCellBytes = 3
	result := model.QueryResult{Columns: []string{"v"}, Rows: [][]string{{"secret"}}}
	requireReason(t, ValidateResult(result, false, limits), ReasonCellLimit)
	require.Equal(t, "secret", result.Rows[0][0])
	limits = DefaultLimits
	limits.OutputColumns = 1
	requireReason(t, ValidateResult(model.QueryResult{Columns: []string{"a", "b"}}, false, limits), ReasonProjectionLimit)
}

func TestReservationLimitsAndIdempotentRelease(t *testing.T) {
	pool := NewReservationPool(ReservationLimits{PerAgent: 1, PerTenant: 1, PerDatasource: 1, GlobalMemoryBytes: 8})
	reservation, err := pool.Reserve("agent", "tenant", "datasource", 8)
	require.NoError(t, err)
	_, err = pool.Reserve("agent", "tenant", "datasource", 1)
	requireReason(t, err, ReasonConcurrencyLimit)
	reservation.Release()
	reservation.Release()
	second, err := pool.Reserve("agent", "tenant", "datasource", 8)
	require.NoError(t, err)
	second.Release()
	_, err = pool.Reserve("other", "other", "other", 9)
	requireReason(t, err, ReasonMemoryLimit)
}

func TestDefaultReservationBaselineAndGlobalOverflow(t *testing.T) {
	requestMemory := defaultStatementMemoryReservation()
	require.Equal(t, int64(52<<20), requestMemory)

	pool := NewReservationPool(DefaultReservationLimits)
	reservations := make([]*Reservation, 0, DefaultReservationLimits.PerDatasource)
	for index := 0; index < DefaultReservationLimits.PerDatasource; index++ {
		reservation, err := pool.Reserve(
			fmt.Sprintf("agent-%d", index),
			fmt.Sprintf("tenant-%d", index),
			"baseline-datasource",
			requestMemory,
		)
		require.NoError(t, err, "normal datasource concurrency %d must fit", index+1)
		reservations = append(reservations, reservation)
	}
	require.Equal(t, int64(192<<20), DefaultReservationLimits.GlobalMemoryBytes-int64(len(reservations))*requestMemory)
	for _, reservation := range reservations {
		reservation.Release()
	}

	// Distinct identities avoid the concurrency gates and isolate the global
	// memory boundary: 19 reservations fit, while the 20th exceeds 1 GiB.
	reservations = reservations[:0]
	for index := 0; index < 19; index++ {
		reservation, err := pool.Reserve(
			fmt.Sprintf("overflow-agent-%d", index),
			fmt.Sprintf("overflow-tenant-%d", index),
			fmt.Sprintf("overflow-datasource-%d", index),
			requestMemory,
		)
		require.NoError(t, err)
		reservations = append(reservations, reservation)
	}
	_, err := pool.Reserve("overflow-agent-20", "overflow-tenant-20", "overflow-datasource-20", requestMemory)
	requireReason(t, err, ReasonMemoryLimit)
	for _, reservation := range reservations {
		reservation.Release()
	}

	// Released accounting must not bleed into the next request or subtest.
	reservation, err := pool.Reserve("next-agent", "next-tenant", "next-datasource", requestMemory)
	require.NoError(t, err)
	reservation.Release()
}

func TestLockAndStatementDeadlinesShareParent(t *testing.T) {
	request, cancelRequest := WithRequestDeadline(context.Background(), time.Second)
	defer cancelRequest()
	locked, cancelLock := WithLockDeadline(request, time.Second)
	defer cancelLock()
	statement, cancelStatement := WithStatementDeadline(locked, 5*time.Second)
	defer cancelStatement()
	lockDeadline, lockOK := locked.Deadline()
	statementDeadline, statementOK := statement.Deadline()
	require.True(t, lockOK && statementOK)
	require.False(t, statementDeadline.After(lockDeadline))
}

type observingWriter struct {
	header http.Header
	writes int
	bytes  bytes.Buffer
}

func (writer *observingWriter) Header() http.Header { return writer.header }
func (*observingWriter) WriteHeader(int)            {}
func (writer *observingWriter) Write(value []byte) (int, error) {
	writer.writes++
	return writer.bytes.Write(value)
}

func TestSealedResponseEmitsZeroBytesUntilSeal(t *testing.T) {
	destination := &observingWriter{header: make(http.Header)}
	buffer := NewSealedResponseWriter(64)
	buffer.Header().Set("Content-Type", "application/json")
	_, err := buffer.Write([]byte(`{"value":"clean"}`))
	require.NoError(t, err)
	buffer.Flush()
	require.Zero(t, destination.writes)
	require.Empty(t, destination.header)
	require.ErrorIs(t, buffer.CommitTo(destination), ErrUnsealed)
	require.Zero(t, destination.writes)
	require.NoError(t, buffer.Seal())
	require.NoError(t, buffer.CommitTo(destination))
	require.Equal(t, 1, destination.writes)
	require.NotContains(t, destination.bytes.String(), "secret")
}

func TestSealedHTTPDropsOversizedPrivateBody(t *testing.T) {
	handler := SealedHTTP(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 9)))
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}), 8)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.NotContains(t, recorder.Body.String(), strings.Repeat("x", 9))
}

type headerOnlyReader struct {
	header       []byte
	payloadReads int
}

func (reader *headerOnlyReader) Read(target []byte) (int, error) {
	if len(reader.header) > 0 {
		count := copy(target, reader.header)
		reader.header = reader.header[count:]
		return count, nil
	}
	reader.payloadReads++
	return 0, errors.New("payload must not be read")
}

func TestFrameCapPrecedesPayloadAllocationAndRead(t *testing.T) {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 1024)
	reader := &headerOnlyReader{header: header}
	_, err := ReadBoundedFrame(reader, 16)
	requireReason(t, err, ReasonFrameLimit)
	require.Zero(t, reader.payloadReads)
}

func TestStableErrorDoesNotLeakCause(t *testing.T) {
	secret := errors.New("DETAIL password=top-secret host=db.internal")
	mapped := StableError(secret)
	require.Equal(t, ReasonDatabaseFailure, mapped.Reason)
	require.NotContains(t, mapped.Error(), "top-secret")
}

func TestFixedExecutionErrorPreservesOnlyA4Mapping(t *testing.T) {
	databaseError := NewDBError(DBErrorKindConstraint, DBErrorCodeConstraint, DBStageExecute)
	databaseError.DriverCode = "DETAIL password=top-secret"
	mapped := fixedExecutionError(databaseError)
	var classified *DBError
	require.ErrorAs(t, mapped, &classified)
	require.Equal(t, DBErrorCodeConstraint, classified.Code)
	require.Equal(t, DBStageExecute, classified.Stage)
	encoded, err := json.Marshal(classified)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "top-secret")
	require.NotContains(t, classified.Error(), "top-secret")
}
