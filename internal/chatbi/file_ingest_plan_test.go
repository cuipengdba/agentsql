package chatbi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFileImportPlanChecksStagedIdentityAndExactIntegerRounding(t *testing.T) {
	now := time.Now().UTC()
	stage := fileStage{FilePath: "agentsql-file-123.csv", Original: "synthetic.csv",
		Data: []byte("region,amount\neast,1.5\nwest,-1.5\nnorth,9223372036854775807\n"), Expires: now.Add(time.Hour)}
	var request FileImportRequest
	require.NoError(t, json.Unmarshal([]byte(`{"filePath":"agentsql-file-123.csv","sheets":[{"sheetName":"Sheet1","fields":[{"fieldName":"region","fieldType":"string"},{"fieldName":"amount","fieldType":"int"}]}]}`), &request))
	plan, err := planFileImport(stage, request, now)
	require.NoError(t, err)
	require.Len(t, plan.Sheets, 1)
	require.Equal(t, []any{"east", int64(2)}, plan.Sheets[0].Rows[0])
	require.Equal(t, []any{"west", int64(-2)}, plan.Sheets[0].Rows[1])
	require.Equal(t, []any{"north", int64(9223372036854775807)}, plan.Sheets[0].Rows[2])

	request.FilePath = "../agentsql-file-123.csv"
	_, err = planFileImport(stage, request, now)
	require.ErrorIs(t, err, errFileImport)
	request.FilePath = stage.FilePath
	request.Sheets[0].Fields[1].Name = "hidden"
	_, err = planFileImport(stage, request, now)
	require.ErrorIs(t, err, errFileImport)
	request.Sheets[0].Fields[1].Name = "amount"
	request.Sheets = append(request.Sheets, request.Sheets[0])
	_, err = planFileImport(stage, request, now)
	require.ErrorIs(t, err, errFileImport)
	request.Sheets = request.Sheets[:1]
	_, err = planFileImport(stage, request, now.Add(2*time.Hour))
	require.ErrorIs(t, err, errFileImport)
}

func TestFileImportPlanRejectsNumericOverflowAndUnsupportedType(t *testing.T) {
	now := time.Now().UTC()
	stage := fileStage{FilePath: "agentsql-file-123.csv", Original: "synthetic.csv",
		Data: []byte("amount\n9223372036854775808\n"), Expires: now.Add(time.Hour)}
	var request FileImportRequest
	require.NoError(t, json.Unmarshal([]byte(`{"filePath":"agentsql-file-123.csv","sheets":[{"sheetName":"Sheet1","fields":[{"fieldName":"amount","fieldType":"int"}]}]}`), &request))
	_, err := planFileImport(stage, request, now)
	require.ErrorIs(t, err, errFileImport)
	request.Sheets[0].Fields[0].Type = "float"
	plan, err := planFileImport(stage, request, now)
	require.NoError(t, err)
	require.Len(t, plan.Sheets[0].Rows, 1)
	request.Sheets[0].Fields[0].Type = "sql"
	_, err = planFileImport(stage, request, now)
	require.ErrorIs(t, err, errFileImport)
}

func TestFileImportPlanProducesTypedUTCInstants(t *testing.T) {
	now := time.Now().UTC()
	stage := fileStage{FilePath: "agentsql-file-dates.csv", Original: "dates.csv",
		Data: []byte("occurred\n2026-10-26T09:30:00+08:00\n2026-10-27 12:00:00\n"), Expires: now.Add(time.Hour)}
	var request FileImportRequest
	require.NoError(t, json.Unmarshal([]byte(`{"filePath":"agentsql-file-dates.csv","sheets":[{"sheetName":"Sheet1","fields":[{"fieldName":"occurred","fieldType":"datetime"}]}]}`), &request))
	plan, err := planFileImport(stage, request, now)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC), plan.Sheets[0].Rows[0][0])
	require.Equal(t, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), plan.Sheets[0].Rows[1][0])
}
