package authorizedexecute

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// TestB2S7FacadeAttackMatrix fixes the connection-free edge of the S7 matrix
// in the repository. The same gate as the real PG14/18 suite keeps ordinary
// unit runs cheap while still compiling these assertions on every go test.
func TestB2S7FacadeAttackMatrix(t *testing.T) {
	if os.Getenv("AGENTSQL_B2_S7_MATRIX") != "1" {
		t.Skip("set AGENTSQL_B2_S7_MATRIX=1 to run the B2 S7 facade attack matrix")
	}
	t.Run("statement-router", func(t *testing.T) {
		gateway := NewGateway(false)
		datasource := model.Datasource{ID: "s7", DBType: "postgres", Host: "127.0.0.1", Port: 5432,
			Database: "unused", Username: "unused", ConnLimit: 1, StmtTimeoutMS: 100}
		request := ColumnAuthorizationRequest{}
		for _, attack := range []struct{ name, sql string }{
			{"stacked", `SELECT 1; SELECT 'secret'`},
			{"comment-stacked", `SeLeCt 1 /* ; */; DROP TABLE secret_table`},
			{"insert", `INSERT INTO secret_table VALUES (1)`},
			{"update", `UPDATE secret_table SET secret='x'`},
			{"delete", `DELETE FROM secret_table`},
			{"copy", `COPY secret_table TO PROGRAM 'id'`},
			{"prepare", `PREPARE leak AS SELECT secret FROM secret_table`},
			{"execute", `EXECUTE leak`},
		} {
			t.Run(attack.name, func(t *testing.T) {
				statement, err := gateway.AuthorizedExecute(WithColumnAuthorization(context.Background(), request), datasource, nil, attack.sql, "")
				require.Nil(t, statement)
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret")
				require.NotContains(t, err.Error(), "leak")
			})
		}
	})

	t.Run("preparse-resource-limits", func(t *testing.T) {
		tests := []struct {
			name   string
			sql    string
			limits Limits
			reason Reason
		}{
			{"raw-bytes", `SELECT '` + strings.Repeat("x", 256) + `'`, Limits{RawSQLBytes: 64}, ReasonRequestTooLarge},
			{"invalid-utf8", string([]byte{'S', 'E', 'L', 'E', 'C', 'T', ' ', 0xff}), Limits{}, ReasonRequestTooLarge},
			{"tokens", `SELECT a+a+a+a+a+a+a+a+a+a FROM s7.t`, Limits{Tokens: 8}, ReasonTokenLimit},
			{"nesting", `SELECT (((((((((1)))))))))`, Limits{LexicalDepth: 4}, ReasonNestingLimit},
			{"query-blocks", `SELECT 1 WHERE EXISTS (SELECT 1 WHERE EXISTS (SELECT 1))`, Limits{QueryBlocks: 2}, ReasonQueryBlockLimit},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				err := ValidatePreParse(test.sql, nil, test.limits)
				var auth *AuthError
				require.ErrorAs(t, err, &auth)
				require.Equal(t, test.reason, auth.Reason)
			})
		}
	})

	t.Run("result-bounds-no-partial-data", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			result model.QueryResult
			limits Limits
			reason Reason
		}{
			{"cell", model.QueryResult{Columns: []string{"v"}, Rows: [][]string{{strings.Repeat("s", 65)}}}, Limits{RawCellBytes: 64}, ReasonCellLimit},
			{"row", model.QueryResult{Columns: []string{"a", "b"}, Rows: [][]string{{strings.Repeat("a", 40), strings.Repeat("b", 40)}}}, Limits{RawCellBytes: 64, RawRowBytes: 64}, ReasonRowLimit},
			{"result", model.QueryResult{Columns: []string{"v"}, Rows: [][]string{{strings.Repeat("a", 40)}, {strings.Repeat("b", 40)}}}, Limits{RawCellBytes: 64, RawRowBytes: 64, RawResultBytes: 64}, ReasonResultLimit},
		} {
			t.Run(test.name, func(t *testing.T) {
				err := ValidateResult(test.result, false, test.limits)
				var auth *AuthError
				require.ErrorAs(t, err, &auth)
				require.Equal(t, test.reason, auth.Reason)
			})
		}
	})

	t.Run("diagnostic-redaction", func(t *testing.T) {
		secret := "s7-secret-column DETAIL=credential password=top-secret"
		mapped := StableError(errors.New(secret))
		require.Equal(t, ReasonDatabaseFailure, mapped.Reason)
		require.NotContains(t, mapped.Error(), "secret")
		require.NotContains(t, mapped.Error(), "credential")
	})
}
