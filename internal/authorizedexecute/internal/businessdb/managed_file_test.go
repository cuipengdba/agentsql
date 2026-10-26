package businessdb

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagedFileSheetsValidateTypedBoundsBeforeConnecting(t *testing.T) {
	good := ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "地区", Type: "string"}, {Name: "amount", Type: "int"}, {Name: "sold_at", Type: "datetime"}},
		Rows: [][]any{{"华东", int64(10), time.Now().UTC()}, {nil, nil, nil}}}
	require.NoError(t, validateManagedFileSheets([]ManagedFileSheet{good}))
	for _, test := range []struct {
		name  string
		sheet ManagedFileSheet
	}{
		{"duplicate casefolded column", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "A", Type: "string"}, {Name: "a", Type: "string"}}}},
		{"oversized header", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: strings.Repeat("a", 64), Type: "string"}}}},
		{"control header", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "a\x00b", Type: "string"}}}},
		{"unknown type", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "a", Type: "sql"}}}},
		{"row width", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "a", Type: "int"}}, Rows: [][]any{{int64(1), int64(2)}}}},
		{"wrong numeric type", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "a", Type: "int"}}, Rows: [][]any{{"1"}}}},
		{"nan", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "a", Type: "float"}}, Rows: [][]any{{math.NaN()}}}},
		{"oversized cell", ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "a", Type: "string"}}, Rows: [][]any{{strings.Repeat("a", 65537)}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorIs(t, validateManagedFileSheets([]ManagedFileSheet{test.sheet}), ErrManagedFileInvalid)
		})
	}
	require.ErrorIs(t, validateManagedFileSheets(nil), ErrManagedFileInvalid)
	require.ErrorIs(t, validateManagedFileSheets(make([]ManagedFileSheet, 9)), ErrManagedFileInvalid)
	large := ManagedFileSheet{Columns: []ManagedFileColumn{{Name: "content", Type: "string"}}}
	for range 257 {
		large.Rows = append(large.Rows, []any{strings.Repeat("a", 65536)})
	}
	require.ErrorIs(t, validateManagedFileSheets([]ManagedFileSheet{large}), ErrManagedFileInvalid)
	operation, err := NewManagedFileOperationID()
	require.NoError(t, err)
	name, err := ManagedFileName(operation, 0)
	require.NoError(t, err)
	require.Regexp(t, managedFileTableName, name)
	same, err := ManagedFileName(operation, 0)
	require.NoError(t, err)
	require.Equal(t, name, same)
	other, err := ManagedFileName(operation, 1)
	require.NoError(t, err)
	require.NotEqual(t, name, other)
	_, err = ManagedFileName(ManagedFileOperationID{}, 0)
	require.ErrorIs(t, err, ErrManagedFileInvalid)
	parsed, err := ParseManagedFileOperationID("11111111111111111111111111111111")
	require.NoError(t, err)
	require.NotEqual(t, ManagedFileOperationID{}, parsed)
	for _, invalid := range []string{"", "1111", "00000000000000000000000000000000", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"} {
		_, err = ParseManagedFileOperationID(invalid)
		require.ErrorIs(t, err, ErrManagedFileInvalid)
	}
}
