package parser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

type corpusCase struct {
	Name               string            `json:"name"`
	SQL                string            `json:"sql"`
	StatementType      model.StmtType    `json:"stmt_type"`
	Tables             []model.ObjectRef `json:"tables"`
	Columns            []string          `json:"columns"`
	HasWhere           bool              `json:"has_where"`
	WhereTautology     bool              `json:"where_tautology"`
	HasLimit           bool              `json:"has_limit"`
	Functions          []string          `json:"functions"`
	Operations         []string          `json:"operations"`
	NormalizedContains []string          `json:"normalized_contains"`
	NormalizedExcludes []string          `json:"normalized_excludes"`
	RequireTables      bool              `json:"require_tables"`
	WantError          bool              `json:"want_error"`
	IsMulti            bool              `json:"is_multi"`
}

func TestCorpus(t *testing.T) {
	tests := []struct {
		dialect  model.DBDialect
		filename string
	}{
		{dialect: postgresDialect, filename: "postgres.json"},
		{dialect: mysqlDialect, filename: "mysql.json"},
	}

	for _, test := range tests {
		t.Run(string(test.dialect), func(t *testing.T) {
			cases := loadCorpus(t, test.filename)
			expectedCases := 20
			if test.dialect == postgresDialect {
				expectedCases = 22
			}
			require.Len(t, cases, expectedCases)
			approvedParser, err := NewParser(test.dialect)
			require.NoError(t, err)

			for _, corpus := range cases {
				t.Run(corpus.Name, func(t *testing.T) {
					ast, err := approvedParser.Parse(corpus.SQL)
					require.NotNil(t, ast)
					require.Equal(t, test.dialect, ast.Dialect)
					require.Equal(t, corpus.SQL, ast.RawSQL)
					require.Equal(t, corpus.IsMulti, ast.IsMulti)

					if corpus.WantError {
						require.Error(t, err)
						require.True(t, errors.Is(err, ErrUnparseable))
						return
					}

					require.NoError(t, err)
					require.Equal(t, corpus.StatementType, ast.StmtType)
					require.NotEmpty(t, ast.Normalized)
					require.Equal(t, corpus.HasWhere, ast.HasWhere)
					require.Equal(t, corpus.WhereTautology, ast.WhereTautology)
					require.Equal(t, corpus.HasLimit, ast.HasLimit)
					require.Nil(t, ast.Explain)
					require.Equal(t, sortedObjects(corpus.Tables), sortedObjects(ast.Tables))
					require.Equal(t, sortedStrings(corpus.Columns), sortedStrings(ast.Columns))
					require.Equal(t, sortedStrings(corpus.Functions), sortedStrings(ast.Functions))
					require.Equal(t, sortedStrings(corpus.Operations), sortedStrings(legacyCorpusOperations(ast.Operations)))
					require.Equal(t, requiresTargetTable(corpus.Operations), corpus.RequireTables)
					if corpus.RequireTables {
						require.NotEmpty(t, corpus.Tables)
						require.NotEmpty(t, ast.Tables)
					}
					for _, placeholder := range corpus.NormalizedContains {
						require.Contains(t, ast.Normalized, placeholder)
					}
					for _, literal := range corpus.NormalizedExcludes {
						require.NotContains(t, ast.Normalized, literal)
					}
				})
			}
		})
	}
}

func legacyCorpusOperations(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		name, _, found := strings.Cut(value, ":")
		if found && (name == nestingDepthOperation || name == unionCountOperation || name == selectColumnOperation) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func requiresTargetTable(operations []string) bool {
	tableTargetingOperations := map[string]struct{}{
		"ALTER TABLE":    {},
		"COPY PROGRAM":   {},
		"COPY":           {},
		"CREATE TABLE":   {},
		"DELETE":         {},
		"DROP TABLE":     {},
		"GRANT":          {},
		"INSERT":         {},
		"REINDEX":        {},
		"TRUNCATE TABLE": {},
		"UPDATE":         {},
		"VACUUM":         {},
		"VACUUM FULL":    {},
	}
	for _, operation := range operations {
		if _, exists := tableTargetingOperations[operation]; exists {
			return true
		}
	}
	return false
}

func sortedStrings(values []string) []string {
	result := append([]string{}, values...)
	sort.Strings(result)
	return result
}

func sortedObjects(values []model.ObjectRef) []model.ObjectRef {
	result := append([]model.ObjectRef{}, values...)
	sort.Slice(result, func(left, right int) bool {
		leftKey := result[left].Schema + "\x00" + result[left].Table + "\x00" + result[left].Alias
		rightKey := result[right].Schema + "\x00" + result[right].Table + "\x00" + result[right].Alias
		return leftKey < rightKey
	})
	return result
}

func TestNewParserRejectsUnsupportedDialect(t *testing.T) {
	approvedParser, err := NewParser(model.DBDialect("sqlite"))
	require.Nil(t, approvedParser)
	require.True(t, errors.Is(err, ErrUnsupportedDialect))
}

func TestMalformedSQLReturnsErrorWithoutPanic(t *testing.T) {
	for _, dialect := range []model.DBDialect{postgresDialect, mysqlDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approvedParser, err := NewParser(dialect)
			require.NoError(t, err)

			require.NotPanics(t, func() {
				ast, parseError := approvedParser.Parse("\x00\xffSELECT FROM")
				require.NotNil(t, ast)
				require.True(t, errors.Is(parseError, ErrUnparseable))
			})
		})
	}
}

func loadCorpus(t *testing.T, filename string) []corpusCase {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "tests", "corpus", filename)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var cases []corpusCase
	require.NoError(t, decoder.Decode(&cases))
	var trailing any
	err = decoder.Decode(&trailing)
	require.True(t, errors.Is(err, io.EOF), fmt.Sprintf("unexpected trailing JSON in %s", filename))
	return cases
}
